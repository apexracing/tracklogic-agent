package model

import "testing"

func TestRawToolSchemaIsPreservedByProviders(t *testing.T) {
	rawSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"fields": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string", "pattern": "^[a-z_]+$"},
			},
		},
		"required": []any{"fields"},
	}
	request := &InvokeRequest{Tools: []ToolDefinition{{
		Name: "query_items", Parameters: ToolParameters{RawSchema: rawSchema},
	}}}

	openAI, err := NewOpenAI(OpenAIConfig{}).buildRequest(request, false)
	if err != nil {
		t.Fatalf("OpenAI buildRequest: %v", err)
	}
	assertNestedItems(t, openAI.Tools[0].Parameters)

	chat, err := NewOpenAIChat(OpenAIConfig{}).buildRequest(request, false)
	if err != nil {
		t.Fatalf("Chat buildRequest: %v", err)
	}
	chatSchema := chat.Tools[0].Function["parameters"].(map[string]any)
	assertNestedItems(t, chatSchema)

	anthropic, err := NewAnthropic(AnthropicConfig{}).buildRequest(request, false)
	if err != nil {
		t.Fatalf("Anthropic buildRequest: %v", err)
	}
	assertNestedItems(t, anthropic.Tools[0].InputSchema)
}

func assertNestedItems(t *testing.T, schema map[string]any) {
	t.Helper()
	properties := schema["properties"].(map[string]any)
	fields := properties["fields"].(map[string]any)
	items := fields["items"].(map[string]any)
	if items["pattern"] != "^[a-z_]+$" {
		t.Fatalf("nested schema lost: %#v", schema)
	}
}
