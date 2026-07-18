package model

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// WithLogger decorates a Model with structured request/response logging.
// Credentials and request headers are never logged. Response content is kept
// at debug level because it may contain application or user data.
func WithLogger(inner Model, logger *slog.Logger) Model {
	if inner == nil || logger == nil {
		return inner
	}
	return &loggedModel{
		inner: inner,
		logger: logger.With(
			"component", "model_io",
			"provider", inner.Provider(),
			"model_id", inner.ModelID(),
		),
	}
}

type loggedModel struct {
	inner  Model
	logger *slog.Logger
}

func (m *loggedModel) Provider() string { return m.inner.Provider() }
func (m *loggedModel) ModelID() string  { return m.inner.ModelID() }

func (m *loggedModel) Invoke(ctx context.Context, req *InvokeRequest) (*InvokeResponse, error) {
	startedAt := time.Now()
	m.logger.Debug("model request started", requestLogFields(req)...)
	response, err := m.inner.Invoke(ctx, req)
	if err != nil {
		m.logger.Error("model request failed", "duration", time.Since(startedAt), "error", err)
		return nil, err
	}
	m.logResponse("model response received", response, time.Since(startedAt))
	return response, nil
}

func (m *loggedModel) InvokeStream(ctx context.Context, req *InvokeRequest) (<-chan ResponseChunk, error) {
	startedAt := time.Now()
	m.logger.Debug("model stream started", requestLogFields(req)...)
	input, err := m.inner.InvokeStream(ctx, req)
	if err != nil {
		m.logger.Error("model stream failed", "duration", time.Since(startedAt), "error", err)
		return nil, err
	}
	output := make(chan ResponseChunk)
	go func() {
		defer close(output)
		var content strings.Builder
		var reasoning strings.Builder
		var final *InvokeResponse
		for chunk := range input {
			content.WriteString(chunk.Content)
			reasoning.WriteString(chunk.Reasoning)
			if final == nil {
				final = &InvokeResponse{}
			}
			if chunk.ToolCall != nil {
				final.ToolCalls = append(final.ToolCalls, *chunk.ToolCall)
			}
			if len(chunk.ReasoningState) > 0 {
				final.ReasoningState = append(final.ReasoningState, append([]byte(nil), chunk.ReasoningState...))
			}
			if chunk.Usage != nil {
				final.Usage = chunk.Usage
			}
			if chunk.FinishReason != "" {
				final.FinishReason = chunk.FinishReason
			}
			if chunk.Error != nil {
				m.logger.Error("model stream failed", "duration", time.Since(startedAt), "error", chunk.Error)
			}
			select {
			case output <- chunk:
			case <-ctx.Done():
				m.logger.Warn("model stream cancelled", "duration", time.Since(startedAt), "error", ctx.Err())
				return
			}
		}
		if final == nil {
			final = &InvokeResponse{}
		}
		final.Content = content.String()
		final.Reasoning = reasoning.String()
		m.logResponse("model stream completed", final, time.Since(startedAt))
	}()
	return output, nil
}

func requestLogFields(req *InvokeRequest) []any {
	if req == nil {
		return []any{"messages", 0, "tools", 0, "stream", false}
	}
	return []any{"messages", len(req.Messages), "tools", len(req.Tools), "stream", req.Stream, "max_tokens", req.MaxTokens, "reasoning_effort", req.ReasoningEffort}
}

func (m *loggedModel) logResponse(message string, response *InvokeResponse, duration time.Duration) {
	if response == nil {
		m.logger.Warn(message, "duration", duration, "response", "nil")
		return
	}
	fields := []any{
		"duration", duration,
		"content", response.Content,
		"content_length", len(response.Content),
		"reasoning_length", len(response.Reasoning),
		"reasoning_state_items", len(response.ReasoningState),
		"tool_calls", len(response.ToolCalls),
		"finish_reason", response.FinishReason,
	}
	if response.Usage != nil {
		fields = append(fields,
			"input_tokens", response.Usage.PromptTokens,
			"output_tokens", response.Usage.CompletionTokens,
			"reasoning_tokens", response.Usage.ReasoningTokens,
			"total_tokens", response.Usage.TotalTokens,
		)
	}
	m.logger.Debug(message, fields...)
}
