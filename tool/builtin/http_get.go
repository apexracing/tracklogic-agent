package builtin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool"
)

const (
	defaultHTTPTimeout = 10
	maxHTTPBodyBytes   = 64 * 1024
)

type HTTPGetTool struct {
	tool.BaseTool
	client *http.Client
}

func NewHTTPGet() *HTTPGetTool {
	return &HTTPGetTool{
		BaseTool: tool.NewBaseTool(
			"http_get",
			"Send an HTTP GET request and return status, headers summary, and response body (truncated to 64KiB).",
			[]model.ToolParameter{
				{Name: "url", Type: "string", Description: "URL to fetch", Required: true},
				{Name: "timeout_seconds", Type: "number", Description: "Request timeout in seconds (default: 10)", Required: false},
			},
		),
		client: &http.Client{},
	}
}

func (t *HTTPGetTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	urlStr, _ := args["url"].(string)
	if urlStr == "" {
		return nil, fmt.Errorf("url is required")
	}

	timeoutSec := defaultHTTPTimeout
	switch v := args["timeout_seconds"].(type) {
	case float64:
		if v > 0 {
			timeoutSec = int(v)
		}
	case int:
		if v > 0 {
			timeoutSec = v
		}
	}

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}
	req.Header.Set("User-Agent", "tracklogic-agent/http_get")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get failed: %w", err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxHTTPBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	truncated := false
	if len(body) > maxHTTPBodyBytes {
		body = body[:maxHTTPBodyBytes]
		truncated = true
	}

	return map[string]any{
		"status":         resp.StatusCode,
		"content_type":   resp.Header.Get("Content-Type"),
		"body":           string(body),
		"truncated":      truncated,
		"content_length": len(body),
	}, nil
}
