package types

import "context"

type RunContext struct {
	RunID       string
	SessionID   string
	UserID      string
	AgentID     string
	TeamID      string
	WorkflowID  string
	ParentRunID string
	Metadata    map[string]any
}

type ctxKey struct{}

func WithRunContext(parent context.Context, runContext *RunContext) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithValue(parent, ctxKey{}, runContext)
}

func RunContextFrom(ctx context.Context) (*RunContext, bool) {
	if ctx == nil {
		return nil, false
	}
	runContext, ok := ctx.Value(ctxKey{}).(*RunContext)
	return runContext, ok && runContext != nil
}
