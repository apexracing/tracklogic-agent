package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordedRequest struct {
	Method          string
	ProtocolVersion string
	SessionID       string
	Authorization   string
}

func TestClientStreamableHTTPLifecycle(t *testing.T) {
	var mu sync.Mutex
	var requests []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/mcp" {
			t.Errorf("path = %q, want /mcp", request.URL.Path)
		}
		if request.Method == http.MethodDelete {
			if request.Header.Get("Mcp-Session-Id") != "session-1" {
				t.Errorf("DELETE session = %q", request.Header.Get("Mcp-Session-Id"))
			}
			writer.WriteHeader(http.StatusNoContent)
			return
		}

		var envelope struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, recordedRequest{
			Method: envelope.Method, ProtocolVersion: request.Header.Get("MCP-Protocol-Version"),
			SessionID: request.Header.Get("Mcp-Session-Id"), Authorization: request.Header.Get("Authorization"),
		})
		mu.Unlock()

		writer.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "initialize":
			writer.Header().Set("Mcp-Session-Id", "session-1")
			writeRPCResult(t, writer, envelope.ID, map[string]any{
				"protocolVersion": ProtocolVersion20251125,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
				"serverInfo":      map[string]any{"name": "catalog-server", "version": "1.0.0"},
			})
		case "notifications/initialized":
			writer.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeRPCResult(t, writer, envelope.ID, map[string]any{"tools": []any{map[string]any{
				"name": "query_items", "description": "query catalog items",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"fields": map[string]any{"type": "array", "items": map[string]any{"type": "string", "pattern": "^[a-z_]+$"}},
					},
					"required": []string{"fields"},
				},
			}}})
		case "tools/call":
			writeRPCResult(t, writer, envelope.ID, map[string]any{
				"content":           []any{map[string]any{"type": "text", "text": "ok"}},
				"structuredContent": map[string]any{"series": []float64{1, 2, 3}},
				"_meta":             map[string]any{"source": "live"},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second,
		WithHeader("Authorization", "Bearer test-token"),
	)
	ctx := context.Background()
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	definitions, err := client.ToToolDefinitions(ctx)
	if err != nil {
		t.Fatalf("ToToolDefinitions: %v", err)
	}
	if len(definitions) != 1 {
		t.Fatalf("definitions = %d, want 1", len(definitions))
	}
	schema := definitions[0].Parameters.Schema()
	properties := schema["properties"].(map[string]any)
	fields := properties["fields"].(map[string]any)
	items := fields["items"].(map[string]any)
	if items["pattern"] != "^[a-z_]+$" {
		t.Fatalf("nested schema not preserved: %#v", schema)
	}

	result, err := client.CallToolResult(ctx, "query_items", map[string]any{"fields": []string{"price"}})
	if err != nil {
		t.Fatalf("CallToolResult: %v", err)
	}
	structured := result.StructuredContent.(map[string]any)
	if structured["series"] == nil || result.Meta["source"] != "live" {
		t.Fatalf("structured result = %#v, meta = %#v", structured, result.Meta)
	}
	if err := client.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	mu.Lock()
	gotRequests := append([]recordedRequest(nil), requests...)
	mu.Unlock()
	if len(gotRequests) != 4 {
		t.Fatalf("requests = %+v", gotRequests)
	}
	if gotRequests[0].ProtocolVersion != "" || gotRequests[0].SessionID != "" {
		t.Fatalf("initialize unexpectedly used negotiated headers: %+v", gotRequests[0])
	}
	for _, request := range gotRequests[1:] {
		if request.ProtocolVersion != ProtocolVersion20251125 || request.SessionID != "session-1" || request.Authorization != "Bearer test-token" {
			t.Fatalf("subsequent request headers = %+v", request)
		}
	}

}

func TestClientAcceptsSSEResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var envelope struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(request.Body).Decode(&envelope)
		if envelope.Method == "notifications/initialized" {
			writer.WriteHeader(http.StatusAccepted)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(writer, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"protocolVersion\":\"%s\",\"capabilities\":{},\"serverInfo\":{\"name\":\"sse\",\"version\":\"1\"}}}\n\n", envelope.ID, ProtocolVersion20251125)
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize over SSE: %v", err)
	}
}

func TestClientRejectsMismatchedResponseIDAndHTTPError(t *testing.T) {
	t.Run("response id", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			var envelope struct {
				ID int `json:"id"`
			}
			_ = json.NewDecoder(request.Body).Decode(&envelope)
			writer.Header().Set("Content-Type", "application/json")
			writeRPCResult(t, writer, envelope.ID+1, map[string]any{})
		}))
		defer server.Close()
		err := NewClient(server.URL, time.Second).Initialize(context.Background())
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("HTTP status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "temporarily unavailable", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		err := NewClient(server.URL, time.Second).Initialize(context.Background())
		if err == nil || !strings.Contains(err.Error(), "503") {
			t.Fatalf("error = %v", err)
		}
	})
}

func writeRPCResult(t *testing.T, writer http.ResponseWriter, id int, result any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
