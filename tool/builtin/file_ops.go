package builtin

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool"
)

const maxFileContentBytes = 1 << 20

type ReadFileTool struct {
	tool.BaseTool
	allowedDir string
}

func NewReadFile(allowedDir string) *ReadFileTool {
	return &ReadFileTool{
		BaseTool: tool.NewBaseTool(
			"read_file",
			"Read a file within the configured root directory (maximum 1 MiB).",
			[]model.ToolParameter{
				{Name: "path", Type: "string", Description: "Path relative to the configured root", Required: true},
			},
		),
		allowedDir: normalizeAllowedDir(allowedDir),
	}
}

func (t *ReadFileTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	root, err := os.OpenRoot(t.allowedDir)
	if err != nil {
		return nil, fmt.Errorf("open allowed directory: %w", err)
	}
	defer root.Close()

	file, err := root.Open(path)
	if err != nil {
		return nil, fmt.Errorf("securely open file %q: %w", path, err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxFileContentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read file %q: %w", path, err)
	}
	if len(data) > maxFileContentBytes {
		return nil, fmt.Errorf("file %q exceeds %d bytes", path, maxFileContentBytes)
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return string(data), nil
}

type WriteFileTool struct {
	tool.BaseTool
	allowedDir string
}

func NewWriteFile(allowedDir string) *WriteFileTool {
	return &WriteFileTool{
		BaseTool: tool.NewBaseTool(
			"write_file",
			"Write a file within the configured root directory (maximum 1 MiB).",
			[]model.ToolParameter{
				{Name: "path", Type: "string", Description: "Path relative to the configured root", Required: true},
				{Name: "content", Type: "string", Description: "Content to write", Required: true},
			},
		),
		allowedDir: normalizeAllowedDir(allowedDir),
	}
}

func (t *WriteFileTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if len(content) > maxFileContentBytes {
		return nil, fmt.Errorf("content exceeds %d bytes", maxFileContentBytes)
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}

	root, err := os.OpenRoot(t.allowedDir)
	if err != nil {
		return nil, fmt.Errorf("open allowed directory: %w", err)
	}
	defer root.Close()

	parent := filepath.Dir(path)
	if parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return nil, fmt.Errorf("securely create parent directory for %q: %w", path, err)
		}
	}
	if err := root.WriteFile(path, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("securely write file %q: %w", path, err)
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "size": len(content)}, nil
}

func normalizeAllowedDir(directory string) string {
	if directory == "" {
		return "."
	}
	return directory
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
