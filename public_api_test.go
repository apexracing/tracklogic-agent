package agent_test

import (
	"context"
	"strings"
	"testing"

	agent "github.com/apexracing/tracklogic-agent"
	"github.com/apexracing/tracklogic-agent/engine"
	"github.com/apexracing/tracklogic-agent/mcp"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/security"
	"github.com/apexracing/tracklogic-agent/tool"
	"github.com/apexracing/tracklogic-agent/types"
	"github.com/apexracing/tracklogic-agent/workflow"
)

type staticModel struct{}

func (staticModel) Provider() string { return "custom" }
func (staticModel) ModelID() string  { return "static-test" }

func (staticModel) Invoke(_ context.Context, req *model.InvokeRequest) (*model.InvokeResponse, error) {
	return &model.InvokeResponse{Content: "custom:" + latestUser(req.Messages)}, nil
}

func (m staticModel) InvokeStream(ctx context.Context, req *model.InvokeRequest) (<-chan model.ResponseChunk, error) {
	response, err := m.Invoke(ctx, req)
	if err != nil {
		return nil, err
	}
	chunks := make(chan model.ResponseChunk, 1)
	chunks <- model.ResponseChunk{Content: response.Content, FinishReason: "stop", Done: true}
	close(chunks)
	return chunks, nil
}

func latestUser(messages []types.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == types.RoleUser {
			return messages[i].Content
		}
	}
	return ""
}

type recordingMemory struct {
	messages []types.Message
}

func (m *recordingMemory) Add(message types.Message) { m.messages = append(m.messages, message) }
func (m *recordingMemory) Clear()                    { m.messages = nil }
func (m *recordingMemory) Len() int                  { return len(m.messages) }

func (m *recordingMemory) Get(index int) (types.Message, bool) {
	if index < 0 || index >= len(m.messages) {
		return types.Message{}, false
	}
	return m.messages[index], true
}

func (m *recordingMemory) Recent(count int) []types.Message {
	if count <= 0 {
		return nil
	}
	if count > len(m.messages) {
		count = len(m.messages)
	}
	return append([]types.Message(nil), m.messages[len(m.messages)-count:]...)
}

func (m *recordingMemory) Snapshot() []types.Message {
	return append([]types.Message(nil), m.messages...)
}

type upperTool struct {
	tool.BaseTool
}

func newUpperTool() *upperTool {
	return &upperTool{BaseTool: tool.NewBaseTool("upper", "uppercase text", []model.ToolParameter{{
		Name: "text", Type: "string", Description: "text to convert", Required: true,
	}})}
}

func (*upperTool) Execute(_ context.Context, args map[string]any) (any, error) {
	return strings.ToUpper(args["text"].(string)), nil
}

func TestPublicExtensionInterfaces(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(newUpperTool()); err != nil {
		t.Fatalf("register custom tool: %v", err)
	}

	memory := &recordingMemory{}
	runtimeAgent := engine.NewAgent(engine.AgentConfig{
		Name:         "custom-agent",
		SystemPrompt: "Reply deterministically.",
		Model:        staticModel{},
		ToolRegistry: registry,
		Memory:       memory,
	})

	var streamed strings.Builder
	result := runtimeAgent.Run(context.Background(), "hello",
		engine.WithMaxLoops(2),
		engine.WithStream(func(chunk string) { streamed.WriteString(chunk) }),
		engine.WithTemperature(0.2),
		engine.WithMaxTokens(128),
	)
	if !result.Success || result.Content != "custom:hello" {
		t.Fatalf("unexpected run result: %+v", result)
	}
	if streamed.String() != result.Content {
		t.Fatalf("streamed %q, want %q", streamed.String(), result.Content)
	}
	if memory.Len() < 2 {
		t.Fatalf("custom memory received %d messages, want at least 2", memory.Len())
	}
}

