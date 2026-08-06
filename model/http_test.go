package model

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apexracing/tracklogic-agent/types"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func invokeRequest() *InvokeRequest {
	return &InvokeRequest{
		Messages:  []types.Message{{Role: types.RoleUser, Content: "hello"}},
		MaxTokens: 16,
	}
}

func TestProvidersExposeStructuredRateLimitErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("x-request-id", "request-123")
		writer.Header().Set("Retry-After", "2")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":"slow down"}`))
	}))
	defer server.Close()

	providers := []Model{
		NewOpenAI(OpenAIConfig{BaseURL: server.URL, ModelID: "test"}),
		NewOpenAIChat(OpenAIConfig{BaseURL: server.URL, ModelID: "test"}),
		NewAnthropic(AnthropicConfig{BaseURL: server.URL, ModelID: "test"}),
	}
	for _, provider := range providers {
		t.Run(provider.Provider(), func(t *testing.T) {
			_, err := provider.Invoke(context.Background(), invokeRequest())
			var harnessError *types.HarnessError
			if !errors.As(err, &harnessError) || harnessError.Code != types.ErrRateLimit {
				t.Fatalf("Invoke() error = %v, want RATE_LIMIT HarnessError", err)
			}
			var upstream *HTTPError
			if !errors.As(err, &upstream) {
				t.Fatalf("Invoke() error = %v, want HTTPError", err)
			}
			if upstream.StatusCode != http.StatusTooManyRequests || upstream.RequestID != "request-123" || upstream.RetryAfter != "2" || !upstream.Retryable() {
				t.Fatalf("HTTPError = %+v", upstream)
			}
		})
	}
}

func TestProviderRejectsOversizedJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(strings.Repeat("x", maxModelResponseBytes+1)))
	}))
	defer server.Close()

	provider := NewOpenAI(OpenAIConfig{BaseURL: server.URL, ModelID: "test"})
	_, err := provider.Invoke(context.Background(), invokeRequest())
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Invoke() error = %v, want response size error", err)
	}
}

func TestProviderClonesHTTPClientAndAppliesCustomHeaders(t *testing.T) {
	var gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotHeader = request.Header.Get("X-Tenant")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"response","status":"completed","output":[]}`))
	}))
	defer server.Close()

	original := &http.Client{}
	provider := NewOpenAI(OpenAIConfig{
		BaseURL: server.URL, ModelID: "test", Timeout: 125 * time.Millisecond,
		HTTPClient: original, Headers: http.Header{"X-Tenant": []string{"tenant-a"}},
	})
	if provider.httpClient == original {
		t.Fatal("provider retained caller-owned HTTP client instead of cloning it")
	}
	if provider.httpClient.Timeout != 125*time.Millisecond || original.Timeout != 0 {
		t.Fatalf("timeouts: provider=%s original=%s", provider.httpClient.Timeout, original.Timeout)
	}
	if _, err := provider.Invoke(context.Background(), invokeRequest()); err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if gotHeader != "tenant-a" {
		t.Fatalf("X-Tenant = %q", gotHeader)
	}
}

func TestStreamingTimeoutOnlyCoversResponseHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		time.Sleep(80 * time.Millisecond)
		_, _ = writer.Write([]byte("stream completed"))
	}))
	defer server.Close()

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := configuredHTTPClient(nil, 20*time.Millisecond)
	started := time.Now()
	response, finish, err := beginStreamingRequest(context.Background(), client, request)
	if err != nil {
		t.Fatalf("beginStreamingRequest() error = %v", err)
	}
	defer finish()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read active stream after client timeout: %v", err)
	}
	if string(body) != "stream completed" {
		t.Fatalf("stream body = %q", body)
	}
	if elapsed := time.Since(started); elapsed < 60*time.Millisecond {
		t.Fatalf("stream completed in %s; test did not outlive the configured timeout", elapsed)
	}
	if client.Timeout != 20*time.Millisecond {
		t.Fatalf("caller client timeout mutated to %s", client.Timeout)
	}
}

func TestStreamingTimeoutStillBoundsResponseHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(80 * time.Millisecond)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := configuredHTTPClient(nil, 20*time.Millisecond)
	response, finish, err := beginStreamingRequest(context.Background(), client, request)
	if finish != nil {
		finish()
	}
	if response != nil {
		response.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("beginStreamingRequest() error = %v, want deadline exceeded", err)
	}
}

func TestProviderClassifiesTransportDeadline(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	provider := NewOpenAI(OpenAIConfig{BaseURL: "https://example.com", ModelID: "test", HTTPClient: client})
	_, err := provider.Invoke(context.Background(), invokeRequest())
	var harnessError *types.HarnessError
	if !errors.As(err, &harnessError) || harnessError.Code != types.ErrModelTimeout {
		t.Fatalf("Invoke() error = %v, want MODEL_TIMEOUT", err)
	}
}

func TestModelsRejectNilInvokeRequest(t *testing.T) {
	models := []Model{
		NewOpenAI(OpenAIConfig{ModelID: "test"}),
		NewOpenAIChat(OpenAIConfig{ModelID: "test"}),
		NewAnthropic(AnthropicConfig{ModelID: "test"}),
		NewMock("test"),
	}
	for _, runtimeModel := range models {
		if _, err := runtimeModel.Invoke(context.Background(), nil); err == nil {
			t.Fatalf("%s Invoke(nil) error = nil", runtimeModel.Provider())
		}
		if _, err := runtimeModel.InvokeStream(context.Background(), nil); err == nil {
			t.Fatalf("%s InvokeStream(nil) error = nil", runtimeModel.Provider())
		}
	}
}

func TestProvidersRejectMalformedStreamEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: not-json\n\n"))
	}))
	defer server.Close()

	providers := []Model{
		NewOpenAI(OpenAIConfig{BaseURL: server.URL, ModelID: "test"}),
		NewOpenAIChat(OpenAIConfig{BaseURL: server.URL, ModelID: "test"}),
		NewAnthropic(AnthropicConfig{BaseURL: server.URL, ModelID: "test"}),
	}
	for _, provider := range providers {
		t.Run(provider.Provider(), func(t *testing.T) {
			chunks, err := provider.InvokeStream(context.Background(), invokeRequest())
			if err != nil {
				t.Fatalf("InvokeStream() error = %v", err)
			}
			select {
			case chunk := <-chunks:
				var harnessError *types.HarnessError
				if !errors.As(chunk.Error, &harnessError) || harnessError.Code != types.ErrAPIError {
					t.Fatalf("stream chunk = %+v, want API_ERROR", chunk)
				}
			case <-time.After(time.Second):
				t.Fatal("stream did not report malformed event")
			}
		})
	}
}

func TestEmitResponseChunkStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if emitResponseChunk(ctx, make(chan ResponseChunk), ResponseChunk{Content: "blocked"}) {
		t.Fatal("emitResponseChunk() succeeded with a cancelled context and no receiver")
	}
}
