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

	"go-harness-tutorial/pkg/types"
)

type OpenAIProvider struct {
	apiKey     string
	baseURL    string
	modelID    string
	httpClient *http.Client
	logger     *slog.Logger
}

type OpenAIConfig struct {
	APIKey   string
	BaseURL  string
	ModelID  string
	Timeout  time.Duration
}

func NewOpenAI(cfg OpenAIConfig) *OpenAIProvider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.ModelID == "" {
		cfg.ModelID = "gpt-4o-mini"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &OpenAIProvider{
		apiKey:  cfg.APIKey,
		baseURL: cfg.BaseURL,
		modelID: cfg.ModelID,
		httpClient: &http.Client{Timeout: cfg.Timeout},
		logger:  slog.With("component", "model", "provider", "openai", "model_id", cfg.ModelID),
	}
}

func (p *OpenAIProvider) Provider() string {
	return "openai"
}

func (p *OpenAIProvider) ModelID() string {
	return p.modelID
}

type chatMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCallJSON   `json:"tool_calls,omitempty"`
	Name       string           `json:"name,omitempty"`
}

type toolCallJSON struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function toolFuncJSON   `json:"function"`
}

type toolFuncJSON struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatRequest struct {
	Model       string          `json:"model"`
	Messages    []chatMessage   `json:"messages"`
	Tools       []toolDefJSON   `json:"tools,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
}

type toolDefJSON struct {
	Type     string          `json:"type"`
	Function json.RawMessage `json:"function"`
}

type chatResponse struct {
	ID      string   `json:"id"`
	Choices []choice `json:"choices"`
	Usage   *usageJSON `json:"usage,omitempty"`
}

type choice struct {
	Index   int           `json:"index"`
	Message responseMsg   `json:"message"`
}

type responseMsg struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	ToolCalls []toolCallJSON `json:"tool_calls,omitempty"`
}

type usageJSON struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type streamChunk struct {
	Choices []streamChoice `json:"choices"`
}

type streamChoice struct {
	Delta struct {
		Content   string         `json:"content,omitempty"`
		ToolCalls []toolCallJSON `json:"tool_calls,omitempty"`
	} `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

func (p *OpenAIProvider) Invoke(ctx context.Context, req *InvokeRequest) (*InvokeResponse, error) {
	chatReq, err := p.buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	p.logger.Debug("invoking model", "messages", len(req.Messages), "tools", len(req.Tools))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, types.WrapError(types.ErrAPIError, "http request failed", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, types.WrapError(types.ErrAPIError,
			fmt.Sprintf("API returned status %d", resp.StatusCode),
			fmt.Errorf("%s", string(respBody)))
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return &InvokeResponse{}, nil
	}

	msg := chatResp.Choices[0].Message
	result := &InvokeResponse{
		Content: msg.Content,
	}

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

	if chatResp.Usage != nil {
		result.Usage = &types.Usage{
			PromptTokens:     chatResp.Usage.PromptTokens,
			CompletionTokens: chatResp.Usage.CompletionTokens,
			TotalTokens:      chatResp.Usage.TotalTokens,
		}
	}

	return result, nil
}

func (p *OpenAIProvider) InvokeStream(ctx context.Context, req *InvokeRequest) (<-chan ResponseChunk, error) {
	chatReq, err := p.buildRequest(req)
	if err != nil {
		return nil, err
	}
	chatReq.Stream = true

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

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	ch := make(chan ResponseChunk, 64)

	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				ch <- ResponseChunk{Done: true}
				return
			}

			var chunk streamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			if len(chunk.Choices) == 0 {
				continue
			}
			delta := chunk.Choices[0].Delta

			rc := ResponseChunk{
				Content: delta.Content,
			}

			if chunk.Choices[0].FinishReason != nil {
				rc.FinishReason = *chunk.Choices[0].FinishReason
				rc.Done = true
			}

			ch <- rc
		}

		if err := scanner.Err(); err != nil {
			ch <- ResponseChunk{Error: err}
		}
	}()

	return ch, nil
}

func (p *OpenAIProvider) buildRequest(req *InvokeRequest) (*chatRequest, error) {
	msgs := make([]chatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		cm := chatMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		if len(m.ToolCalls) > 0 {
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
		}
		msgs = append(msgs, cm)
	}

	cReq := &chatRequest{
		Model:       p.modelID,
		Messages:    msgs,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	for _, td := range req.Tools {
		fnJSON, err := json.Marshal(map[string]any{
			"name":        td.Name,
			"description": td.Description,
			"parameters":  td.Parameters,
		})
		if err != nil {
			continue
		}
		cReq.Tools = append(cReq.Tools, toolDefJSON{
			Type:     "function",
			Function: fnJSON,
		})
	}

	return cReq, nil
}
