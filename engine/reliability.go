package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/task"
	"github.com/apexracing/tracklogic-agent/types"
)

type circuitPhase uint8

const (
	circuitClosed circuitPhase = iota
	circuitOpen
	circuitHalfOpen
)

type circuitState struct {
	phase       circuitPhase
	failures    int
	openedAt    time.Time
	halfOpenRun int
}

type circuitOutcome uint8

const (
	circuitNeutral circuitOutcome = iota
	circuitSuccess
	circuitFailure
)

// ReliabilityManager holds process-local circuit state. Harness shares one
// manager across its Agents so the boundary is a Model instance, not an Agent.
type ReliabilityManager struct {
	mu       sync.Mutex
	circuits map[string]*circuitState
	now      func() time.Time
}

func NewReliabilityManager() *ReliabilityManager {
	return &ReliabilityManager{circuits: make(map[string]*circuitState), now: time.Now}
}

func (manager *ReliabilityManager) allow(runModel model.Model, config task.CircuitConfig) (func(circuitOutcome), error) {
	if manager == nil {
		return func(circuitOutcome) {}, nil
	}
	key := modelInstanceKey(runModel)
	manager.mu.Lock()
	state := manager.circuits[key]
	if state == nil {
		state = &circuitState{}
		manager.circuits[key] = state
	}
	now := manager.now()
	if state.phase == circuitOpen {
		if now.Sub(state.openedAt) < config.OpenDuration {
			manager.mu.Unlock()
			return nil, types.NewError(types.ErrCircuitOpen, "model circuit is open")
		}
		state.phase = circuitHalfOpen
		state.halfOpenRun = 0
	}
	if state.phase == circuitHalfOpen {
		if state.halfOpenRun >= config.HalfOpenMax {
			manager.mu.Unlock()
			return nil, types.NewError(types.ErrCircuitOpen, "model circuit probe is already running")
		}
		state.halfOpenRun++
	}
	phase := state.phase
	manager.mu.Unlock()

	var once sync.Once
	return func(outcome circuitOutcome) {
		once.Do(func() {
			manager.mu.Lock()
			defer manager.mu.Unlock()
			if phase == circuitHalfOpen && state.halfOpenRun > 0 {
				state.halfOpenRun--
			}
			if outcome == circuitNeutral {
				return
			}
			if outcome == circuitSuccess {
				state.phase = circuitClosed
				state.failures = 0
				state.openedAt = time.Time{}
				return
			}
			state.failures++
			if phase == circuitHalfOpen || state.failures >= config.FailureThreshold {
				state.phase = circuitOpen
				state.openedAt = manager.now()
			}
		})
	}, nil
}

func modelInstanceKey(runModel model.Model) string {
	value := reflect.ValueOf(runModel)
	if value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Map || value.Kind() == reflect.Func || value.Kind() == reflect.Slice || value.Kind() == reflect.Chan) && !value.IsNil() {
		return fmt.Sprintf("%T:%x", runModel, value.Pointer())
	}
	return fmt.Sprintf("%T:%s:%s", runModel, runModel.Provider(), runModel.ModelID())
}

func invokeModelWithTaskPolicy(ctx context.Context, manager *ReliabilityManager, runtime task.Runtime, runModel model.Model, request *model.InvokeRequest, onChunk func(string)) (*model.InvokeResponse, error) {
	finish, err := manager.allow(runModel, runtime.CircuitPolicy())
	if err != nil {
		return nil, err
	}
	outcome := circuitNeutral
	defer func() { finish(outcome) }()

	config := runtime.RetryPolicy()
	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		_ = runtime.Emit(ctx, task.Event{Type: task.EventModelAttemptStarted, Delivery: task.DeliveryBestEffort, Payload: task.EventPayload{Attempt: attempt, MaxAttempts: config.MaxAttempts}})
		streamed := false
		wrappedChunk := onChunk
		if onChunk != nil {
			wrappedChunk = func(chunk string) {
				streamed = true
				onChunk(chunk)
			}
		}
		attemptRequest := *request
		if request.ReasoningDelta != nil {
			attemptRequest.ReasoningDelta = func(chunk string) {
				streamed = true
				request.ReasoningDelta(chunk)
			}
		}
		response, invokeErr := invokeModel(ctx, runModel, &attemptRequest, wrappedChunk)
		if invokeErr == nil {
			outcome = circuitSuccess
			return response, nil
		}
		if ctx.Err() != nil || !isRetryableModelError(invokeErr) || attempt == config.MaxAttempts {
			if isRetryableModelError(invokeErr) && ctx.Err() == nil {
				outcome = circuitFailure
			}
			return nil, invokeErr
		}
		if streamed {
			_ = runtime.Emit(ctx, task.Event{Type: task.EventContentReset, Delivery: task.DeliveryBestEffort})
		}
		delay := retryDelay(config, attempt, invokeErr)
		_ = runtime.Emit(ctx, task.Event{Type: task.EventModelRetryWaiting, Delivery: task.DeliveryBestEffort, Payload: task.EventPayload{Attempt: attempt, MaxAttempts: config.MaxAttempts, NextAttempt: attempt + 1, DelayMS: delay.Milliseconds()}})
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
	outcome = circuitFailure
	return nil, types.NewError(types.ErrAPIError, "model retry budget exhausted")
}

func isRetryableModelError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var upstream *model.HTTPError
	if errors.As(err, &upstream) {
		return upstream.Retryable()
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func retryDelay(config task.RetryConfig, failedAttempt int, err error) time.Duration {
	if retryAfter, ok := retryAfterDelay(err); ok {
		if retryAfter > config.MaxRetryAfter {
			return config.MaxRetryAfter
		}
		return retryAfter
	}
	maximum := config.BaseDelay
	for index := 1; index < failedAttempt && maximum < config.MaxDelay; index++ {
		if maximum > config.MaxDelay/2 {
			maximum = config.MaxDelay
			break
		}
		maximum *= 2
	}
	if maximum > config.MaxDelay {
		maximum = config.MaxDelay
	}
	if maximum <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(maximum) + 1))
}

func retryAfterDelay(err error) (time.Duration, bool) {
	var upstream *model.HTTPError
	if !errors.As(err, &upstream) || strings.TrimSpace(upstream.RetryAfter) == "" {
		return 0, false
	}
	value := strings.TrimSpace(upstream.RetryAfter)
	if seconds, parseErr := strconv.Atoi(value); parseErr == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	when, parseErr := http.ParseTime(value)
	if parseErr != nil {
		return 0, false
	}
	delay := time.Until(when)
	if delay < 0 {
		delay = 0
	}
	return delay, true
}
