package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/apexracing/tracklogic-agent/model"
)

const maxResponseBytes = 16 << 20

type ClientOption func(*Client)

// WithProtocolVersion selects the newest protocol revision this client asks
// the server to negotiate. The server may respond with another supported
// Streamable HTTP revision.
func WithProtocolVersion(version string) ClientOption {
	return func(client *Client) {
		if version != "" {
			client.requestedProtocol = version
		}
	}
}

func WithLogger(logger *slog.Logger) ClientOption {
	return func(client *Client) {
		if logger != nil {
			client.logger = logger.With("component", "mcp_client", "url", client.endpoint)
		}
	}
}

// WithHTTPClient supplies a custom client, typically to configure TLS,
// proxies, authentication transports, or test doubles.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(client *Client) {
		if httpClient != nil {
			client.httpClient = httpClient
		}
	}
}

// WithHeader adds a header to every MCP HTTP request. Secrets should be
// supplied programmatically rather than stored in JSON configuration.
func WithHeader(name, value string) ClientOption {
	return func(client *Client) {
		if name != "" {
			client.headers.Set(name, value)
		}
	}
}

type Client struct {
	mu                sync.Mutex
	initializeMu      sync.Mutex
	endpoint          string
	httpClient        *http.Client
	headers           http.Header
	logger            *slog.Logger
	reqID             int
	capabilities      ServerCapabilities
	requestedProtocol string
	protocolVersion   string
	sessionID         string
	initialized       bool
}

func NewClient(baseURL string, timeout time.Duration, options ...ClientOption) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	endpoint := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(endpoint, "/mcp") {
		endpoint += "/mcp"
	}
	client := &Client{
		endpoint:          endpoint,
		httpClient:        &http.Client{Timeout: timeout},
		headers:           make(http.Header),
		logger:            slog.Default().With("component", "mcp_client", "url", endpoint),
		requestedProtocol: CurrentProtocolVersion,
	}
	for _, option := range options {
		if option != nil {
			option(client)
		}
	}
	return client
}

func (c *Client) Initialize(ctx context.Context) error {
	c.initializeMu.Lock()
	defer c.initializeMu.Unlock()
	if c.isInitialized() {
		return nil
	}

	requestedProtocol := c.requestedVersion()
	params := InitializeParams{
		ProtocolVersion: requestedProtocol,
		Capabilities:    ClientCapabilities{},
		ClientInfo: Implementation{
			Name:    "tracklogic-agent",
			Version: "0.2.0",
		},
	}

	response, headers, err := c.callWithHeaders(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("initialize failed: %w", err)
	}

	var result InitializeResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return fmt.Errorf("parse initialize result: %w", err)
	}
	if !IsSupportedProtocolVersion(result.ProtocolVersion) {
		return fmt.Errorf("server negotiated unsupported protocol version %q (requested %q)", result.ProtocolVersion, requestedProtocol)
	}

	c.mu.Lock()
	c.protocolVersion = result.ProtocolVersion
	c.sessionID = headers.Get("Mcp-Session-Id")
	c.capabilities = result.Capabilities
	c.mu.Unlock()

	if err := c.notify(ctx, "notifications/initialized", nil); err != nil {
		c.clearNegotiatedState()
		return fmt.Errorf("send initialized notification: %w", err)
	}

	c.mu.Lock()
	c.initialized = true
	c.mu.Unlock()
	c.logger.Info("MCP server initialized", "server", result.ServerInfo.Name, "protocol_version", result.ProtocolVersion)
	return nil
}

func IsSupportedProtocolVersion(version string) bool {
	switch version {
	case ProtocolVersion20250326, ProtocolVersion20250618, ProtocolVersion20251125:
		return true
	default:
		return false
	}
}

func (c *Client) ListTools(ctx context.Context) ([]ToolDescription, error) {
	if !c.isInitialized() {
		return nil, fmt.Errorf("client not initialized")
	}

	response, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}

	var result ListToolsResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return nil, fmt.Errorf("parse tools/list result: %w", err)
	}
	return result.Tools, nil
}

