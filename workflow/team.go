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

	for _, agent := range agents {
		name := agent.Name()
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

	for _, agent := range t.Agents {
		name := agent.Name()
		if name == leaderName {
			continue
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
