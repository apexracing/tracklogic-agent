package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/apexracing/tracklogic-agent/types"
)

// AnthropicProvider implements Model via the Anthropic Messages API (POST /v1/messages).
type AnthropicProvider struct {
	apiKey     string
	baseURL    string
	modelID    string
	vendor     string
	httpClient *http.Client
	headers    http.Header
	logger     *slog.Logger
	version    string
}

type AnthropicConfig struct {
	APIKey  string
	BaseURL string
	ModelID string
	Vendor  string
	Timeout time.Duration
	Logger  *slog.Logger
	// HTTPClient is cloned before use. If its Timeout is zero, Timeout above is
	// applied to the clone. Headers can override default request headers.
	HTTPClient *http.Client
	Headers    http.Header
}

func NewAnthropic(cfg AnthropicConfig) *AnthropicProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.anthropic.com/v1"
	}
	if cfg.ModelID == "" {
		cfg.ModelID = "claude-sonnet-4-20250514"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &AnthropicProvider{
		apiKey:     cfg.APIKey,
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		modelID:    cfg.ModelID,
		vendor:     strings.ToLower(strings.TrimSpace(cfg.Vendor)),
		httpClient: configuredHTTPClient(cfg.HTTPClient, cfg.Timeout),
		headers:    cfg.Headers.Clone(),
		logger:     logger.With("component", "model", "provider", "anthropic", "model_id", cfg.ModelID),
		version:    "2023-06-01",
	}
}

func (p *AnthropicProvider) Provider() string { return "anthropic" }
func (p *AnthropicProvider) ModelID() string  { return p.modelID }

// --- Messages API wire types ---

type anthropicRequest struct {
	Model        string                 `json:"model"`
	Messages     []anthropicMsg         `json:"messages"`
	System       string                 `json:"system,omitempty"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	MaxTokens    int                    `json:"max_tokens"`
	Temperature  float64                `json:"temperature,omitempty"`
	Stream       bool                   `json:"stream,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
}

type anthropicOutputConfig struct {
	Effort string `json:"effort"`
}

type anthropicThinking struct {
	Type string `json:"type"`
}

type anthropicMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []anthropicBlock
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicAPIResponse struct {
	ID         string           `json:"id"`
	Type       string           `json:"type"`
	Role       string           `json:"role"`
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      *anthropicUsage  `json:"usage,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index,omitempty"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
		Thinking    string `json:"thinking,omitempty"`
		Signature   string `json:"signature,omitempty"`
		StopReason  string `json:"stop_reason,omitempty"`
	} `json:"delta,omitempty"`
	ContentBlock *anthropicBlock `json:"content_block,omitempty"`
	Message      *struct {
		Usage *anthropicUsage `json:"usage,omitempty"`
	} `json:"message,omitempty"`
	Usage *anthropicUsage `json:"usage,omitempty"`
}

func (p *AnthropicProvider) Invoke(ctx context.Context, req *InvokeRequest) (*InvokeResponse, error) {
	if err := validateInvokeRequest(req); err != nil {
		return nil, err
	}
	apiReq, err := p.buildRequest(req, false)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	p.logger.Debug("invoking messages api", "messages", len(req.Messages), "tools", len(req.Tools))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, modelTransportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, modelHTTPError(p.Provider(), resp)
	}

	var apiResp anthropicAPIResponse
	if err := decodeModelJSON(resp.Body, &apiResp); err != nil {
		return nil, err
	}

	return parseAnthropicResponse(&apiResp), nil
}

func (p *AnthropicProvider) InvokeStream(ctx context.Context, req *InvokeRequest) (<-chan ResponseChunk, error) {
	if err := validateInvokeRequest(req); err != nil {
		return nil, err
	}
	apiReq, err := p.buildRequest(req, true)
	if err != nil {
		return nil, modelTransportError(err)
	}

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	p.setHeaders(httpReq)

	resp, finishStream, err := beginStreamingRequest(ctx, p.httpClient, httpReq)
	if err != nil {
		return nil, modelTransportError(err)
	}

	ch := make(chan ResponseChunk, 64)
	go func() {
		defer close(ch)
		defer finishStream()
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			emitResponseChunk(ctx, ch, ResponseChunk{Error: modelHTTPError(p.Provider(), resp)})
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		// Accumulate tool_use blocks across stream events.
		type toolAcc struct {
			id, name, args string
		}
		tools := map[int]*toolAcc{}
		reasoningBlocks := map[int]*anthropicBlock{}

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				emitResponseChunk(ctx, ch, ResponseChunk{Done: true})
				return
			}

			var evt anthropicStreamEvent
			if err := json.Unmarshal([]byte(data), &evt); err != nil {
				emitResponseChunk(ctx, ch, ResponseChunk{Error: modelStreamError("decode messages stream event", err)})
				return
			}

			switch evt.Type {
			case "content_block_start":
				if evt.ContentBlock != nil && evt.ContentBlock.Type == "thinking" {
					copy := *evt.ContentBlock
					reasoningBlocks[evt.Index] = &copy
				} else if evt.ContentBlock != nil && evt.ContentBlock.Type == "tool_use" {
					tools[evt.Index] = &toolAcc{
						id:   evt.ContentBlock.ID,
						name: evt.ContentBlock.Name,
					}
				}
			case "content_block_delta":
				if evt.Delta == nil {
					continue
				}
				switch evt.Delta.Type {
				case "text_delta":
					if evt.Delta.Text != "" {
						if !emitResponseChunk(ctx, ch, ResponseChunk{Content: evt.Delta.Text}) {
							return
						}
					}
				case "thinking_delta":
					if evt.Delta.Thinking != "" {
						if block := reasoningBlocks[evt.Index]; block != nil {
							block.Thinking += evt.Delta.Thinking
						}
						if !emitResponseChunk(ctx, ch, ResponseChunk{Reasoning: evt.Delta.Thinking}) {
							return
						}
					}
				case "signature_delta":
					if block := reasoningBlocks[evt.Index]; block != nil {
						block.Signature += evt.Delta.Signature
					}
				case "input_json_delta":
					if acc, ok := tools[evt.Index]; ok {
						acc.args += evt.Delta.PartialJSON
					}
				}
			case "content_block_stop":
				if block, ok := reasoningBlocks[evt.Index]; ok {
					if raw, err := json.Marshal(block); err == nil {
						if !emitResponseChunk(ctx, ch, ResponseChunk{ReasoningState: raw}) {
							return
						}
					}
					delete(reasoningBlocks, evt.Index)
				}
				if acc, ok := tools[evt.Index]; ok {
					if !emitResponseChunk(ctx, ch, ResponseChunk{
						ToolCall: &types.ToolCall{
							ID:   acc.id,
							Type: "function",
							Function: types.ToolCallFunction{
								Name:      acc.name,
								Arguments: acc.args,
							},
						},
					}) {
						return
					}
					delete(tools, evt.Index)
				}
			case "message_delta":
				finish := "stop"
				if evt.Delta != nil && evt.Delta.StopReason != "" {
					if evt.Delta.StopReason == "tool_use" {
						finish = "tool_calls"
					} else {
						finish = evt.Delta.StopReason
					}
				}
				rc := ResponseChunk{Done: true, FinishReason: finish}
				if evt.Usage != nil {
					rc.Usage = &types.Usage{
						PromptTokens:     evt.Usage.InputTokens,
						CompletionTokens: evt.Usage.OutputTokens,
						TotalTokens:      evt.Usage.InputTokens + evt.Usage.OutputTokens,
					}
				}
				emitResponseChunk(ctx, ch, rc)
				return
			case "error":
				emitResponseChunk(ctx, ch, ResponseChunk{Error: modelStreamError("messages stream returned an error", fmt.Errorf("%s", data)), Done: true})
				return
			}
		}

		if err := scanner.Err(); err != nil {
			emitResponseChunk(ctx, ch, ResponseChunk{Error: modelStreamError("read messages stream", err)})
			return
		}
		emitResponseChunk(ctx, ch, ResponseChunk{Error: modelStreamError("messages stream ended before completion", io.EOF)})
	}()

	return ch, nil
}

func (p *AnthropicProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", p.version)
	applyCustomHeaders(req, p.headers)
}

func (p *AnthropicProvider) buildRequest(req *InvokeRequest, stream bool) (*anthropicRequest, error) {
	system, messages := messagesToAnthropic(req.Messages)

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	apiReq := &anthropicRequest{
		Model:       p.modelID,
		Messages:    messages,
		System:      system,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		Stream:      stream,
	}
	if effort := normalizedReasoningEffort(req.ReasoningEffort); effort != "" {
		apiReq.OutputConfig = &anthropicOutputConfig{Effort: effort}
		if p.vendor == "anthropic" {
			apiReq.Thinking = &anthropicThinking{Type: "adaptive"}
		}
	}

	for _, td := range req.Tools {
		schema := td.Parameters.Schema()
		if schema["type"] == "" {
			schema["type"] = "object"
		}
		if len(td.Parameters.Required) > 0 {
			schema["required"] = td.Parameters.Required
		}
		apiReq.Tools = append(apiReq.Tools, anthropicTool{
			Name:        td.Name,
			Description: td.Description,
			InputSchema: schema,
		})
	}

	return apiReq, nil
}

func messagesToAnthropic(msgs []types.Message) (system string, out []anthropicMsg) {
	var sysParts []string
	var pendingToolResults []anthropicBlock

	flushToolResults := func() {
		if len(pendingToolResults) == 0 {
			return
		}
		out = append(out, anthropicMsg{Role: "user", Content: pendingToolResults})
		pendingToolResults = nil
	}

	for _, m := range msgs {
		switch m.Role {
		case types.RoleSystem:
			if m.Content != "" {
				sysParts = append(sysParts, m.Content)
			}
		case types.RoleUser:
			flushToolResults()
			out = append(out, anthropicMsg{Role: "user", Content: m.Content})
		case types.RoleAssistant:
			flushToolResults()
			blocks := make([]anthropicBlock, 0, len(m.ReasoningState)+1+len(m.ToolCalls))
			for _, rawState := range m.ReasoningState {
				var block anthropicBlock
				if json.Unmarshal(rawState, &block) == nil && block.Type == "thinking" {
					blocks = append(blocks, block)
				}
			}
			if m.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				args := json.RawMessage(tc.Function.Arguments)
				if !json.Valid(args) {
					args, _ = json.Marshal(tc.Function.Arguments)
				}
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: args,
				})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: ""})
			}
			out = append(out, anthropicMsg{Role: "assistant", Content: blocks})
		case types.RoleTool:
			pendingToolResults = append(pendingToolResults, anthropicBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			})
		}
	}
	flushToolResults()

	if len(sysParts) > 0 {
		system = strings.Join(sysParts, "\n\n")
	}
	return system, out
}

func parseAnthropicResponse(apiResp *anthropicAPIResponse) *InvokeResponse {
	result := &InvokeResponse{}
	var textParts []string

	for _, block := range apiResp.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "thinking":
			result.Reasoning += block.Thinking
			if raw, err := json.Marshal(block); err == nil {
				result.ReasoningState = append(result.ReasoningState, raw)
			}
		case "tool_use":
			args := string(block.Input)
			if args == "" {
				args = "{}"
			}
			result.ToolCalls = append(result.ToolCalls, types.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: types.ToolCallFunction{
					Name:      block.Name,
					Arguments: args,
				},
			})
		}
	}

	result.Content = strings.Join(textParts, "")
	switch apiResp.StopReason {
	case "tool_use":
		result.FinishReason = "tool_calls"
	case "":
		if len(result.ToolCalls) > 0 {
			result.FinishReason = "tool_calls"
		} else {
			result.FinishReason = "stop"
		}
	default:
		result.FinishReason = apiResp.StopReason
	}

	if apiResp.Usage != nil {
		result.Usage = &types.Usage{
			PromptTokens:     apiResp.Usage.InputTokens,
			CompletionTokens: apiResp.Usage.OutputTokens,
			TotalTokens:      apiResp.Usage.InputTokens + apiResp.Usage.OutputTokens,
		}
	}
	return result
}
