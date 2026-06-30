package tool

import "fmt"

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error: field %q - %s", e.Field, e.Message)
}

type ExecutionError struct {
	ToolName string
	Message  string
	Err      error
}

func (e *ExecutionError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("tool %q execution failed: %s: %v", e.ToolName, e.Message, e.Err)
	}
	return fmt.Sprintf("tool %q execution failed: %s", e.ToolName, e.Message)
}

func (e *ExecutionError) Unwrap() error {
	return e.Err
}
