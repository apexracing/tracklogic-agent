package tool

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/apexracing/tracklogic-agent/model"
)

type testTool struct {
	BaseTool
	definitionName string
}

func TestBaseToolRejectsNonFiniteNumbers(t *testing.T) {
	runtimeTool := NewBaseTool("bounded", "", []model.ToolParameter{{Name: "value", Type: "number", Required: true}})
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := runtimeTool.Validate(map[string]any{"value": value}); err == nil {
			t.Fatalf("Validate(%v) error = nil", value)
		}
	}
}

func TestBaseToolOwnsParameterCollections(t *testing.T) {
	enum := []string{"safe"}
	parameters := []model.ToolParameter{{Name: "mode", Type: "string", Enum: enum}}
	runtimeTool := NewBaseTool("owned", "", parameters)
	enum[0] = "changed"
	parameters[0].Name = "changed"

	definition := runtimeTool.Definition()
	parameter := definition.Parameters.Properties["mode"]
	if parameter.Name != "mode" || !reflect.DeepEqual(parameter.Enum, []string{"safe"}) {
		t.Fatalf("Definition() = %+v, want owned parameter snapshot", definition)
	}
	parameter.Enum[0] = "mutated-copy"
	if got := runtimeTool.Definition().Parameters.Properties["mode"].Enum[0]; got != "safe" {
		t.Fatalf("Definition() exposed internal enum: %q", got)
	}
}

func newTestTool(name string) *testTool {
	return &testTool{BaseTool: NewBaseTool(name, "test", nil)}
}

func (tool *testTool) Definition() model.ToolDefinition {
	definition := tool.BaseTool.Definition()
	if tool.definitionName != "" {
		definition.Name = tool.definitionName
	}
	return definition
}

func (*testTool) Execute(context.Context, map[string]any) (any, error) { return "ok", nil }

func TestRegistryRejectsMalformedTools(t *testing.T) {
	var typedNil *testTool
	tests := []struct {
		name string
		tool Tool
		want string
	}{
		{name: "nil", tool: nil, want: "required"},
		{name: "typed nil", tool: typedNil, want: "required"},
		{name: "empty name", tool: newTestTool(""), want: "name"},
		{name: "surrounding whitespace", tool: newTestTool(" bad "), want: "whitespace"},
		{name: "definition mismatch", tool: &testTool{BaseTool: NewBaseTool("one", "", nil), definitionName: "two"}, want: "does not match"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := NewRegistry().Register(test.tool)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Register() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestRegistryReturnsDeterministicOrder(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{"zeta", "alpha", "middle"} {
		if err := registry.Register(newTestTool(name)); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
	}
	want := []string{"alpha", "middle", "zeta"}
	if got := registry.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	listed := registry.List()
	got := make([]string, len(listed))
	for index, runtimeTool := range listed {
		got[index] = runtimeTool.Name()
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}
}

func TestBaseToolValidatesTypesAndEnums(t *testing.T) {
	base := NewBaseTool("validated", "", []model.ToolParameter{
		{Name: "count", Type: "integer", Required: true},
		{Name: "mode", Type: "string", Enum: []string{"fast", "safe"}},
	})

	if err := base.Validate(map[string]any{"count": float64(2), "mode": "safe"}); err != nil {
		t.Fatalf("valid arguments rejected: %v", err)
	}
	for _, args := range []map[string]any{
		{"count": 2.5},
		{"count": "2"},
		{"count": 2, "mode": "unknown"},
	} {
		if err := base.Validate(args); err == nil {
			t.Fatalf("invalid arguments accepted: %#v", args)
		}
	}
}
