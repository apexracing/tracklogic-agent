package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/apexracing/tracklogic-agent/engine"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/task"
)

type TeamMode string

const (
	ModeSequential     TeamMode = "sequential"
	ModeParallel       TeamMode = "parallel"
	ModeLeaderFollower TeamMode = "leader_follower"
)

type Team struct {
	ID          string
	Name        string
	Mode        TeamMode
	Agents      []*engine.Agent
	Leader      *engine.Agent
	SharedModel model.Model
	logger      *slog.Logger
}

type TeamConfig struct {
	ID          string
	Name        string
	Mode        TeamMode
	Agents      []*engine.Agent
	Leader      *engine.Agent
	SharedModel model.Model
	Logger      *slog.Logger
}

func NewTeam(cfg TeamConfig) *Team {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Team{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Mode:        cfg.Mode,
		Agents:      append([]*engine.Agent(nil), cfg.Agents...),
		Leader:      cfg.Leader,
		SharedModel: cfg.SharedModel,
		logger:      logger.With("component", "team", "name", cfg.Name),
	}
}

type TeamOutput struct {
	AgentOutputs map[string]*engine.RunOutput
	FinalOutput  string
	Success      bool
	Error        string
	Err          error `json:"-"`
}

func (t *Team) Run(ctx context.Context, input string) *TeamOutput {
	if err := t.Validate(); err != nil {
		return &TeamOutput{Success: false, Error: err.Error(), Err: err}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	t.logger.Info("team run started", "mode", t.Mode)
	if err := t.emitCheckpoint(ctx, "starting", 0, input, nil, ""); err != nil {
		return &TeamOutput{Success: false, Error: err.Error(), Err: err}
	}

	switch t.Mode {
	case ModeSequential:
		return t.runSequential(ctx, input)
	case ModeParallel:
		return t.runParallel(ctx, input)
	case ModeLeaderFollower:
		return t.runLeaderFollower(ctx, input)
	default:
		err := fmt.Errorf("unknown mode: %s", t.Mode)
		return &TeamOutput{Success: false, Error: err.Error(), Err: err}
	}
}

// Resume continues a Team suspended at a safe Agent interaction checkpoint.
func (t *Team) Resume(ctx context.Context, checkpoint task.TeamCheckpoint, toolResult string) *TeamOutput {
	if err := t.Validate(); err != nil {
		return &TeamOutput{Success: false, Error: err.Error(), Err: err}
	}
	if checkpoint.TeamID != t.Name || checkpoint.Agent == nil || checkpoint.Agent.PendingToolCall == nil {
		err := fmt.Errorf("team checkpoint is not resumable")
		return &TeamOutput{Success: false, Error: err.Error(), Err: err}
	}
	pendingAgent := t.agentByName(checkpoint.Agent.AgentID)
	if pendingAgent == nil {
		err := fmt.Errorf("team agent %q not found", checkpoint.Agent.AgentID)
		return &TeamOutput{Success: false, Error: err.Error(), Err: err}
	}
	options := []engine.RunOption(nil)
	if t.SharedModel != nil {
		options = append(options, engine.WithModel(t.SharedModel))
	}
	resumed := pendingAgent.Resume(ctx, *checkpoint.Agent, toolResult, options...)
	outputs := make(map[string]*engine.RunOutput, len(checkpoint.AgentOutputs)+1)
	for name, content := range checkpoint.AgentOutputs {
		outputs[name] = &engine.RunOutput{Content: content, Success: true}
	}
	outputs[pendingAgent.Name()] = resumed
	if !resumed.Success {
		return teamFailure(outputs, resumed.Content, fmt.Errorf("agent %s failed: %w", pendingAgent.Name(), runOutputError(resumed)))
	}

	switch t.Mode {
	case ModeSequential:
		current := resumed.Content
		start := checkpoint.CurrentIndex + 1
		for index := start; index < len(t.Agents); index++ {
			runtimeAgent := t.Agents[index]
			if err := t.emitCheckpoint(ctx, "agent", index, current, outputs, runtimeAgent.Name()); err != nil {
				return teamFailure(outputs, current, err)
			}
			output := t.runAgent(ctx, runtimeAgent, current)
			outputs[runtimeAgent.Name()] = output
			if !output.Success {
				return teamFailure(outputs, output.Content, fmt.Errorf("agent %s failed: %w", runtimeAgent.Name(), runOutputError(output)))
			}
			current = output.Content
		}
		return &TeamOutput{AgentOutputs: outputs, FinalOutput: current, Success: true}
	case ModeParallel:
		for _, runtimeAgent := range t.Agents {
			name := runtimeAgent.Name()
			if _, exists := outputs[name]; exists {
				continue
			}
			saved := checkpoint.Agents[name]
			if saved == nil || saved.PendingToolCall != nil || saved.LastContent == "" {
				err := fmt.Errorf("parallel agent %q state is incomplete; refusing unsafe replay", name)
				return teamFailure(outputs, "", err)
			}
			outputs[name] = &engine.RunOutput{Content: saved.LastContent, Success: true, Messages: saved.Messages, ToolCalls: saved.ToolCalls, TotalTokens: saved.TotalTokens, LoopCount: saved.LoopCount}
		}
		parts := make([]string, 0, len(t.Agents))
		for _, runtimeAgent := range t.Agents {
			parts = append(parts, fmt.Sprintf("**%s**: %s", runtimeAgent.Name(), outputs[runtimeAgent.Name()].Content))
		}
		return &TeamOutput{AgentOutputs: outputs, FinalOutput: strings.Join(parts, "\n\n"), Success: true}
	case ModeLeaderFollower:
		return t.resumeLeaderFollower(ctx, checkpoint, outputs, resumed)
	default:
		err := fmt.Errorf("unknown team mode %q", t.Mode)
		return teamFailure(outputs, "", err)
	}
}

func (t *Team) resumeLeaderFollower(ctx context.Context, checkpoint task.TeamCheckpoint, outputs map[string]*engine.RunOutput, resumed *engine.RunOutput) *TeamOutput {
	leaderName := t.Leader.Name()
	switch checkpoint.Phase {
	case "leader_synthesis":
		return &TeamOutput{AgentOutputs: outputs, FinalOutput: resumed.Content, Success: true}
	case "leader_plan":
		// Continue below with the resumed plan.
	case "follower":
		// Existing outputs and the resumed follower are already populated.
	default:
		return teamFailure(outputs, "", fmt.Errorf("unknown leader/follower checkpoint phase %q", checkpoint.Phase))
	}
	plan := outputs[leaderName]
	if checkpoint.Phase == "leader_plan" {
		plan = resumed
		outputs[leaderName] = resumed
	}
	if plan == nil || !plan.Success {
		return teamFailure(outputs, "", fmt.Errorf("leader plan is unavailable"))
	}
	for index, runtimeAgent := range t.Agents {
		name := runtimeAgent.Name()
		if name == leaderName {
			continue
		}
		if existing := outputs[name]; existing != nil && existing.Success {
			continue
		}
		if index <= checkpoint.CurrentIndex && checkpoint.Phase == "follower" {
			continue
		}
		followerInput := fmt.Sprintf("Plan: %s\n\nTask: %s\n\nYour role: %s", plan.Content, checkpoint.CurrentInput, runtimeAgent.SystemPrompt())
		output := t.runAgent(ctx, runtimeAgent, followerInput)
		outputs[name] = output
		if !output.Success {
			return teamFailure(outputs, "", fmt.Errorf("follower %s failed: %w", name, runOutputError(output)))
		}
	}
	synthesis := t.runAgent(ctx, t.Leader, fmt.Sprintf("Synthesize the following results into a final answer.\n\nOriginal request: %s\n\nResults:\n%s", checkpoint.CurrentInput, t.formatOutputs(outputs)))
	outputs[leaderName] = synthesis
	if !synthesis.Success {
		return teamFailure(outputs, synthesis.Content, fmt.Errorf("leader synthesis failed: %w", runOutputError(synthesis)))
	}
	return &TeamOutput{AgentOutputs: outputs, FinalOutput: synthesis.Content, Success: true}
}

func (t *Team) agentByName(name string) *engine.Agent {
	for _, runtimeAgent := range t.Agents {
		if runtimeAgent.Name() == name {
			return runtimeAgent
		}
	}
	if t.Leader != nil && t.Leader.Name() == name {
		return t.Leader
	}
	return nil
}

func teamFailure(outputs map[string]*engine.RunOutput, final string, err error) *TeamOutput {
	if err == nil {
		err = fmt.Errorf("team failed")
	}
	return &TeamOutput{AgentOutputs: outputs, FinalOutput: final, Success: false, Error: err.Error(), Err: err}
}

func runOutputError(output *engine.RunOutput) error {
	if output == nil {
		return fmt.Errorf("Agent returned no output")
	}
	if output.Err != nil {
		return output.Err
	}
	if output.Error != "" {
		return errors.New(output.Error)
	}
	return fmt.Errorf("Agent failed")
}

// Validate checks the structural invariants required by every Team run.
func (t *Team) Validate() error {
	if t == nil {
		return fmt.Errorf("team is required")
	}
	if t.Name == "" || t.Name != strings.TrimSpace(t.Name) {
		return fmt.Errorf("team name must be non-empty and must not have surrounding whitespace")
	}
	switch t.Mode {
	case ModeSequential, ModeParallel, ModeLeaderFollower:
	default:
		return fmt.Errorf("unknown team mode: %s", t.Mode)
	}
	if len(t.Agents) == 0 {
		return fmt.Errorf("team %q has no agents", t.Name)
	}
	seen := make(map[string]struct{}, len(t.Agents))
	for index, runtimeAgent := range t.Agents {
		if runtimeAgent == nil {
			return fmt.Errorf("team %q agent %d is nil", t.Name, index)
		}
		name := runtimeAgent.Name()
		if name == "" {
			return fmt.Errorf("team %q agent %d has no name", t.Name, index)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("team %q has duplicate agent name %q", t.Name, name)
		}
		seen[name] = struct{}{}
	}
	if t.Mode == ModeLeaderFollower && t.Leader == nil {
		return fmt.Errorf("team %q requires a leader", t.Name)
	}
	return nil
}

func (t *Team) runAgent(ctx context.Context, runtimeAgent *engine.Agent, input string) *engine.RunOutput {
	if t.SharedModel == nil {
		return runtimeAgent.Run(ctx, input)
	}
	return runtimeAgent.Run(ctx, input, engine.WithModel(t.SharedModel))
}

func (t *Team) runSequential(ctx context.Context, input string) *TeamOutput {
	agents := append([]*engine.Agent(nil), t.Agents...)
	if len(agents) == 0 {
		err := fmt.Errorf("no agents configured")
		return &TeamOutput{Success: false, Error: err.Error(), Err: err}
	}

	outputs := make(map[string]*engine.RunOutput)
	currentInput := input

	for index, agent := range agents {
		name := agent.Name()
		if err := t.emitCheckpoint(ctx, "agent", index, currentInput, outputs, name); err != nil {
			return &TeamOutput{AgentOutputs: outputs, FinalOutput: currentInput, Success: false, Error: err.Error(), Err: err}
		}
		t.logger.Info("sequential: running agent", "agent", name)
		output := t.runAgent(ctx, agent, currentInput)
		outputs[name] = output

		if !output.Success {
			cause := output.Err
			if cause == nil {
				cause = fmt.Errorf("%s", output.Error)
			}
			teamErr := fmt.Errorf("agent %s failed: %w", name, cause)
			return &TeamOutput{
				AgentOutputs: outputs,
				FinalOutput:  output.Content,
				Success:      false,
				Error:        teamErr.Error(),
				Err:          teamErr,
			}
		}
		currentInput = output.Content
		if err := t.emitCheckpoint(ctx, "agent_completed", index+1, currentInput, outputs, ""); err != nil {
			return &TeamOutput{AgentOutputs: outputs, FinalOutput: currentInput, Success: false, Error: err.Error(), Err: err}
		}
	}

	finalOutput := outputs[agents[len(agents)-1].Name()].Content
	return &TeamOutput{
		AgentOutputs: outputs,
		FinalOutput:  finalOutput,
		Success:      true,
	}
}

func (t *Team) runParallel(ctx context.Context, input string) *TeamOutput {
	outputs := make(map[string]*engine.RunOutput)
	mu := sync.Mutex{}
	wg := sync.WaitGroup{}

	for _, agent := range t.Agents {
		wg.Add(1)
		a := agent
		go func() {
			defer wg.Done()
			output := t.runAgent(ctx, a, input)
			mu.Lock()
			outputs[a.Name()] = output
			mu.Unlock()
		}()
	}
	wg.Wait()
	if err := t.emitCheckpoint(ctx, "parallel_completed", len(t.Agents), input, outputs, ""); err != nil {
		return &TeamOutput{AgentOutputs: outputs, Success: false, Error: err.Error(), Err: err}
	}

	var parts []string
	var failures []error
	for _, agent := range t.Agents {
		name := agent.Name()
		output := outputs[name]
		if output == nil {
			failures = append(failures, fmt.Errorf("agent %s returned no output", name))
			continue
		}
		if !output.Success {
			cause := output.Err
			if cause == nil {
				cause = fmt.Errorf("%s", output.Error)
			}
			failures = append(failures, fmt.Errorf("agent %s failed: %w", name, cause))
			continue
		}
		parts = append(parts, fmt.Sprintf("**%s**: %s", name, output.Content))
	}

	teamErr := errors.Join(failures...)
	errorMessage := ""
	if teamErr != nil {
		errorMessage = teamErr.Error()
	}
	return &TeamOutput{
		AgentOutputs: outputs,
		FinalOutput:  strings.Join(parts, "\n\n"),
		Success:      teamErr == nil,
		Error:        errorMessage,
		Err:          teamErr,
	}
}

func (t *Team) runLeaderFollower(ctx context.Context, input string) *TeamOutput {
	outputs := make(map[string]*engine.RunOutput)

	if t.Leader == nil {
		err := fmt.Errorf("leader is required for leader_follower mode")
		return &TeamOutput{Success: false, Error: err.Error(), Err: err}
	}

	leader := t.Leader
	leaderName := leader.Name()
	if err := t.emitCheckpoint(ctx, "leader_plan", 0, input, outputs, leaderName); err != nil {
		return &TeamOutput{AgentOutputs: outputs, Success: false, Error: err.Error(), Err: err}
	}
	planOutput := t.runAgent(ctx, leader, fmt.Sprintf("Plan the approach for: %s\n\nProvide a step-by-step plan.", input))
	outputs[leaderName] = planOutput

	if !planOutput.Success {
		cause := planOutput.Err
		if cause == nil {
			cause = fmt.Errorf("%s", planOutput.Error)
		}
		teamErr := fmt.Errorf("leader failed: %w", cause)
		return &TeamOutput{
			AgentOutputs: outputs,
			Success:      false,
			Error:        teamErr.Error(),
			Err:          teamErr,
		}
	}

	for index, agent := range t.Agents {
		name := agent.Name()
		if name == leaderName {
			continue
		}
		if err := t.emitCheckpoint(ctx, "follower", index, input, outputs, name); err != nil {
			return &TeamOutput{AgentOutputs: outputs, Success: false, Error: err.Error(), Err: err}
		}
		t.logger.Info("follower executing", "agent", name)
		followerInput := fmt.Sprintf("Plan: %s\n\nTask: %s\n\nYour role: %s", planOutput.Content, input, agent.SystemPrompt())
		output := t.runAgent(ctx, agent, followerInput)
		outputs[name] = output
		if !output.Success {
			cause := output.Err
			if cause == nil {
				cause = fmt.Errorf("%s", output.Error)
			}
			teamErr := fmt.Errorf("follower %s failed: %w", name, cause)
			return &TeamOutput{AgentOutputs: outputs, Success: false, Error: teamErr.Error(), Err: teamErr}
		}
	}

	if err := t.emitCheckpoint(ctx, "leader_synthesis", len(t.Agents), input, outputs, leaderName); err != nil {
		return &TeamOutput{AgentOutputs: outputs, Success: false, Error: err.Error(), Err: err}
	}
	synthOutput := t.runAgent(ctx, leader, fmt.Sprintf(
		"Synthesize the following results into a final answer.\n\nOriginal request: %s\n\nResults:\n%s",
		input, t.formatOutputs(outputs),
	))

	result := &TeamOutput{
		AgentOutputs: outputs,
		FinalOutput:  synthOutput.Content,
		Success:      synthOutput.Success,
	}
	if !synthOutput.Success {
		cause := synthOutput.Err
		if cause == nil {
			cause = fmt.Errorf("%s", synthOutput.Error)
		}
		result.Err = fmt.Errorf("leader synthesis failed: %w", cause)
		result.Error = result.Err.Error()
	}
	return result
}

func (t *Team) emitCheckpoint(ctx context.Context, phase string, index int, current string, outputs map[string]*engine.RunOutput, pending string) error {
	runtime, taskMode := task.RuntimeFrom(ctx)
	if !taskMode {
		return nil
	}
	completed := make(map[string]string, len(outputs))
	for name, output := range outputs {
		if output != nil && output.Success {
			completed[name] = output.Content
		}
	}
	checkpoint := task.Checkpoint{
		Kind: task.TurnTeam, Target: t.Name, Status: task.TurnRunning,
		Team: &task.TeamCheckpoint{TeamID: t.Name, Mode: string(t.Mode), Phase: phase, CurrentIndex: index, CurrentInput: current, AgentOutputs: completed, PendingAgentID: pending},
	}
	if err := runtime.Checkpoint(ctx, checkpoint); err != nil {
		return fmt.Errorf("team checkpoint: %w", err)
	}
	return nil
}

func (t *Team) formatOutputs(outputs map[string]*engine.RunOutput) string {
	var sb strings.Builder
	names := make([]string, 0, len(outputs))
	for name := range outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out := outputs[name]
		sb.WriteString(fmt.Sprintf("=== %s ===\n%s\n\n", name, out.Content))
	}
	return sb.String()
}
