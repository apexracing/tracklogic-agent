package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool"
)

type JSONParseTool struct {
	tool.BaseTool
}

func NewJSONParse() *JSONParseTool {
	return &JSONParseTool{
		BaseTool: tool.NewBaseTool(
			"json_parse",
			"Parse a JSON string into a structured value.",
			[]model.ToolParameter{
				{Name: "text", Type: "string", Description: "JSON text to parse", Required: true},
			},
		),
	}
}

func (t *JSONParseTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	text, _ := args["text"].(string)
	if text == "" {
		return nil, fmt.Errorf("text is required")
	}

	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	return map[string]any{
		"value": parsed,
	}, nil
}
