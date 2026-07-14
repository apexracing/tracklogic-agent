package builtin

import (
	"context"
	"fmt"
	"time"

	"go-harness-tutorial/internal/model"
	"go-harness-tutorial/internal/tool"
)

type CurrentTimeTool struct {
	tool.BaseTool
}

func NewCurrentTime() *CurrentTimeTool {
	return &CurrentTimeTool{
		BaseTool: tool.NewBaseTool(
			"get_current_time",
			"Get the current date and time. Optionally specify an IANA timezone (e.g. Asia/Shanghai).",
			[]model.ToolParameter{
				{Name: "timezone", Type: "string", Description: "IANA timezone name (default: UTC)", Required: false},
			},
		),
	}
}

func (t *CurrentTimeTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	tz, _ := args["timezone"].(string)
	if tz == "" {
		tz = "UTC"
	}

	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", tz, err)
	}

	now := time.Now().In(loc)
	return map[string]any{
		"rfc3339":  now.Format(time.RFC3339),
		"unix":     now.Unix(),
		"timezone": tz,
	}, nil
}
