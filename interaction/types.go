package interaction

import (
	"fmt"
	"strings"
	"time"
)

// Option is one selectable answer to a Question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

// Question is a structured prompt that an application can render without
// parsing model-generated prose.
type Question struct {
	ID        string   `json:"id"`
	Header    string   `json:"header,omitempty"`
	Prompt    string   `json:"prompt"`
	Options   []Option `json:"options,omitempty"`
	AllowText bool     `json:"allow_text,omitempty"`
	Required  bool     `json:"required,omitempty"`
}

// Request groups one to three questions under a stable correlation ID.
type Request struct {
	ID        string     `json:"id"`
	Title     string     `json:"title,omitempty"`
	Questions []Question `json:"questions"`
}

// Response carries answers back to the suspended turn.
type Response struct {
	RequestID  string              `json:"request_id"`
	Answers    map[string][]string `json:"answers"`
	AnsweredAt time.Time           `json:"answered_at"`
}

func (r Request) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("interaction request id is required")
	}
	if len(r.Questions) == 0 || len(r.Questions) > 3 {
		return fmt.Errorf("interaction must contain between one and three questions")
	}
	seen := make(map[string]struct{}, len(r.Questions))
	for index, question := range r.Questions {
		if strings.TrimSpace(question.ID) == "" {
			return fmt.Errorf("question %d id is required", index)
		}
		if _, duplicate := seen[question.ID]; duplicate {
			return fmt.Errorf("duplicate question id %q", question.ID)
		}
		seen[question.ID] = struct{}{}
		if strings.TrimSpace(question.Prompt) == "" {
			return fmt.Errorf("question %q prompt is required", question.ID)
		}
		if len(question.Options) > 3 {
			return fmt.Errorf("question %q may contain at most three options", question.ID)
		}
		if len(question.Options) == 0 && !question.AllowText {
			return fmt.Errorf("question %q requires options or free-text input", question.ID)
		}
		labels := make(map[string]struct{}, len(question.Options))
		for _, option := range question.Options {
			label := strings.TrimSpace(option.Label)
			if label == "" {
				return fmt.Errorf("question %q contains an empty option", question.ID)
			}
			if _, duplicate := labels[label]; duplicate {
				return fmt.Errorf("question %q contains duplicate option %q", question.ID, label)
			}
			labels[label] = struct{}{}
		}
	}
	return nil
}

// ValidateAgainst verifies that a response belongs to the request and only
// contains allowed question IDs and option labels. Free text is accepted only
// when the corresponding question enables it.
func (r Response) ValidateAgainst(request Request) error {
	if r.RequestID != request.ID {
		return fmt.Errorf("response request id %q does not match %q", r.RequestID, request.ID)
	}
	questions := make(map[string]Question, len(request.Questions))
	for _, question := range request.Questions {
		questions[question.ID] = question
	}
	for id := range r.Answers {
		if _, ok := questions[id]; !ok {
			return fmt.Errorf("answer references unknown question %q", id)
		}
	}
	for _, question := range request.Questions {
		answers := r.Answers[question.ID]
		if question.Required && len(answers) == 0 {
			return fmt.Errorf("question %q requires an answer", question.ID)
		}
		allowed := make(map[string]struct{}, len(question.Options))
		for _, option := range question.Options {
			allowed[option.Label] = struct{}{}
		}
		for _, answer := range answers {
			answer = strings.TrimSpace(answer)
			if answer == "" {
				return fmt.Errorf("question %q contains an empty answer", question.ID)
			}
			if _, ok := allowed[answer]; !ok && !question.AllowText {
				return fmt.Errorf("question %q answer %q is not an allowed option", question.ID, answer)
			}
		}
	}
	return nil
}
