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

// OpenAIProvider implements Model via the OpenAI Responses API (POST /v1/responses).
type OpenAIProvider struct {
	apiKey     string
	baseURL    string
	modelID    string
	httpClient *http.Client
	headers    http.Header
	logger     *slog.Logger
}

type OpenAIConfig struct {
	APIKey  string
	BaseURL string
	ModelID string
	Timeout time.Duration
	Logger  *slog.Logger
	// HTTPClient is cloned before use. If its Timeout is zero, Timeout above is
	// applied to the clone. Headers can override default request headers.
	HTTPClient *http.Client
	Headers    http.Header
}

func NewOpenAI(cfg OpenAIConfig) *OpenAIProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.ModelID == "" {
		cfg.ModelID = "gpt-4o-mini"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &OpenAIProvider{
		apiKey:     cfg.APIKey,
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		modelID:    cfg.ModelID,
		httpClient: configuredHTTPClient(cfg.HTTPClient, cfg.Timeout),
		headers:    cfg.Headers.Clone(),
		logger:     logger.With("component", "model", "provider", "openai", "model_id", cfg.ModelID),
	}
}

func (p *OpenAIProvider) Provider() string { return "openai" }
func (p *OpenAIProvider) ModelID() string  { return p.modelID }

// --- Responses API wire types ---

