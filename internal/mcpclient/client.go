package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/apexracing/tracklogic-agent/model"
)

type Client struct {
	mu           sync.Mutex
	baseURL      string
	httpClient   *http.Client
	logger       *slog.Logger
	reqID        int
	capabilities ServerCapabilities
	initialized  bool
}

func NewClient(baseURL string, timeout time.Duration) *Client {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
		logger:     slog.With("component", "mcp_client", "url", baseURL),
	}
}

func (c *Client) Initialize(ctx context.Context) error {
	c.mu.Lock()
	if c.initialized {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	params := InitializeParams{
		ProtocolVersion: "2025-03-26",
		Capabilities:    ClientCapabilities{},
		ClientInfo: Implementation{
			Name:    "tracklogic-agent",
			Version: "1.0.0",
		},
	}

	resp, err := c.call(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("initialize failed: %w", err)
	}

	var result InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("parse initialize result: %w", err)
	}

	c.mu.Lock()
	c.capabilities = result.Capabilities
	c.initialized = true
	c.mu.Unlock()

	c.logger.Info("MCP server initialized", "server", result.ServerInfo.Name)
	return nil
}

func (c *Client) ListTools(ctx context.Context) ([]ToolDescription, error) {
	if !c.initialized {
		return nil, fmt.Errorf("client not initialized")
	}

	resp, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}

	var result ListToolsResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse tools/list result: %w", err)
	}

	return result.Tools, nil
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if !c.initialized {
		return "", fmt.Errorf("client not initialized")
	}

	params := CallToolParams{Name: name, Arguments: args}

	resp, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return "", fmt.Errorf("call tool %q failed: %w", name, err)
	}

	var result CallToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("parse tools/call result: %w", err)
	}

	if result.IsError {
		return "", fmt.Errorf("tool %q returned error", name)
	}

	var texts []string
	for _, content := range result.Content {
		if content.Type == "text" {
			texts = append(texts, content.Text)
		}
	}

	return strings.Join(texts, "\n"), nil
}

func (c *Client) ToToolDefinitions(ctx context.Context) ([]model.ToolDefinition, error) {
	tools, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}

	defs := make([]model.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		schema, _ := json.Marshal(t.InputSchema)
		var params model.ToolParameters
		json.Unmarshal(schema, &params)

		defs = append(defs, model.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		})
	}
	return defs, nil
}

func (c *Client) call(ctx context.Context, method string, params any) (*Response, error) {
	c.mu.Lock()
	c.reqID++
	id := c.reqID
	c.mu.Unlock()

	req := Request{
		JSONRPC: Version2,
		ID:      id,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/mcp", jsonContentReader(body))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var response Response
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if response.Error != nil {
		return nil, fmt.Errorf("RPC error [%d]: %s", response.Error.Code, response.Error.Message)
	}

	return &response, nil
}

func jsonContentReader(data []byte) io.Reader {
	return &jsonReader{data: data}
}

type jsonReader struct {
	data []byte
	pos  int
}

func (r *jsonReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

type ServerStream struct {
	reader *bufio.Reader
	closer io.Closer
}

func (c *Client) SSEStream(ctx context.Context) (*ServerStream, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/sse", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	return &ServerStream{
		reader: bufio.NewReader(resp.Body),
		closer: resp.Body,
	}, nil
}
