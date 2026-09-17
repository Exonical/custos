package projects

import "context"

type ctxKey struct{}

// WithProjectContext attaches pc to ctx.
func WithProjectContext(ctx context.Context, pc ProjectContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, pc)
}

// ProjectContextFrom returns the ProjectContext on ctx, if any.
func ProjectContextFrom(ctx context.Context) (ProjectContext, bool) {
	pc, ok := ctx.Value(ctxKey{}).(ProjectContext)
	return pc, ok
}

// MustProjectContext returns the ProjectContext on ctx or panics — a
// programming error meaning a handler ran without Require upstream.
func MustProjectContext(ctx context.Context) ProjectContext {
	pc, ok := ProjectContextFrom(ctx)
	if !ok {
		panic("projects: no project context")
	}
	return pc
}