func TestRootHarnessTeamAndWorkflow(t *testing.T) {
	cfg := agent.DefaultConfig()
	cfg.DefaultModel.APIFormat = "mock"
	cfg.DefaultModel.ModelID = "public-api-test"

	harness, err := agent.New(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	first, err := harness.CreateAgent("first", "")
	if err != nil {
		t.Fatalf("create first agent: %v", err)
	}
	second, err := harness.CreateAgent("second", "")
	if err != nil {
		t.Fatalf("create second agent: %v", err)
	}

	team := workflow.NewTeam(workflow.TeamConfig{
		Name:   "test-team",
		Mode:   workflow.ModeSequential,
		Agents: []*engine.Agent{first, second},
	})
	teamResult := team.Run(context.Background(), "team input")
	if !teamResult.Success {
		t.Fatalf("team run failed: %s", teamResult.Error)
	}

	runtimeWorkflow := workflow.NewWorkflow(workflow.WorkflowConfig{Name: "test-workflow"})
	runtimeWorkflow.AddNode(workflow.NewStepNode("answer", first))
	workflowResult := runtimeWorkflow.Run(context.Background(), "workflow input")
	if !workflowResult.Success {
		t.Fatalf("workflow run failed: %s", workflowResult.Error)
	}

	registeredTeam := workflow.NewTeam(workflow.TeamConfig{
		Name:   "registered-team",
		Mode:   workflow.ModeSequential,
		Agents: []*engine.Agent{first},
	})
	if err := harness.RegisterTeam(registeredTeam); err != nil {
		t.Fatalf("register team: %v", err)
	}
	if result := harness.RunTeam(context.Background(), "registered-team", "input"); !result.Success {
		t.Fatalf("run registered team: %s", result.Error)
	}

	registeredWorkflow := workflow.NewWorkflow(workflow.WorkflowConfig{Name: "registered-workflow"})
	registeredWorkflow.AddNode(workflow.NewStepNode("answer", first))
	if err := harness.RegisterWorkflow(registeredWorkflow); err != nil {
		t.Fatalf("register workflow: %v", err)
	}
	if result := harness.RunWorkflow(context.Background(), "registered-workflow", "input"); !result.Success {
		t.Fatalf("run registered workflow: %s", result.Error)
	}
}

type allowAllPermissions struct{}

func (allowAllPermissions) Allow(...security.Permission)       {}
func (allowAllPermissions) Deny(...security.Permission)        {}
func (allowAllPermissions) Check(security.Permission) error    { return nil }
func (allowAllPermissions) IsAllowed(security.Permission) bool { return true }
func (allowAllPermissions) SetRole(security.Role)              {}

type passValidator struct{}

func (passValidator) Validate(string) error { return nil }

type prefixSanitizer struct{}

func (prefixSanitizer) Sanitize(input string) string { return "safe:" + input }

func TestHarnessAcceptsSecurityExtensionsAndPublicMCP(t *testing.T) {
	cfg := agent.DefaultConfig()
	cfg.DefaultModel.APIFormat = "mock"
	cfg.Security.SanitizePII = true

	harness, err := agent.New(cfg,
		agent.WithPermissionManager(allowAllPermissions{}),
		agent.WithInputValidator(passValidator{}),
		agent.WithOutputValidator(passValidator{}),
		agent.WithSanitizer(prefixSanitizer{}),
	)
	if err != nil {
		t.Fatalf("new harness with custom security: %v", err)
	}
	if got := harness.Sanitize("value"); got != "safe:value" {
		t.Fatalf("sanitize = %q", got)
	}

	if client := mcp.NewClient("http://127.0.0.1:1", 0); client == nil {
		t.Fatal("public MCP client is nil")
	}
}

func TestTeamSharedModelDoesNotMutateAgent(t *testing.T) {
	baseModel := model.NewMock("base")
	sharedModel := model.NewMock("shared")
	runtimeAgent := engine.NewAgent(engine.AgentConfig{Name: "member", Model: baseModel})
	team := workflow.NewTeam(workflow.TeamConfig{
		Name:        "shared-model",
		Mode:        workflow.ModeSequential,
		Agents:      []*engine.Agent{runtimeAgent},
		SharedModel: sharedModel,
	})
	if result := team.Run(context.Background(), "hello"); !result.Success {
		t.Fatalf("team run failed: %s", result.Error)
	}
	if runtimeAgent.Model() != baseModel {
		t.Fatal("team shared model mutated the agent default model")
	}
}

func TestRunContextUsesStandardContext(t *testing.T) {
	want := &types.RunContext{RunID: "run-1", WorkflowID: "workflow-1"}
	ctx := types.WithRunContext(context.Background(), want)
	got, ok := types.RunContextFrom(ctx)
	if !ok || got != want {
		t.Fatalf("run context = %#v, %v", got, ok)
	}
}

func TestPermissionModesHaveExpectedDefaults(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		wantAllowed bool
	}{
		{name: "strict requires an explicit grant", mode: "strict", wantAllowed: false},
		{name: "permissive grants built-in permissions", mode: "permissive", wantAllowed: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := agent.DefaultConfig()
			cfg.DefaultModel.APIFormat = "mock"
			cfg.PermissionMode = test.mode

			harness, err := agent.New(cfg)
			if err != nil {
				t.Fatalf("new harness: %v", err)
			}
			err = harness.CheckPermission(security.PermWriteFile)
			if gotAllowed := err == nil; gotAllowed != test.wantAllowed {
				t.Fatalf("write permission allowed = %v, want %v (error: %v)", gotAllowed, test.wantAllowed, err)
			}
		})
	}
}
