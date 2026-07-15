package types

import "fmt"

type ErrorCode string

const (
	ErrModelTimeout        ErrorCode = "MODEL_TIMEOUT"
	ErrToolError           ErrorCode = "TOOL_ERROR"
	ErrInvalidInput        ErrorCode = "INVALID_INPUT"
	ErrInvalidConfig       ErrorCode = "INVALID_CONFIG"
	ErrAPIError            ErrorCode = "API_ERROR"
	ErrRateLimit           ErrorCode = "RATE_LIMIT"
	ErrRunCancelled        ErrorCode = "RUN_CANCELLED"
	ErrSecurityViolation   ErrorCode = "SECURITY_VIOLATION"
	ErrMaxLoopsExceeded    ErrorCode = "MAX_LOOPS_EXCEEDED"
	ErrCircuitOpen         ErrorCode = "CIRCUIT_OPEN"
	ErrEventDelivery       ErrorCode = "EVENT_DELIVERY_FAILED"
	ErrTurnNotFound        ErrorCode = "TURN_NOT_FOUND"
	ErrInteractionNotFound ErrorCode = "INTERACTION_NOT_FOUND"
	ErrRunInterrupted      ErrorCode = "RUN_INTERRUPTED"
	ErrSummaryFailed       ErrorCode = "SUMMARY_FAILED"
)

type HarnessError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Err     error     `json:"-"`
}

func (e *HarnessError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *HarnessError) Unwrap() error {
	return e.Err
}

func NewError(code ErrorCode, msg string) *HarnessError {
	return &HarnessError{Code: code, Message: msg}
}

func WrapError(code ErrorCode, msg string, err error) *HarnessError {
	return &HarnessError{Code: code, Message: msg, Err: err}
}
