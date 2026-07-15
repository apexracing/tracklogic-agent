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
	// RawSchema preserves arbitrary JSON Schema keywords discovered through
	// MCP (for example items, oneOf, $defs, and nested object constraints).
	// When set, Schema returns this map unchanged except for a shallow copy.
	RawSchema map[string]any `json:"-"`
}

// Schema returns the JSON Schema representation used by model providers.
func (parameters ToolParameters) Schema() map[string]any {
	if parameters.RawSchema != nil {
		result := make(map[string]any, len(parameters.RawSchema))
		for key, value := range parameters.RawSchema {
			result[key] = value
		}
		return result
	}
	result := map[string]any{
		"type":       parameters.Type,
		"properties": parameters.Properties,
	}
	if len(parameters.Required) > 0 {
		result["required"] = parameters.Required
	}
	return result
}

type ToolParameter struct {
	Name        string   `json:"-"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Required    bool     `json:"-"`
	Enum        []string `json:"enum,omitempty"`
}
