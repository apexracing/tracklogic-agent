package workflow

import (
	"context"
	"fmt"
	"log/slog"
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
}

func NewTeam(cfg TeamConfig) *Team {
	return &Team{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Mode:        cfg.Mode,
		Agents:      cfg.Agents,
		Leader:      cfg.Leader,
		SharedModel: cfg.SharedModel,
		logger:      slog.With("component", "team", "name", cfg.Name),
	}
}

type TeamOutput struct {
	AgentOutputs map[string]*engine.RunOutput
	FinalOutput  string
	Success      bool
	Error        string
}

func (t *Team) Run(ctx context.Context, input string) *TeamOutput {
	t.logger.Info("team run started", "mode", t.Mode)

	switch t.Mode {
	case ModeSequential:
		return t.runSequential(ctx, input)
	case ModeParallel:
		return t.runParallel(ctx, input)
	case ModeLeaderFollower:
		return t.runLeaderFollower(ctx, input)
	default:
		return &TeamOutput{Success: false, Error: fmt.Sprintf("unknown mode: %s", t.Mode)}
	}
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
		return &TeamOutput{Success: false, Error: "no agents configured"}
	}

	outputs := make(map[string]*engine.RunOutput)
	currentInput := input

	for _, agent := range agents {
		name := agent.Name()
		t.logger.Info("sequential: running agent", "agent", name)
		output := t.runAgent(ctx, agent, currentInput)
		outputs[name] = output

		if !output.Success {
			return &TeamOutput{
				AgentOutputs: outputs,
				FinalOutput:  output.Content,
				Success:      false,
				Error:        fmt.Sprintf("agent %s failed: %s", name, output.Error),
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
	allSuccess := true
	for _, agent := range t.Agents {
		name := agent.Name()
		output := outputs[name]
		if output == nil || !output.Success {
			allSuccess = false
			continue
		}
		parts = append(parts, fmt.Sprintf("**%s**: %s", name, output.Content))
	}

	return &TeamOutput{
		AgentOutputs: outputs,
		FinalOutput:  strings.Join(parts, "\n\n"),
		Success:      allSuccess,
	}
}

func (t *Team) runLeaderFollower(ctx context.Context, input string) *TeamOutput {
	outputs := make(map[string]*engine.RunOutput)

	if t.Leader == nil {
		return &TeamOutput{Success: false, Error: "leader is required for leader_follower mode"}
	}

	leader := t.Leader
	leaderName := leader.Name()
	planOutput := t.runAgent(ctx, leader, fmt.Sprintf("Plan the approach for: %s\n\nProvide a step-by-step plan.", input))
	outputs[leaderName] = planOutput

	if !planOutput.Success {
		return &TeamOutput{
			AgentOutputs: outputs,
			Success:      false,
			Error:        fmt.Sprintf("leader failed: %s", planOutput.Error),
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
	}

	synthOutput := t.runAgent(ctx, leader, fmt.Sprintf(
		"Synthesize the following results into a final answer.\n\nOriginal request: %s\n\nResults:\n%s",
		input, t.formatOutputs(outputs),
	))

	return &TeamOutput{
		AgentOutputs: outputs,
		FinalOutput:  synthOutput.Content,
		Success:      synthOutput.Success,
	}
}

func (t *Team) formatOutputs(outputs map[string]*engine.RunOutput) string {
	var sb strings.Builder
	for name, out := range outputs {
		sb.WriteString(fmt.Sprintf("=== %s ===\n%s\n\n", name, out.Content))
	}
	return sb.String()
}
