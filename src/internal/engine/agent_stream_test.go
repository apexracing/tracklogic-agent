package engine

import (
	"context"
	"strings"
	"testing"

	"go-harness-tutorial/internal/memory"
	"go-harness-tutorial/internal/model"
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
