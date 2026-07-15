package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/apexracing/tracklogic-agent/engine"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool/builtin"
	"github.com/apexracing/tracklogic-agent/types"
	"github.com/apexracing/tracklogic-agent/workflow"
)

type rejectingOutputValidator struct{}

func (rejectingOutputValidator) Validate(string) error { return fmt.Errorf("output rejected") }

func newRegistrationHarness(t *testing.T) *Harness {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DefaultModel.APIFormat = "mock"
	cfg.DefaultModel.ModelID = "registration"
	harness, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return harness
}

func TestCreateAgentRejectsInvalidAndDuplicateNamesWithoutOverwrite(t *testing.T) {
	harness := newRegistrationHarness(t)
	first, err := harness.CreateAgent("analyst", "first prompt")
	if err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	if _, err := harness.CreateAgent("analyst", "replacement prompt"); err == nil {
		t.Fatal("duplicate CreateAgent() error = nil")
	}
	stored, ok := harness.Agent("analyst")
	if !ok || stored != first || stored.SystemPrompt() != "first prompt" {
		t.Fatalf("stored agent was replaced: %#v", stored)
	}
	for _, name := range []string{"", " analyst "} {
		if _, err := harness.CreateAgent(name, ""); err == nil {
			t.Fatalf("CreateAgent(%q) error = nil", name)
		}
	}
}

func TestCreateAgentWithToolsValidatesCapabilityScope(t *testing.T) {
	harness := newRegistrationHarness(t)
	if err := harness.RegisterTool(builtin.NewCalculator()); err != nil {
		t.Fatalf("RegisterTool() error = %v", err)
	}
	if _, err := harness.CreateAgentWithTools("missing", "", "not_registered"); err == nil {
		t.Fatal("CreateAgentWithTools() accepted an unregistered tool")
	}
	if _, err := harness.CreateAgentWithTools("duplicate", "", "calculator", "calculator"); err == nil {
		t.Fatal("CreateAgentWithTools() accepted a duplicate tool")
	}
	runtimeAgent, err := harness.CreateAgentWithTools("scoped", "", "calculator")
	if err != nil || runtimeAgent == nil {
		t.Fatalf("CreateAgentWithTools() = %#v, %v", runtimeAgent, err)
	}
}

func TestCreateTeamAndWorkflowValidateBeforeRegistration(t *testing.T) {
	harness := newRegistrationHarness(t)
	member, err := harness.CreateAgent("member", "")
	if err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	if _, err := harness.CreateTeam(workflow.TeamConfig{
		Name: "invalid-team", Mode: workflow.ModeParallel, Agents: []*engine.Agent{nil},
	}); err == nil {
		t.Fatal("CreateTeam() accepted a nil member")
	}
	team, err := harness.CreateTeam(workflow.TeamConfig{
		Name: "valid-team", Mode: workflow.ModeSequential, Agents: []*engine.Agent{member},
	})
	if err != nil || team == nil {
		t.Fatalf("CreateTeam() = %#v, %v", team, err)
	}
	if _, err := harness.CreateTeam(workflow.TeamConfig{
		Name: "valid-team", Mode: workflow.ModeSequential, Agents: []*engine.Agent{member},
	}); err == nil {
		t.Fatal("duplicate CreateTeam() error = nil")
	}

	if _, err := harness.CreateWorkflow(workflow.WorkflowConfig{Name: " "}); err == nil {
		t.Fatal("CreateWorkflow() accepted an invalid name")
	}
	if _, err := harness.CreateWorkflow(workflow.WorkflowConfig{Name: "invalid-workflow", Nodes: []workflow.Node{nil}}); err == nil {
		t.Fatal("CreateWorkflow() accepted a nil node")
	}
	runtimeWorkflow, err := harness.CreateWorkflow(workflow.WorkflowConfig{
		Name:  "valid-workflow",
		Nodes: []workflow.Node{workflow.NewStepNode("member-step", member)},
	})
	if err != nil || runtimeWorkflow == nil {
		t.Fatalf("CreateWorkflow() = %#v, %v", runtimeWorkflow, err)
	}
	if _, err := harness.CreateWorkflow(workflow.WorkflowConfig{Name: "valid-workflow"}); err == nil {
		t.Fatal("duplicate CreateWorkflow() error = nil")
	}
}

func TestWithModelUsesInjectedDefault(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultModel.APIFormat = "mock"
	injected := model.NewMock("injected")
	harness, err := New(cfg, WithModel(injected))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if harness.Model() != injected {
		t.Fatal("Harness did not retain the injected model")
	}
	runtimeAgent, err := harness.CreateAgent("injected-agent", "")
	if err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	output := runtimeAgent.Run(context.Background(), "hello")
	if !output.Success || !strings.Contains(output.Content, "hello") {
		t.Fatalf("Run() = %+v", output)
	}
}

func TestWithModelRejectsTypedNil(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultModel.APIFormat = "mock"
	if _, err := New(cfg, WithModel((*model.MockModel)(nil))); err == nil {
		t.Fatal("New() accepted a typed-nil Model")
	}
}

func TestRunAgentPreservesRunEvidenceWhenOutputValidationFails(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultModel.APIFormat = "mock"
	harness, err := New(cfg, WithOutputValidator(rejectingOutputValidator{}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := harness.CreateAgent("validated-output", ""); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	output := harness.RunAgent(context.Background(), "validated-output", "hello")
	if output.Success || !strings.Contains(output.Error, "output rejected") {
		t.Fatalf("RunAgent() = %+v", output)
	}
	if output.RunID == "" || output.Content == "" || len(output.Messages) == 0 || output.LoopCount == 0 {
		t.Fatalf("output validation discarded run evidence: %+v", output)
	}
	var harnessErr *types.HarnessError
	if output.Err == nil || !errors.As(output.Err, &harnessErr) || harnessErr.Code != types.ErrInvalidInput {
		t.Fatalf("output.Err = %v, want INVALID_INPUT HarnessError", output.Err)
	}
}
