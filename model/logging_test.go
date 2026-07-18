package model

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/apexracing/tracklogic-agent/types"
)

func TestWithLoggerRecordsInvokeResponse(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	runtimeModel := WithLogger(NewMock("test-model"), logger)

	response, err := runtimeModel.Invoke(context.Background(), &InvokeRequest{
		Messages:  []types.Message{{Role: types.RoleUser, Content: "hello"}},
		MaxTokens: 8,
	})
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if response.Content == "" {
		t.Fatal("Invoke() returned empty content")
	}
	logs := output.String()
	for _, expected := range []string{"model request started", "model response received", "provider=mock", "model_id=test-model", "content="} {
		if !strings.Contains(logs, expected) {
			t.Fatalf("logs do not contain %q: %s", expected, logs)
		}
	}
}

func TestWithLoggerRecordsStreamResponse(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	runtimeModel := WithLogger(NewMock("test-model"), logger)

	chunks, err := runtimeModel.InvokeStream(context.Background(), &InvokeRequest{
		Messages:  []types.Message{{Role: types.RoleUser, Content: "hello"}},
		MaxTokens: 8,
	})
	if err != nil {
		t.Fatalf("InvokeStream() error = %v", err)
	}
	for range chunks {
	}
	logs := output.String()
	for _, expected := range []string{"model stream started", "model stream completed", "content="} {
		if !strings.Contains(logs, expected) {
			t.Fatalf("logs do not contain %q: %s", expected, logs)
		}
	}
}
