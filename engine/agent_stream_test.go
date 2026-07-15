package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apexracing/tracklogic-agent/memory"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/types"
)

func TestAgent_RunWithStream(t *testing.T) {
	m := model.NewMock("mock-stream")
	agent := NewAgent(AgentConfig{
		Name:         "stream-agent",
		SystemPrompt: "你是测试助手",
		Model:        m,
		Memory:       memory.NewBufferMemory(20),
		MaxLoops:     3,
	})

	var chunks []string
	out := agent.Run(context.Background(), "你好", WithStream(func(c string) {
		chunks = append(chunks, c)
	}))
	if !out.Success {
		t.Fatalf("run failed: %s", out.Error)
	}
	joined := strings.Join(chunks, "")
	if joined == "" {
		t.Fatal("expected streamed chunks")
	}
	if out.Content != joined {
		t.Errorf("content %q != streamed %q", out.Content, joined)
	}
}

type deadlineStreamModel struct{}

func (deadlineStreamModel) Provider() string { return "deadline" }
func (deadlineStreamModel) ModelID() string  { return "deadline" }
func (deadlineStreamModel) Invoke(context.Context, *model.InvokeRequest) (*model.InvokeResponse, error) {
	return nil, context.DeadlineExceeded
}
func (deadlineStreamModel) InvokeStream(context.Context, *model.InvokeRequest) (<-chan model.ResponseChunk, error) {
	chunks := make(chan model.ResponseChunk, 1)
	chunks <- model.ResponseChunk{Error: context.DeadlineExceeded}
	close(chunks)
	return chunks, nil
}

func TestAgentStreamClassifiesContextErrors(t *testing.T) {
	runtimeAgent := NewAgent(AgentConfig{Name: "deadline", Model: deadlineStreamModel{}})
	output := runtimeAgent.Run(context.Background(), "input", WithStream(func(string) {}))
	var harnessErr *types.HarnessError
	if output.Success || !errors.As(output.Err, &harnessErr) || harnessErr.Code != types.ErrModelTimeout {
		t.Fatalf("Run() = %+v, want MODEL_TIMEOUT", output)
	}
}
