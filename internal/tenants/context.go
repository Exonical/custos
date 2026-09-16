package tenants

import "context"

type ctxKey struct{}

// WithTenantContext attaches tc to ctx.
func WithTenantContext(ctx context.Context, tc TenantContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, tc)
}

// TenantContextFrom returns the TenantContext on ctx, if any.
func TenantContextFrom(ctx context.Context) (TenantContext, bool) {
	tc, ok := ctx.Value(ctxKey{}).(TenantContext)
	return tc, ok
}

// MustTenantContext returns the TenantContext on ctx or panics — a
// programming error meaning a handler ran without Require upstream.
func MustTenantContext(ctx context.Context) TenantContext {
	tc, ok := TenantContextFrom(ctx)
	if !ok {
		panic("tenants: no tenant context")
	}
	return tc
}