// CallToolResult returns the full MCP result, including structuredContent and
// _meta. Use CallTool when only concatenated text content is needed.
func (c *Client) CallToolResult(ctx context.Context, name string, args map[string]any) (*CallToolResult, error) {
	if !c.isInitialized() {
		return nil, fmt.Errorf("client not initialized")
	}

	response, err := c.call(ctx, "tools/call", CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("call tool %q failed: %w", name, err)
	}

	var result CallToolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return nil, fmt.Errorf("parse tools/call result: %w", err)
	}
	if result.IsError {
		return nil, fmt.Errorf("tool %q returned error: %s", name, textContent(result.Content))
	}
	return &result, nil
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	result, err := c.CallToolResult(ctx, name, args)
	if err != nil {
		return "", err
	}
	return textContent(result.Content), nil
}

func textContent(content []ToolContent) string {
	texts := make([]string, 0, len(content))
	for _, item := range content {
		if item.Type == "text" {
			texts = append(texts, item.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func (c *Client) ToToolDefinitions(ctx context.Context) ([]model.ToolDefinition, error) {
	tools, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}

	definitions := make([]model.ToolDefinition, 0, len(tools))
	for _, description := range tools {
		schema, err := json.Marshal(description.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("marshal schema for tool %q: %w", description.Name, err)
		}
		var parameters model.ToolParameters
		if err := json.Unmarshal(schema, &parameters); err != nil {
			return nil, fmt.Errorf("parse schema for tool %q: %w", description.Name, err)
		}
		var rawSchema map[string]any
		if err := json.Unmarshal(schema, &rawSchema); err != nil {
			return nil, fmt.Errorf("preserve schema for tool %q: %w", description.Name, err)
		}
		parameters.RawSchema = rawSchema
		definitions = append(definitions, model.ToolDefinition{
			Name: description.Name, Description: description.Description, Parameters: parameters,
		})
	}
	return definitions, nil
}

func (c *Client) call(ctx context.Context, method string, params any) (*Response, error) {
	response, _, err := c.callWithHeaders(ctx, method, params)
	return response, err
}

func (c *Client) callWithHeaders(ctx context.Context, method string, params any) (*Response, http.Header, error) {
	c.mu.Lock()
	c.reqID++
	id := c.reqID
	c.mu.Unlock()

	request := Request{JSONRPC: Version2, ID: id, Method: method, Params: params}
	return c.send(ctx, request, &id)
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	notification := Notification{JSONRPC: Version2, Method: method, Params: params}
	_, _, err := c.send(ctx, notification, nil)
	return err
}

func (c *Client) send(ctx context.Context, payload any, expectedID *int) (response *Response, headers http.Header, err error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}
	c.applyHeaders(httpRequest)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json, text/event-stream")

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return nil, nil, fmt.Errorf("http request failed: %w", err)
	}
	defer httpResponse.Body.Close()
	headers = httpResponse.Header.Clone()

	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		message, readErr := readLimited(httpResponse.Body)
		if readErr != nil {
			return nil, headers, fmt.Errorf("MCP HTTP status %d (read error: %v)", httpResponse.StatusCode, readErr)
		}
		return nil, headers, fmt.Errorf("MCP HTTP status %d: %s", httpResponse.StatusCode, strings.TrimSpace(string(message)))
	}
	if expectedID == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(httpResponse.Body, maxResponseBytes))
		return nil, headers, nil
	}

	mediaType, _, _ := mime.ParseMediaType(httpResponse.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		response, err = decodeSSEResponse(httpResponse.Body, *expectedID)
	} else {
		response, err = decodeJSONResponse(httpResponse.Body)
	}
	if err != nil {
		return nil, headers, err
	}
	if response.JSONRPC != Version2 {
		return nil, headers, fmt.Errorf("unexpected JSON-RPC version %q", response.JSONRPC)
	}
	if response.ID != *expectedID {
		return nil, headers, fmt.Errorf("JSON-RPC response id %d does not match request id %d", response.ID, *expectedID)
	}
	if response.Error != nil {
		return nil, headers, fmt.Errorf("RPC error [%d]: %s", response.Error.Code, response.Error.Message)
	}
	return response, headers, nil
}

