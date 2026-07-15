package engine

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/apexracing/tracklogic-agent/interaction"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/task"
	"github.com/apexracing/tracklogic-agent/types"
)

const (
	reportProgressTool = "report_progress"
	requestInputTool   = "request_user_input"
)

func taskControlDefinitions() []model.ToolDefinition {
	return []model.ToolDefinition{
		{
			Name:        reportProgressTool,
			Description: "Provide a short user-readable work status. Do not include hidden reasoning.",
			Parameters: model.ToolParameters{
				Type: "object",
				Properties: map[string]model.ToolParameter{
					"summary": {Type: "string", Description: "A concise status of at most 200 Unicode characters."},
				},
				Required: []string{"summary"},
			},
		},
		{
			Name:        requestInputTool,
			Description: "Pause the current turn and ask the user one to three structured questions.",
			Parameters: model.ToolParameters{RawSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{"type": "string"},
					"questions": map[string]any{
						"type": "array", "minItems": 1, "maxItems": 3,
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id":         map[string]any{"type": "string"},
								"header":     map[string]any{"type": "string"},
								"prompt":     map[string]any{"type": "string"},
								"allow_text": map[string]any{"type": "boolean"},
								"required":   map[string]any{"type": "boolean"},
								"options": map[string]any{
									"type": "array", "maxItems": 3,
									"items": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"label":       map[string]any{"type": "string"},
											"description": map[string]any{"type": "string"},
											"recommended": map[string]any{"type": "boolean"},
										},
										"required": []string{"label"},
									},
								},
							},
							"required": []string{"id", "prompt"},
						},
					},
				},
				"required": []string{"questions"},
			}},
		},
	}
}

func executeTaskControl(ctx context.Context, runtime task.Runtime, call types.ToolCall, checkpoint task.Checkpoint) (string, bool, error) {
	switch call.Function.Name {
	case reportProgressTool:
		var arguments struct {
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
			return "", true, types.WrapError(types.ErrInvalidInput, "failed to parse progress arguments", err)
		}
		if strings.TrimSpace(arguments.Summary) == "" {
			return "", true, types.NewError(types.ErrInvalidInput, "progress summary is required")
		}
		if err := runtime.ReportProgress(ctx, arguments.Summary); err != nil {
			return "", true, err
		}
		return `{"accepted":true}`, true, nil
	case requestInputTool:
		var request interaction.Request
		if err := json.Unmarshal([]byte(call.Function.Arguments), &request); err != nil {
			return "", true, types.WrapError(types.ErrInvalidInput, "failed to parse interaction arguments", err)
		}
		if request.ID == "" {
			request.ID = task.NewID("interaction")
		}
		response, err := runtime.RequestInput(ctx, request, checkpoint)
		if err != nil {
			return "", true, err
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return "", true, types.WrapError(types.ErrToolError, "failed to encode interaction response", err)
		}
		return string(encoded), true, nil
	default:
		return "", false, nil
	}
}
