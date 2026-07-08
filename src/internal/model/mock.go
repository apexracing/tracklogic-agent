package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go-harness-tutorial/pkg/types"
)

// MockModel provides deterministic responses for local demos without an API key.
type MockModel struct {
	modelID string
}

func NewMock(modelID string) *MockModel {
	if modelID == "" {
		modelID = "mock-demo"
	}
	return &MockModel{modelID: modelID}
}

func (m *MockModel) Provider() string  { return "mock" }
func (m *MockModel) ModelID() string   { return m.modelID }

func (m *MockModel) Invoke(ctx context.Context, req *InvokeRequest) (*InvokeResponse, error) {
	select {
	case <-ctx.Done():
		return nil, types.WrapError(types.ErrRunCancelled, "context cancelled", ctx.Err())
	default:
	}

	lastUser := lastUserMessage(req.Messages)
	if tc := m.pendingToolCall(req); tc != nil {
		return &InvokeResponse{ToolCalls: []types.ToolCall{*tc}}, nil
	}

	return &InvokeResponse{Content: m.finalReply(req, lastUser)}, nil
}

func (m *MockModel) InvokeStream(ctx context.Context, req *InvokeRequest) (<-chan ResponseChunk, error) {
	resp, err := m.Invoke(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan ResponseChunk, 1)
	go func() {
		defer close(ch)
		if len(resp.ToolCalls) > 0 {
			ch <- ResponseChunk{ToolCall: &resp.ToolCalls[0], Done: true}
			return
		}
		ch <- ResponseChunk{Content: resp.Content, Done: true, FinishReason: "stop"}
	}()
	return ch, nil
}

func lastUserMessage(msgs []types.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == types.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

func lastTurnMessages(msgs []types.Message) []types.Message {
	lastUserIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == types.RoleUser {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		return msgs
	}
	return msgs[lastUserIdx:]
}

func (m *MockModel) pendingToolCall(req *InvokeRequest) *types.ToolCall {
	if len(req.Tools) == 0 {
		return nil
	}
	turn := lastTurnMessages(req.Messages)
	for _, msg := range turn {
		if msg.Role == types.RoleTool {
			return nil
		}
	}

	lastUser := lastUserMessage(req.Messages)
	toolNames := make(map[string]bool, len(req.Tools))
	for _, td := range req.Tools {
		toolNames[td.Name] = true
	}

	if toolNames["classify_intent"] {
		args, _ := json.Marshal(map[string]string{"input": lastUser})
		return &types.ToolCall{
			ID:   "mock-classify-1",
			Type: "function",
			Function: types.ToolCallFunction{
				Name:      "classify_intent",
				Arguments: string(args),
			},
		}
	}

	lower := strings.ToLower(lastUser)
	if toolNames["create_refund"] && strings.Contains(lower, "退款") {
		orderID := extractToken(lastUser, "ord")
		args, _ := json.Marshal(map[string]any{
			"order_id": orderID,
			"user_id":  "u1002",
			"reason":   lastUser,
			"amount":   3299.0,
		})
		return &types.ToolCall{
			ID: "mock-refund", Type: "function",
			Function: types.ToolCallFunction{Name: "create_refund", Arguments: string(args)},
		}
	}
	if toolNames["track_logistics"] && strings.Contains(lower, "sf") {
		tracking := extractToken(lastUser, "SF")
		if tracking == "" {
			tracking = extractToken(lastUser, "sf")
		}
		args, _ := json.Marshal(map[string]string{"tracking_number": tracking})
		return &types.ToolCall{
			ID: "mock-track", Type: "function",
			Function: types.ToolCallFunction{Name: "track_logistics", Arguments: string(args)},
		}
	}
	if toolNames["query_order"] && strings.Contains(lower, "ord") {
		orderID := extractToken(lastUser, "ord")
		args, _ := json.Marshal(map[string]string{"order_id": orderID})
		return &types.ToolCall{
			ID: "mock-query-order", Type: "function",
			Function: types.ToolCallFunction{Name: "query_order", Arguments: string(args)},
		}
	}
	if toolNames["recommend_product"] && (strings.Contains(lower, "推荐") || strings.Contains(lower, "键盘")) {
		args, _ := json.Marshal(map[string]string{"keyword": "键盘"})
		return &types.ToolCall{
			ID: "mock-recommend", Type: "function",
			Function: types.ToolCallFunction{Name: "recommend_product", Arguments: string(args)},
		}
	}
	return nil
}

func (m *MockModel) finalReply(req *InvokeRequest, lastUser string) string {
	turn := lastTurnMessages(req.Messages)
	var toolResult string
	for i := len(turn) - 1; i >= 0; i-- {
		if turn[i].Role == types.RoleTool {
			toolResult = turn[i].Content
			break
		}
	}
	if toolResult != "" {
		return fmt.Sprintf("根据系统查询结果：%s\n\n如需进一步帮助，请告诉我。", toolResult)
	}
	return fmt.Sprintf("您好，我是京东智能客服（模拟模式）。已收到您的问题：「%s」。", lastUser)
}

func extractToken(text, prefix string) string {
	lower := strings.ToLower(text)
	prefixLower := strings.ToLower(prefix)
	idx := strings.Index(lower, prefixLower)
	if idx < 0 {
		return ""
	}
	start := idx
	for start > 0 && (isAlnum(text[start-1]) || text[start-1] == '_') {
		start--
	}
	end := idx + len(prefix)
	for end < len(text) && (isAlnum(text[end]) || text[end] == '_') {
		end++
	}
	return text[start:end]
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