func decodeJSONResponse(reader io.Reader) (*Response, error) {
	body, err := readLimited(reader)
	if err != nil {
		return nil, err
	}
	var response Response
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("parse JSON-RPC response: %w", err)
	}
	return &response, nil
}

func decodeSSEResponse(reader io.Reader, expectedID int) (*Response, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxResponseBytes)
	var data strings.Builder
	consume := func() (*Response, error) {
		if data.Len() == 0 {
			return nil, nil
		}
		payload := data.String()
		data.Reset()
		var response Response
		if err := json.Unmarshal([]byte(payload), &response); err != nil {
			return nil, fmt.Errorf("parse SSE JSON-RPC event: %w", err)
		}
		if response.ID == expectedID {
			return &response, nil
		}
		return nil, nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			response, err := consume()
			if err != nil || response != nil {
				return response, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SSE response: %w", err)
	}
	if response, err := consume(); err != nil || response != nil {
		return response, err
	}
	return nil, fmt.Errorf("SSE stream ended without response id %d", expectedID)
}

func readLimited(reader io.Reader) ([]byte, error) {
	limited := io.LimitReader(reader, maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return body, nil
}

func (c *Client) applyHeaders(request *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, values := range c.headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	if c.protocolVersion != "" {
		request.Header.Set("MCP-Protocol-Version", c.protocolVersion)
	}
	if c.sessionID != "" {
		request.Header.Set("Mcp-Session-Id", c.sessionID)
	}
}

func (c *Client) requestState() (protocolVersion, sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.protocolVersion, c.sessionID
}

func (c *Client) requestedVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requestedProtocol
}

func (c *Client) isInitialized() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initialized
}

func (c *Client) clearNegotiatedState() {
	c.mu.Lock()
	c.capabilities = ServerCapabilities{}
	c.protocolVersion = ""
	c.sessionID = ""
	c.initialized = false
	c.mu.Unlock()
}

// Close terminates a stateful Streamable HTTP session when the server issued
// one, then closes idle HTTP connections.
func (c *Client) Close(ctx context.Context) error {
	protocolVersion, sessionID := c.requestState()
	c.clearNegotiatedState()
	defer c.httpClient.CloseIdleConnections()
	if sessionID == "" {
		return nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.endpoint, nil)
	if err != nil {
		return err
	}
	for name, values := range c.headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	request.Header.Set("MCP-Protocol-Version", protocolVersion)
	request.Header.Set("Mcp-Session-Id", sessionID)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusMethodNotAllowed {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("terminate MCP session: HTTP status %d", response.StatusCode)
	}
	return nil
}

type ServerStream struct {
	reader *bufio.Reader
	closer io.Closer
}

func (stream *ServerStream) ReadLine() (string, error) {
	if stream == nil || stream.reader == nil {
		return "", fmt.Errorf("server stream is not initialized")
	}
	line, err := stream.reader.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

func (stream *ServerStream) Close() error {
	if stream == nil || stream.closer == nil {
		return nil
	}
	return stream.closer.Close()
}

// SSEStream opens the optional server-to-client stream on the Streamable HTTP
// MCP endpoint. A 405 response means the server does not offer this stream.
func (c *Client) SSEStream(ctx context.Context) (*ServerStream, error) {
	if !c.isInitialized() {
		return nil, fmt.Errorf("client not initialized")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(request)
	request.Header.Set("Accept", "text/event-stream")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("open MCP SSE stream: HTTP status %d", response.StatusCode)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != "text/event-stream" {
		response.Body.Close()
		return nil, fmt.Errorf("open MCP SSE stream: unexpected content type %q", mediaType)
	}
	return &ServerStream{reader: bufio.NewReader(response.Body), closer: response.Body}, nil
}
