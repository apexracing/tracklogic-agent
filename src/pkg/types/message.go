package types

import "time"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role      Role          `json:"role"`
	Content   string        `json:"content"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	ToolCalls []ToolCall    `json:"tool_calls,omitempty"`
	Name      string        `json:"name,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
