package tool

import (
	"context"
	"github.com/apexracing/tracklogic-agent/model"
)

type Tool interface {
	Name() string
	Description() string
	Definition() model.ToolDefinition
	Validate(args map[string]any) error
	Execute(ctx context.Context, args map[string]any) (any, error)
}

type BaseTool struct {
	name        string
	description string
	parameters  []model.ToolParameter
}

func NewBaseTool(name, description string, params []model.ToolParameter) BaseTool {
	return BaseTool{name: name, description: description, parameters: params}
}

func (b BaseTool) Name() string {
	return b.name
}

func (b BaseTool) Description() string {
	return b.description
}

func (b BaseTool) Definition() model.ToolDefinition {
	props := make(map[string]model.ToolParameter)
	required := make([]string, 0)
	for _, p := range b.parameters {
		props[p.Name] = p
		if p.Required {
			required = append(required, p.Name)
		}
	}
	return model.ToolDefinition{
		Name:        b.name,
		Description: b.description,
		Parameters: model.ToolParameters{
			Type:       "object",
			Properties: props,
			Required:   required,
		},
	}
}

func (b BaseTool) Validate(args map[string]any) error {
	for _, p := range b.parameters {
		if p.Required {
			if _, ok := args[p.Name]; !ok {
				return &ValidationError{Field: p.Name, Message: "required field missing"}
			}
		}
	}
	return nil
}
