package security

import (
	"regexp"
	"strings"
)

var (
	phonePattern      = regexp.MustCompile(`1[3-9]\d{9}`)
	emailPattern      = regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	idCardPattern     = regexp.MustCompile(`\d{18}|\d{17}X`)
	creditCardPattern = regexp.MustCompile(`\d{4}[- ]?\d{4}[- ]?\d{4}[- ]?\d{4}`)
)

type Sanitizer struct {
	rules []SanitizeRule
}

type SanitizeRule struct {
	Name    string
	Pattern *regexp.Regexp
	Replace func(string) string
}

func NewSanitizer() *Sanitizer {
	s := &Sanitizer{}
	s.addDefaultRules()
	return s
}

func (s *Sanitizer) addDefaultRules() {
	s.rules = []SanitizeRule{
		{
			Name:    "phone",
			Pattern: phonePattern,
			Replace: func(m string) string {
				if len(m) == 11 {
					return m[:3] + "****" + m[7:]
				}
				return strings.Repeat("*", len(m))
			},
		},
		{
			Name:    "email",
			Pattern: emailPattern,
			Replace: func(m string) string {
				parts := strings.Split(m, "@")
				if len(parts) == 2 && len(parts[0]) > 2 {
					return parts[0][:2] + "***@" + parts[1]
				}
				return "***@" + parts[1]
			},
		},
		{
			Name:    "id_card",
			Pattern: idCardPattern,
			Replace: func(m string) string {
				if len(m) >= 10 {
					return m[:4] + "**********" + m[len(m)-4:]
				}
				return strings.Repeat("*", len(m))
			},
		},
		{
			Name:    "credit_card",
			Pattern: creditCardPattern,
			Replace: func(m string) string {
				cleaned := strings.ReplaceAll(strings.ReplaceAll(m, "-", ""), " ", "")
				if len(cleaned) >= 8 {
					return cleaned[:4] + " **** **** " + cleaned[len(cleaned)-4:]
				}
				return strings.Repeat("*", len(cleaned))
			},
		},
	}
}

func (s *Sanitizer) Sanitize(input string) string {
	result := input
	for _, rule := range s.rules {
		result = rule.Pattern.ReplaceAllStringFunc(result, rule.Replace)
	}
	return result
}

func (s *Sanitizer) AddRule(rule SanitizeRule) {
	s.rules = append(s.rules, rule)
}
