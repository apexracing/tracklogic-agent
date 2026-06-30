package types

type RunContext struct {
	RunID      string
	SessionID  string
	UserID     string
	AgentID    string
	TeamID     string
	WorkflowID string
	ParentRunID string
	Metadata   map[string]any
}

type ctxKey string

const runCtxKey ctxKey = "run_context"

func WithRunContext(parent any, ctx *RunContext) any {
	// Placeholder — in real usage this wraps context.Context
	return ctx
}

func GetRunContext(ctx any) *RunContext {
	if rc, ok := ctx.(*RunContext); ok {
		return rc
	}
	return nil
}
