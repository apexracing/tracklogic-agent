package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apexracing/tracklogic-agent/memory"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/types"
)

type blockingModel struct {
	entered chan struct{}
	release chan struct{}
}

type nilResultModel struct{ nilStream bool }

func (nilResultModel) Provider() string { return "nil-result" }
func (nilResultModel) ModelID() string  { return "nil-result" }
func (nilResultModel) Invoke(context.Context, *model.InvokeRequest) (*model.InvokeResponse, error) {
	return nil, nil
}
func (m nilResultModel) InvokeStream(context.Context, *model.InvokeRequest) (<-chan model.ResponseChunk, error) {
	if m.nilStream {
		return nil, nil
	}
	return make(chan model.ResponseChunk), nil
}

func (*blockingModel) Provider() string { return "test" }
func (*blockingModel) ModelID() string  { return "blocking" }

func (m *blockingModel) Invoke(ctx context.Context, _ *model.InvokeRequest) (*model.InvokeResponse, error) {
	select {
	case m.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-m.release:
		return &model.InvokeResponse{Content: "done"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *blockingModel) InvokeStream(ctx context.Context, req *model.InvokeRequest) (<-chan model.ResponseChunk, error) {
	response, err := m.Invoke(ctx, req)
	if err != nil {
		return nil, err
	}
	chunks := make(chan model.ResponseChunk, 1)
	chunks <- model.ResponseChunk{Content: response.Content, Done: true}
	close(chunks)
	return chunks, nil
}

func TestAgentSerializesSharedMemoryRunsAndWaitIsCancellable(t *testing.T) {
	mem := memory.NewBufferMemory(10)
	blocking := &blockingModel{entered: make(chan struct{}), release: make(chan struct{})}
	runtimeAgent := NewAgent(AgentConfig{Name: "stateful", Model: blocking, Memory: mem})

	firstDone := make(chan *RunOutput, 1)
	go func() { firstDone <- runtimeAgent.Run(context.Background(), "first") }()
	select {
	case <-blocking.entered:
	case <-time.After(time.Second):
		t.Fatal("first run did not enter the model")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	second := runtimeAgent.Run(waitCtx, "second")
	if second.Success || !strings.Contains(second.Error, string(types.ErrRunCancelled)) {
		t.Fatalf("second run = %+v, want cancellation while waiting", second)
	}
	if second.RunID == "" {
		t.Fatal("cancelled run did not receive a RunID")
	}
	var harnessErr *types.HarnessError
	if second.Err == nil || !errors.As(second.Err, &harnessErr) || harnessErr.Code != types.ErrRunCancelled {
		t.Fatalf("second Err = %v, want RUN_CANCELLED HarnessError", second.Err)
	}
	if got := mem.Len(); got != 1 {
		t.Fatalf("memory length while first run is blocked = %d, want 1; waiting run must not mutate memory", got)
	}

	close(blocking.release)
	select {
	case first := <-firstDone:
		if !first.Success {
			t.Fatalf("first run failed: %+v", first)
		}
	case <-time.After(time.Second):
		t.Fatal("first run did not finish")
	}
}

func TestAgentRejectsInvalidRunOptionsBeforeCallingModel(t *testing.T) {
	tests := []struct {
		name string
		opt  RunOption
		want string
	}{
		{name: "max loops", opt: WithMaxLoops(0), want: "max loops"},
		{name: "max tokens", opt: WithMaxTokens(0), want: "max tokens"},
		{name: "negative temperature", opt: WithTemperature(-0.1), want: "temperature"},
		{name: "high temperature", opt: WithTemperature(2.1), want: "temperature"},
		{name: "nil model", opt: WithModel(nil), want: "model"},
		{name: "typed nil model", opt: WithModel((*model.MockModel)(nil)), want: "model"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeAgent := NewAgent(AgentConfig{Name: "validated", Model: model.NewMock("validated")})
			output := runtimeAgent.Run(context.Background(), "input", test.opt)
			if output.Success || !strings.Contains(output.Error, test.want) {
				t.Fatalf("output = %+v, want error containing %q", output, test.want)
			}
			if output.RunID == "" {
				t.Fatal("invalid run did not receive a RunID")
			}
			var harnessErr *types.HarnessError
			if output.Err == nil || !errors.As(output.Err, &harnessErr) || harnessErr.Code != types.ErrInvalidConfig {
				t.Fatalf("output.Err = %v, want INVALID_CONFIG HarnessError", output.Err)
			}
		})
	}
}

func TestAgentRejectsNilModelResultsWithoutPanickingOrBlocking(t *testing.T) {
	tests := []struct {
		name   string
		model  model.Model
		option RunOption
		want   string
	}{
		{name: "response", model: nilResultModel{}, want: "nil response"},
		{name: "stream", model: nilResultModel{nilStream: true}, option: WithStream(func(string) {}), want: "nil stream"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeAgent := NewAgent(AgentConfig{Name: test.name, Model: test.model})
			var options []RunOption
			if test.option != nil {
				options = append(options, test.option)
			}
			output := runtimeAgent.Run(context.Background(), "input", options...)
			var harnessErr *types.HarnessError
			if output.Success || !strings.Contains(output.Error, test.want) ||
				!errors.As(output.Err, &harnessErr) || harnessErr.Code != types.ErrAPIError {
				t.Fatalf("Run() = %+v, want API_ERROR containing %q", output, test.want)
			}
		})
	}
}
