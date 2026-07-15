package builtin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/tool"
)

const maxDirectoryEntries = 1000

type ListDirTool struct {
	tool.BaseTool
	allowedDir string
}

func NewListDir(allowedDir string) *ListDirTool {
	return &ListDirTool{
		BaseTool: tool.NewBaseTool(
			"list_dir",
			"List at most 1000 entries within the configured root directory.",
			[]model.ToolParameter{
				{Name: "path", Type: "string", Description: "Directory path relative to the configured root (default: .)", Required: false},
			},
		),
		allowedDir: normalizeAllowedDir(allowedDir),
	}
}

func (t *ListDirTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
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
		return nil, fmt.Errorf("securely open directory %q: %w", path, err)
	}
	defer file.Close()

	entries, err := file.ReadDir(maxDirectoryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read directory %q: %w", path, err)
	}
	truncated := len(entries) > maxDirectoryEntries
	if truncated {
		entries = entries[:maxDirectoryEntries]
	}

	result := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		info := map[string]any{"name": entry.Name(), "is_dir": entry.IsDir()}
		if fileInfo, infoErr := entry.Info(); infoErr == nil && !entry.IsDir() {
			info["size"] = fileInfo.Size()
		}
		result = append(result, info)
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return map[string]any{
		"path": path, "entries": result, "truncated": truncated,
	}, nil
}
