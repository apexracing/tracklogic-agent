package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/types"
)

type continuationSequenceModel struct {
	responses []*model.InvokeResponse
	requests  []*model.InvokeRequest
}

func (*continuationSequenceModel) Provider() string { return "test" }
func (*continuationSequenceModel) ModelID() string  { return "continuation-sequence" }

func (m *continuationSequenceModel) Invoke(_ context.Context, request *model.InvokeRequest) (*model.InvokeResponse, error) {
	copyRequest := *request
	copyRequest.Messages = append([]types.Message(nil), request.Messages...)
	m.requests = append(m.requests, &copyRequest)
	if len(m.responses) == 0 {
		return nil, errors.New("unexpected model invocation")
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, nil
}

func (m *continuationSequenceModel) InvokeStream(ctx context.Context, request *model.InvokeRequest) (<-chan model.ResponseChunk, error) {
	response, err := m.Invoke(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan model.ResponseChunk, 4)
	if response.Reasoning != "" {
		chunks <- model.ResponseChunk{Reasoning: response.Reasoning}
	}
	for _, state := range response.ReasoningState {
		chunks <- model.ResponseChunk{ReasoningState: state}
	}
	if response.Content != "" {
		chunks <- model.ResponseChunk{Content: response.Content}
	}
	chunks <- model.ResponseChunk{Done: true, FinishReason: response.FinishReason, Usage: response.Usage}
	close(chunks)
	return chunks, nil
}

func TestAgentContinuesReasoningOnlyTokenLimitedResponse(t *testing.T) {
	state := json.RawMessage(`{"type":"thinking","signature":"opaque"}`)
	runtimeModel := &continuationSequenceModel{responses: []*model.InvokeResponse{
		{
			Reasoning:      "still analyzing",
			ReasoningState: []json.RawMessage{state},
			FinishReason:   "max_tokens",
			Usage:          &types.Usage{TotalTokens: 4096},
		},
		{
			Content:      "最终结论",
			FinishReason: "stop",
			Usage:        &types.Usage{TotalTokens: 128},
		},
	}}
	runtimeAgent := NewAgent(AgentConfig{Name: "reasoning-continuation", Model: runtimeModel, MaxLoops: 3})

	output := runtimeAgent.Run(context.Background(), "analyze")

	if !output.Success || output.Content != "最终结论" {
		t.Fatalf("Run() = %+v, want a completed final answer", output)
	}
	if output.LoopCount != 2 || output.TotalTokens != 4224 {
		t.Fatalf("loops/tokens = %d/%d, want 2/4224", output.LoopCount, output.TotalTokens)
	}
	if len(runtimeModel.requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(runtimeModel.requests))
	}
	continuedMessages := runtimeModel.requests[1].Messages
	if len(continuedMessages) < 3 {
		t.Fatalf("continued messages = %+v, want original + assistant state + continuation", continuedMessages)
	}
	assistantState := continuedMessages[len(continuedMessages)-2]
	if assistantState.Role != types.RoleAssistant || assistantState.Reasoning != "still analyzing" || len(assistantState.ReasoningState) != 1 {
		t.Fatalf("preserved assistant state = %+v", assistantState)
	}
	continuation := continuedMessages[len(continuedMessages)-1]
	if continuation.Role != types.RoleUser || !strings.Contains(continuation.Content, "preserved reasoning state") {
		t.Fatalf("continuation message = %+v", continuation)
	}
}

func TestAgentCombinesVisibleContentAcrossStreamContinuation(t *testing.T) {
	runtimeModel := &continuationSequenceModel{responses: []*model.InvokeResponse{
		{Content: "结论前半", FinishReason: "length"},
		{Content: "，后半。", FinishReason: "stop"},
	}}
	runtimeAgent := NewAgent(AgentConfig{Name: "content-continuation", Model: runtimeModel, MaxLoops: 3})
	var streamed strings.Builder

	output := runtimeAgent.Run(context.Background(), "analyze", WithStream(func(chunk string) {
		streamed.WriteString(chunk)
	}))

	if !output.Success || output.Content != "结论前半，后半。" {
		t.Fatalf("Run() = %+v, want combined final content", output)
	}
	if streamed.String() != output.Content {
		t.Fatalf("streamed content = %q, output = %q", streamed.String(), output.Content)
	}
	continuedMessages := runtimeModel.requests[1].Messages
	if got := continuedMessages[len(continuedMessages)-2].Content; got != "结论前半" {
		t.Fatalf("preserved partial content = %q", got)
	}
}

func TestAgentRejectsEmptyCompletedResponse(t *testing.T) {
	runtimeModel := &continuationSequenceModel{responses: []*model.InvokeResponse{{FinishReason: "stop"}}}
	runtimeAgent := NewAgent(AgentConfig{Name: "empty-response", Model: runtimeModel, MaxLoops: 3})

	output := runtimeAgent.Run(context.Background(), "analyze")

	var harnessErr *types.HarnessError
	if output.Success || !errors.As(output.Err, &harnessErr) || harnessErr.Code != types.ErrAPIError {
		t.Fatalf("Run() = %+v, want API_ERROR", output)
	}
	if !strings.Contains(output.Error, "no visible content") {
		t.Fatalf("error = %q", output.Error)
	}
}

func TestAgentBoundsRepeatedTokenLimitedContinuations(t *testing.T) {
	runtimeModel := &continuationSequenceModel{responses: []*model.InvokeResponse{
		{Reasoning: "first", FinishReason: "max_tokens"},
		{Reasoning: "second", FinishReason: "max_tokens"},
	}}
	runtimeAgent := NewAgent(AgentConfig{Name: "bounded-continuation", Model: runtimeModel, MaxLoops: 2})

	output := runtimeAgent.Run(context.Background(), "analyze")

	var harnessErr *types.HarnessError
	if output.Success || !errors.As(output.Err, &harnessErr) || harnessErr.Code != types.ErrMaxLoopsExceeded {
		t.Fatalf("Run() = %+v, want MAX_LOOPS_EXCEEDED", output)
	}
	if len(runtimeModel.requests) != 2 {
		t.Fatalf("model requests = %d, want bounded at 2", len(runtimeModel.requests))
	}
}
