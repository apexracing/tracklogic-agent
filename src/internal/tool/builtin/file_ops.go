package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go-harness-tutorial/internal/model"
	"go-harness-tutorial/internal/tool"
)

type ReadFileTool struct {
	tool.BaseTool
	allowedDir string
}

func NewReadFile(allowedDir string) *ReadFileTool {
	return &ReadFileTool{
		BaseTool: tool.NewBaseTool(
			"read_file",
			"Read the contents of a file. Only files within the allowed directory can be read.",
			[]model.ToolParameter{
				{Name: "path", Type: "string", Description: "Relative path to the file", Required: true},
			},
		),
		allowedDir: allowedDir,
	}
}

func (t *ReadFileTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}

	fullPath, err := t.safePath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return string(data), nil
}

func (t *ReadFileTool) safePath(path string) (string, error) {
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
	if err != nil || rel[:2] == ".." {
		return "", fmt.Errorf("path traversal detected: %q is outside allowed directory", path)
	}
	return absPath, nil
}

type WriteFileTool struct {
	tool.BaseTool
	allowedDir string
}

func NewWriteFile(allowedDir string) *WriteFileTool {
	return &WriteFileTool{
		BaseTool: tool.NewBaseTool(
			"write_file",
			"Write content to a file. Creates parent directories if needed.",
			[]model.ToolParameter{
				{Name: "path", Type: "string", Description: "Relative path to the file", Required: true},
				{Name: "content", Type: "string", Description: "Content to write", Required: true},
			},
		),
		allowedDir: allowedDir,
	}
}

func (t *WriteFileTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)

	fullPath, err := t.safePath(path)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create directories: %w", err)
	}

	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return map[string]any{"path": path, "size": len(content)}, nil
}

func (t *WriteFileTool) safePath(path string) (string, error) {
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
	if err != nil || len(rel) >= 2 && rel[:2] == ".." {
		return "", fmt.Errorf("path traversal detected")
	}
	return absPath, nil
}
