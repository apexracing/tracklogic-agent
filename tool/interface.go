package tool

import (
	"context"
	"fmt"
	"math"
	"slices"

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
	parameters := make([]model.ToolParameter, len(params))
	for index, parameter := range params {
		parameter.Enum = append([]string(nil), parameter.Enum...)
		parameters[index] = parameter
	}
	return BaseTool{name: name, description: description, parameters: parameters}
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
		p.Enum = append([]string(nil), p.Enum...)
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
		value, exists := args[p.Name]
		if p.Required && !exists {
			return &ValidationError{Field: p.Name, Message: "required field missing"}
		}
		if !exists {
			continue
		}
		if err := validateParameterType(p.Type, value); err != nil {
			return &ValidationError{Field: p.Name, Message: err.Error()}
		}
		if len(p.Enum) > 0 {
			text, ok := value.(string)
			if !ok || !slices.Contains(p.Enum, text) {
				return &ValidationError{Field: p.Name, Message: fmt.Sprintf("must be one of %v", p.Enum)}
			}
		}
	}
	return nil
}

func validateParameterType(expected string, value any) error {
	valid := false
	switch expected {
	case "", "any":
		valid = true
	case "string":
		_, valid = value.(string)
	case "number":
		switch number := value.(type) {
		case float64:
			valid = !math.IsNaN(number) && !math.IsInf(number, 0)
		case float32:
			converted := float64(number)
			valid = !math.IsNaN(converted) && !math.IsInf(converted, 0)
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			valid = true
		}
	case "integer":
		switch number := value.(type) {
		case float64:
			valid = !math.IsNaN(number) && !math.IsInf(number, 0) && math.Trunc(number) == number
		case float32:
			valid = !float32IsNonInteger(number)
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			valid = true
		}
	case "boolean":
		_, valid = value.(bool)
	case "object":
		_, valid = value.(map[string]any)
	case "array":
		switch value.(type) {
		case []any, []string, []int, []float64:
			valid = true
		}
	default:
		return fmt.Errorf("uses unsupported schema type %q", expected)
	}
	if !valid {
		return fmt.Errorf("must be of type %s", expected)
	}
	return nil
}

func float32IsNonInteger(number float32) bool {
	converted := float64(number)
	return math.IsNaN(converted) || math.IsInf(converted, 0) || math.Trunc(converted) != converted
}
