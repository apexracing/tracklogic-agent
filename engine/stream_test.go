package engine

import (
	"context"
	"testing"

	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/types"
)

func TestConsumeStream_ContentChunks(t *testing.T) {
	ch := make(chan model.ResponseChunk, 4)
	ch <- model.ResponseChunk{Content: "你"}
	ch <- model.ResponseChunk{Content: "好"}
	ch <- model.ResponseChunk{Content: "！", Done: true, FinishReason: "stop"}
	close(ch)

	var got []string
	resp, err := consumeStream(context.Background(), ch, func(s string) {
		got = append(got, s)
	})
	if err != nil {
		t.Fatalf("consumeStream: %v", err)
	}
	if resp.Content != "你好！" {
		t.Errorf("content = %q, want 你好！", resp.Content)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("finish = %q, want stop", resp.FinishReason)
	}
	if len(got) != 3 {
		t.Errorf("chunks = %v, want 3", got)
	}
}

func TestConsumeStream_MergeToolCalls(t *testing.T) {
	ch := make(chan model.ResponseChunk, 4)
	ch <- model.ResponseChunk{ToolCall: &types.ToolCall{
		ID: "tc1", Type: "function",
		Function: types.ToolCallFunction{Name: "lookup", Arguments: `{"q":`},
	}}
	ch <- model.ResponseChunk{ToolCall: &types.ToolCall{
		ID:       "tc1",
		Function: types.ToolCallFunction{Arguments: `"ord"}`},
	}}
	ch <- model.ResponseChunk{Done: true, FinishReason: "tool_calls"}
	close(ch)

	resp, err := consumeStream(context.Background(), ch, nil)
	if err != nil {
		t.Fatalf("consumeStream: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.Function.Name != "lookup" {
		t.Errorf("name = %q", tc.Function.Name)
	}
	wantArgs := `{"q":"ord"}`
	if tc.Function.Arguments != wantArgs {
		t.Errorf("args = %q, want %q", tc.Function.Arguments, wantArgs)
	}
}

func TestConsumeStream_Error(t *testing.T) {
	ch := make(chan model.ResponseChunk, 1)
	ch <- model.ResponseChunk{Error: context.DeadlineExceeded}
	close(ch)

	_, err := consumeStream(context.Background(), ch, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
