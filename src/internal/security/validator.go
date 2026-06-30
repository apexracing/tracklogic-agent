package security

import (
	"fmt"
	"regexp"
	"strings"
)

var promptInjectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?(previous|above|below)\s+instructions`),
	regexp.MustCompile(`(?i)forget\s+(all\s+)?(previous|above|below)\s+(instructions|prompts|context)`),
	regexp.MustCompile(`(?i)you\s+are\s+(not\s+)?(an?\s+)?(AI|assistant|GPT|chatbot)`),
	regexp.MustCompile(`(?i)system\s+(prompt|message|instruction)`),
	regexp.MustCompile(`(?i)pretend\s+(to\s+)?be`),
	regexp.MustCompile(`(?i)do\s+(not\s+)?(follow|obey|respect)\s+(the\s+)?(previous|above|given)\s+(instructions|constraints|rules)`),
	regexp.MustCompile(`(?i)角色\s*(切换|扮演|设定)`),
	regexp.MustCompile(`(?i)忽略\s*(前面|以上|之前)\s*(的\s*)?(指令|要求|规则|设定)`),
}

type InputValidator struct {
	maxLength    int
	blockedWords []string
}

func NewInputValidator() *InputValidator {
	return &InputValidator{
		maxLength: 10000,
		blockedWords: []string{
			"<script>", "javascript:", "onerror=", "onload=",
		},
	}
}

func (v *InputValidator) Validate(input string) error {
	if len(input) == 0 {
		return fmt.Errorf("empty input")
	}
	if len(input) > v.maxLength {
		return fmt.Errorf("input exceeds max length of %d characters", v.maxLength)
	}
	for _, pattern := range promptInjectionPatterns {
		if pattern.MatchString(input) {
			return fmt.Errorf("potential prompt injection detected: matched pattern %q", pattern.String())
		}
	}
	lower := strings.ToLower(input)
	for _, word := range v.blockedWords {
		if strings.Contains(lower, word) {
			return fmt.Errorf("blocked content detected: %q", word)
		}
	}
	return nil
}

type OutputValidator struct {
	MaxLength int
}

func NewOutputValidator() *OutputValidator {
	return &OutputValidator{MaxLength: 50000}
}

func (v *OutputValidator) Validate(output string) error {
	if len(output) > v.MaxLength {
		return fmt.Errorf("output exceeds max length of %d characters", v.MaxLength)
	}
	return nil
}
