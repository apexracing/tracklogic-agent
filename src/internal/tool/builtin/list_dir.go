package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go-harness-tutorial/internal/model"
	"go-harness-tutorial/internal/tool"
)

type ListDirTool struct {
	tool.BaseTool
	allowedDir string
}

func NewListDir(allowedDir string) *ListDirTool {
	return &ListDirTool{
		BaseTool: tool.NewBaseTool(
			"list_dir",
			"List files and directories within the allowed directory.",
			[]model.ToolParameter{
				{Name: "path", Type: "string", Description: "Relative path to the directory (default: .)", Required: false},
			},
		),
		allowedDir: allowedDir,
	}
}

func (t *ListDirTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}

	fullPath, err := t.safePath(path)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to list directory: %w", err)
	}

	result := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		info := map[string]any{
			"name":   e.Name(),
			"is_dir": e.IsDir(),
		}
		if fi, err := e.Info(); err == nil && !e.IsDir() {
			info["size"] = fi.Size()
		}
		result = append(result, info)
	}

	return map[string]any{
		"path":    path,
		"entries": result,
	}, nil
}

func (t *ListDirTool) safePath(path string) (string, error) {
	if t.allowedDir == "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	fullPath := filepath.Join(t.allowedDir, path)
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		return "", err
	}
	absAllowed, err := filepath.Abs(t.allowedDir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absAllowed, absPath)
	if err != nil || len(rel) >= 2 && rel[:2] == ".." || rel == ".." {
		return "", fmt.Errorf("path traversal detected: %q is outside allowed directory", path)
	}
	return absPath, nil
}
