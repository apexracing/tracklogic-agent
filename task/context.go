package task

import "context"

type runtimeContextKey struct{}

func WithRuntime(parent context.Context, runtime Runtime) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithValue(parent, runtimeContextKey{}, runtime)
}

func RuntimeFrom(ctx context.Context) (Runtime, bool) {
	if ctx == nil {
		return nil, false
	}
	runtime, ok := ctx.Value(runtimeContextKey{}).(Runtime)
	return runtime, ok && runtime != nil
}
