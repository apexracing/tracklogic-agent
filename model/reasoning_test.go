package model

import "testing"

func TestReasoningEffortIsMappedByProtocol(t *testing.T) {
	request := &InvokeRequest{ReasoningEffort: "medium"}

	responses, err := NewOpenAI(OpenAIConfig{}).buildRequest(request, false)
	if err != nil {
		t.Fatal(err)
	}
	if responses.Reasoning == nil || responses.Reasoning.Effort != "medium" || responses.Reasoning.Summary != "auto" {
		t.Fatalf("responses reasoning = %#v", responses.Reasoning)
	}

	chat, err := NewOpenAIChat(OpenAIConfig{Vendor: "deepseek"}).buildRequest(request, false)
	if err != nil {
		t.Fatal(err)
	}
	if chat.ReasoningEffort != "medium" || chat.Thinking == nil || chat.Thinking.Type != "enabled" {
		t.Fatalf("chat reasoning = %q, thinking = %#v", chat.ReasoningEffort, chat.Thinking)
	}

	messages, err := NewAnthropic(AnthropicConfig{Vendor: "anthropic"}).buildRequest(request, false)
	if err != nil {
		t.Fatal(err)
	}
	if messages.OutputConfig == nil || messages.OutputConfig.Effort != "medium" || messages.Thinking == nil || messages.Thinking.Type != "adaptive" {
		t.Fatalf("messages effort = %#v, thinking = %#v", messages.OutputConfig, messages.Thinking)
	}
}

func TestAutoReasoningOmitsProtocolParameters(t *testing.T) {
	request := &InvokeRequest{}
	responses, _ := NewOpenAI(OpenAIConfig{}).buildRequest(request, false)
	chat, _ := NewOpenAIChat(OpenAIConfig{Vendor: "deepseek"}).buildRequest(request, false)
	messages, _ := NewAnthropic(AnthropicConfig{Vendor: "anthropic"}).buildRequest(request, false)

	if responses.Reasoning != nil || chat.ReasoningEffort != "" || chat.Thinking != nil || messages.OutputConfig != nil || messages.Thinking != nil {
		t.Fatal("auto reasoning must leave provider defaults unchanged")
	}
}
