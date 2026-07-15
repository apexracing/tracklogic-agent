package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/apexracing/tracklogic-agent/types"
)

const (
	maxModelResponseBytes = 16 << 20
	maxModelErrorBytes    = 8 << 10
)

// HTTPError preserves the machine-readable part of an upstream model error.
// Callers can use errors.As and Retryable to implement an application-owned
// retry policy without parsing error strings.
type HTTPError struct {
	Provider   string
	StatusCode int
	RequestID  string
	RetryAfter string
	Body       string
}

func (e *HTTPError) Error() string {
	message := fmt.Sprintf("%s API returned HTTP %d", e.Provider, e.StatusCode)
	if e.RequestID != "" {
		message += " (request_id=" + e.RequestID + ")"
	}
	if e.Body != "" {
		message += ": " + e.Body
	}
	return message
}

func (e *HTTPError) Retryable() bool {
	if e == nil {
		return false
	}
	switch e.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func modelHTTPError(provider string, response *http.Response) error {
	body, readErr := readLimited(response.Body, maxModelErrorBytes)
	if readErr != nil {
		body = []byte("failed to read error response: " + readErr.Error())
	}
	requestID := response.Header.Get("x-request-id")
	if requestID == "" {
		requestID = response.Header.Get("request-id")
	}
	upstream := &HTTPError{
		Provider: provider, StatusCode: response.StatusCode,
		RequestID: requestID, RetryAfter: response.Header.Get("Retry-After"),
		Body: strings.TrimSpace(string(body)),
	}
	code := types.ErrAPIError
	if response.StatusCode == http.StatusTooManyRequests {
		code = types.ErrRateLimit
	}
	return types.WrapError(code, "model API request failed", upstream)
}

func modelTransportError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return types.WrapError(types.ErrRunCancelled, "model request cancelled", err)
	case errors.Is(err, context.DeadlineExceeded):
		return types.WrapError(types.ErrModelTimeout, "model request timed out", err)
	default:
		return types.WrapError(types.ErrAPIError, "model HTTP request failed", err)
	}
}

func decodeModelJSON(reader io.Reader, destination any) error {
	body, err := readLimited(reader, maxModelResponseBytes)
	if err != nil {
		return types.WrapError(types.ErrAPIError, "read model response", err)
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return types.WrapError(types.ErrAPIError, "decode model response", err)
	}
	return nil
}

func readLimited(reader io.Reader, maximum int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maximum {
		return nil, fmt.Errorf("response exceeds %d bytes", maximum)
	}
	return body, nil
}

func configuredHTTPClient(client *http.Client, timeout time.Duration) *http.Client {
	// Clone caller-owned clients before applying a default timeout.
	if client == nil {
		return &http.Client{Timeout: timeout}
	}
	clone := *client
	if clone.Timeout == 0 {
		clone.Timeout = timeout
	}
	return &clone
}

func applyCustomHeaders(request *http.Request, headers http.Header) {
	for name, values := range headers {
		request.Header.Del(name)
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
}

func validateInvokeRequest(request *InvokeRequest) error {
	if request == nil {
		return types.NewError(types.ErrInvalidInput, "model invoke request is required")
	}
	return nil
}

func emitResponseChunk(ctx context.Context, channel chan<- ResponseChunk, chunk ResponseChunk) bool {
	select {
	case channel <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}

func modelStreamError(message string, err error) error {
	return types.WrapError(types.ErrAPIError, message, err)
}
