package model

import (
	"context"
	"github.com/apexracing/tracklogic-agent/types"
)

type Model interface {
	Invoke(ctx context.Context, req *InvokeRequest) (*InvokeResponse, error)
	InvokeStream(ctx context.Context, req *InvokeRequest) (<-chan ResponseChunk, error)
	Provider() string
	ModelID() string
}

type InvokeRequest struct {
	Messages    []types.Message
	Tools       []ToolDefinition
	Temperature float64
	MaxTokens   int
	Stream      bool
	Extra       map[string]any
}

type InvokeResponse struct {
	Content      string
	ToolCalls    []types.ToolCall
	Usage        *types.Usage
	FinishReason string
	Metadata     map[string]any
}

type ResponseChunk struct {
	Content      string
	ToolCall     *types.ToolCall
	FinishReason string
	Usage        *types.Usage
	Done         bool
	Error        error
}

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  ToolParameters `json:"parameters"`
}

type ToolParameters struct {
	Type       string                   `json:"type"`
	Properties map[string]ToolParameter `json:"properties"`
	Required   []string                 `json:"required,omitempty"`
}

type ToolParameter struct {
	Name        string   `json:"-"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Required    bool     `json:"-"`
	Enum        []string `json:"enum,omitempty"`
}
