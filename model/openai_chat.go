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

// OpenAIChatProvider implements Model via OpenAI Chat Completions
// (POST /v1/chat/completions). Compatible with DeepSeek and other vendors
// that expose an OpenAI-compatible chat endpoint.
type OpenAIChatProvider struct {
	apiKey     string
	baseURL    string
	modelID    string
	vendor     string
	httpClient *http.Client
	headers    http.Header
	logger     *slog.Logger
}

func NewOpenAIChat(cfg OpenAIConfig) *OpenAIChatProvider {
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
	return &OpenAIChatProvider{
		apiKey:     cfg.APIKey,
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		modelID:    cfg.ModelID,
		vendor:     strings.ToLower(strings.TrimSpace(cfg.Vendor)),
		httpClient: configuredHTTPClient(cfg.HTTPClient, cfg.Timeout),
		headers:    cfg.Headers.Clone(),
		logger:     logger.With("component", "model", "provider", "openai_chat_completions", "model_id", cfg.ModelID),
	}
}

func (p *OpenAIChatProvider) Provider() string { return "openai_chat_completions" }
func (p *OpenAIChatProvider) ModelID() string  { return p.modelID }

type chatMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	ToolCalls        []toolCallJSON `json:"tool_calls,omitempty"`
	Name             string         `json:"name,omitempty"`
}

type toolCallJSON struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFuncJSON `json:"function"`
}

type toolFuncJSON struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	Tools           []toolDefJSON `json:"tools,omitempty"`
	Temperature     float64       `json:"temperature,omitempty"`
	MaxTokens       int           `json:"max_tokens,omitempty"`
	Stream          bool          `json:"stream,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
	Thinking        *chatThinking `json:"thinking,omitempty"`
}

type chatThinking struct {
	Type string `json:"type"`
}

type toolDefJSON struct {
	Type     string         `json:"type"`
	Function map[string]any `json:"function"`
}

type chatResponse struct {
	ID      string       `json:"id"`
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
}

type chatChoice struct {
	Index   int         `json:"index"`
	Message responseMsg `json:"message"`
}

type responseMsg struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCallJSON `json:"tool_calls,omitempty"`
}

type chatUsage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	TotalTokens             int `json:"total_tokens"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string         `json:"content,omitempty"`
			ReasoningContent string         `json:"reasoning_content,omitempty"`
			ToolCalls        []toolCallJSON `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

func (p *OpenAIChatProvider) Invoke(ctx context.Context, req *InvokeRequest) (*InvokeResponse, error) {
	if err := validateInvokeRequest(req); err != nil {
		return nil, err
	}
	chatReq, err := p.buildRequest(req, false)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	p.logger.Debug("invoking chat completions", "messages", len(req.Messages), "tools", len(req.Tools))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
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

	var chatResp chatResponse
	if err := decodeModelJSON(resp.Body, &chatResp); err != nil {
		return nil, err
	}

	if len(chatResp.Choices) == 0 {
		return &InvokeResponse{}, nil
	}

	msg := chatResp.Choices[0].Message
	result := &InvokeResponse{Content: msg.Content, Reasoning: msg.ReasoningContent}
	for _, tc := range msg.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, types.ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: types.ToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	if len(result.ToolCalls) > 0 {
		result.FinishReason = "tool_calls"
	} else {
		result.FinishReason = "stop"
	}

	if chatResp.Usage != nil {
		reasoningTokens := 0
		if chatResp.Usage.CompletionTokensDetails != nil {
			reasoningTokens = chatResp.Usage.CompletionTokensDetails.ReasoningTokens
		}
		result.Usage = &types.Usage{
			PromptTokens:     chatResp.Usage.PromptTokens,
			CompletionTokens: chatResp.Usage.CompletionTokens,
			ReasoningTokens:  reasoningTokens,
			TotalTokens:      chatResp.Usage.TotalTokens,
		}
	}
	return result, nil
}

func (p *OpenAIChatProvider) InvokeStream(ctx context.Context, req *InvokeRequest) (<-chan ResponseChunk, error) {
	if err := validateInvokeRequest(req); err != nil {
		return nil, err
	}
	chatReq, err := p.buildRequest(req, true)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
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

			var chunk chatStreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				emitResponseChunk(ctx, ch, ResponseChunk{Error: modelStreamError("decode chat stream event", err)})
				return
			}
			if len(chunk.Choices) == 0 {
				continue
			}

			delta := chunk.Choices[0].Delta
			rc := ResponseChunk{Content: delta.Content, Reasoning: delta.ReasoningContent}
			if len(delta.ToolCalls) > 0 {
				tc := delta.ToolCalls[0]
				rc.ToolCall = &types.ToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: types.ToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			}
			if chunk.Choices[0].FinishReason != nil {
				rc.FinishReason = *chunk.Choices[0].FinishReason
				rc.Done = true
			}
			if !emitResponseChunk(ctx, ch, rc) {
				return
			}
			if rc.Done {
				return
			}
		}

		if err := scanner.Err(); err != nil {
			emitResponseChunk(ctx, ch, ResponseChunk{Error: modelStreamError("read chat stream", err)})
			return
		}
		emitResponseChunk(ctx, ch, ResponseChunk{Error: modelStreamError("chat stream ended before completion", io.EOF)})
	}()

	return ch, nil
}

func (p *OpenAIChatProvider) buildRequest(req *InvokeRequest, stream bool) (*chatRequest, error) {
	msgs := make([]chatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		cm := chatMessage{
			Role:             string(m.Role),
			Content:          m.Content,
			ReasoningContent: m.Reasoning,
			ToolCallID:       m.ToolCallID,
			Name:             m.Name,
		}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, toolCallJSON{
				ID:   tc.ID,
				Type: tc.Type,
				Function: toolFuncJSON{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		msgs = append(msgs, cm)
	}

	cReq := &chatRequest{
		Model:       p.modelID,
		Messages:    msgs,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      stream,
	}
	if effort := normalizedReasoningEffort(req.ReasoningEffort); effort != "" {
		cReq.ReasoningEffort = effort
		if p.vendor == "deepseek" {
			cReq.Thinking = &chatThinking{Type: "enabled"}
		}
	}

	for _, td := range req.Tools {
		params := td.Parameters.Schema()
		if params["type"] == "" {
			params["type"] = "object"
		}
		if len(td.Parameters.Required) > 0 {
			params["required"] = td.Parameters.Required
		}
		cReq.Tools = append(cReq.Tools, toolDefJSON{
			Type: "function",
			Function: map[string]any{
				"name":        td.Name,
				"description": td.Description,
				"parameters":  params,
			},
		})
	}

	return cReq, nil
}