type responsesRequest struct {
	Model           string          `json:"model"`
	Input           []any           `json:"input"`
	Instructions    string          `json:"instructions,omitempty"`
	Tools           []responsesTool `json:"tools,omitempty"`
	Temperature     float64         `json:"temperature,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
}

type responsesTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type responsesAPIResponse struct {
	ID     string              `json:"id"`
	Status string              `json:"status"`
	Output []responsesOutItem  `json:"output"`
	Usage  *responsesUsageJSON `json:"usage,omitempty"`
}

type responsesOutItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	Status    string          `json:"status,omitempty"`
}

type responsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type responsesUsageJSON struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type responsesStreamEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
	Item     json.RawMessage `json:"item,omitempty"`
}

func (p *OpenAIProvider) Invoke(ctx context.Context, req *InvokeRequest) (*InvokeResponse, error) {
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

	p.logger.Debug("invoking responses api", "messages", len(req.Messages), "tools", len(req.Tools))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	applyCustomHeaders(httpReq, p.headers)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, modelTransportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, modelHTTPError(p.Provider(), resp)
	}

	var apiResp responsesAPIResponse
	if err := decodeModelJSON(resp.Body, &apiResp); err != nil {
		return nil, err
	}

	return parseResponsesOutput(&apiResp), nil
}

func (p *OpenAIProvider) InvokeStream(ctx context.Context, req *InvokeRequest) (<-chan ResponseChunk, error) {
	if err := validateInvokeRequest(req); err != nil {
		return nil, err
	}
	apiReq, err := p.buildRequest(req, true)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	applyCustomHeaders(httpReq, p.headers)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, modelTransportError(err)
	}

	ch := make(chan ResponseChunk, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			emitResponseChunk(ctx, ch, ResponseChunk{Error: modelHTTPError(p.Provider(), resp)})
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

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

			var evt responsesStreamEvent
			if err := json.Unmarshal([]byte(data), &evt); err != nil {
				emitResponseChunk(ctx, ch, ResponseChunk{Error: modelStreamError("decode responses stream event", err)})
				return
			}

			switch evt.Type {
			case "response.output_text.delta":
				if evt.Delta != "" {
					if !emitResponseChunk(ctx, ch, ResponseChunk{Content: evt.Delta}) {
						return
					}
				}
			case "response.output_item.done":
				var item responsesOutItem
				if err := json.Unmarshal(evt.Item, &item); err != nil {
					emitResponseChunk(ctx, ch, ResponseChunk{Error: modelStreamError("decode responses tool event", err)})
					return
				}
				if item.Type == "function_call" {
					if !emitResponseChunk(ctx, ch, ResponseChunk{
						ToolCall: &types.ToolCall{
							ID:   item.CallID,
							Type: "function",
							Function: types.ToolCallFunction{
								Name:      item.Name,
								Arguments: item.Arguments,
							},
						},
					}) {
						return
					}
				}
			case "response.completed":
				var completed struct {
					Response responsesAPIResponse `json:"response"`
				}
				if err := json.Unmarshal([]byte(data), &completed); err == nil && completed.Response.Usage != nil {
					u := completed.Response.Usage
					emitResponseChunk(ctx, ch, ResponseChunk{
						Done:         true,
						FinishReason: "stop",
						Usage: &types.Usage{
							PromptTokens:     u.InputTokens,
							CompletionTokens: u.OutputTokens,
							TotalTokens:      u.TotalTokens,
						},
					})
				} else {
					emitResponseChunk(ctx, ch, ResponseChunk{Done: true, FinishReason: "stop"})
				}
				return
			case "error":
				emitResponseChunk(ctx, ch, ResponseChunk{Error: modelStreamError("responses stream returned an error", fmt.Errorf("%s", data)), Done: true})
				return
			}
		}

		if err := scanner.Err(); err != nil {
			emitResponseChunk(ctx, ch, ResponseChunk{Error: modelStreamError("read responses stream", err)})
			return
		}
		emitResponseChunk(ctx, ch, ResponseChunk{Error: modelStreamError("responses stream ended before completion", io.EOF)})
	}()

	return ch, nil
}

func (p *OpenAIProvider) buildRequest(req *InvokeRequest, stream bool) (*responsesRequest, error) {
	instructions, input := messagesToResponsesInput(req.Messages)

	apiReq := &responsesRequest{
		Model:           p.modelID,
		Input:           input,
		Instructions:    instructions,
		Temperature:     req.Temperature,
		MaxOutputTokens: req.MaxTokens,
		Stream:          stream,
	}

	for _, td := range req.Tools {
		params := td.Parameters.Schema()
		if len(td.Parameters.Required) > 0 {
			params["required"] = td.Parameters.Required
		}
		if params["type"] == "" {
			params["type"] = "object"
		}
		apiReq.Tools = append(apiReq.Tools, responsesTool{
			Type:        "function",
			Name:        td.Name,
			Description: td.Description,
			Parameters:  params,
		})
	}

	return apiReq, nil
}

func messagesToResponsesInput(msgs []types.Message) (instructions string, input []any) {
	var sysParts []string
	for _, m := range msgs {
		switch m.Role {
		case types.RoleSystem:
			if m.Content != "" {
				sysParts = append(sysParts, m.Content)
			}
		case types.RoleUser:
			input = append(input, map[string]any{
				"role":    "user",
				"content": m.Content,
			})
		case types.RoleAssistant:
			if m.Content != "" {
				input = append(input, map[string]any{
					"role":    "assistant",
					"content": m.Content,
				})
			}
			for _, tc := range m.ToolCalls {
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				})
			}
		case types.RoleTool:
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": m.ToolCallID,
				"output":  m.Content,
			})
		}
	}
	if len(sysParts) > 0 {
		instructions = strings.Join(sysParts, "\n\n")
	}
	return instructions, input
}

func parseResponsesOutput(apiResp *responsesAPIResponse) *InvokeResponse {
	result := &InvokeResponse{}
	var textParts []string

	for _, item := range apiResp.Output {
		switch item.Type {
		case "message":
			textParts = append(textParts, extractResponsesMessageText(item.Content)...)
		case "function_call":
			result.ToolCalls = append(result.ToolCalls, types.ToolCall{
				ID:   item.CallID,
				Type: "function",
				Function: types.ToolCallFunction{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			})
		}
	}

	result.Content = strings.Join(textParts, "")
	if len(result.ToolCalls) > 0 {
		result.FinishReason = "tool_calls"
	} else {
		result.FinishReason = "stop"
	}

	if apiResp.Usage != nil {
		result.Usage = &types.Usage{
			PromptTokens:     apiResp.Usage.InputTokens,
			CompletionTokens: apiResp.Usage.OutputTokens,
			TotalTokens:      apiResp.Usage.TotalTokens,
		}
	}
	return result
}

func extractResponsesMessageText(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	// content may be a string or an array of parts
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if asString != "" {
			return []string{asString}
		}
		return nil
	}
	var parts []responsesContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	var out []string
	for _, p := range parts {
		if (p.Type == "output_text" || p.Type == "text") && p.Text != "" {
			out = append(out, p.Text)
		}
	}
	return out
}
