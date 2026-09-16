package authn

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/httpx"
)

// BearerOption customizes RequireBearer.
type BearerOption func(*bearerCfg)

type bearerCfg struct {
	prov Provisioner
}

// WithProvisioner runs prov after successful verification and attaches
// the enriched principal. Provisioning failures fail closed with 503
// PROVISIONING_UNAVAILABLE.
func WithProvisioner(prov Provisioner) BearerOption {
	return func(c *bearerCfg) { c.prov = prov }
}

// RequireBearer authenticates `Authorization: Bearer <token>` with v and
// stores the Principal on the request context. Rejections produce a 401
// envelope plus `WWW-Authenticate: Bearer error="invalid_token"`, are
// logged at warn, and are recorded as `auth.token_rejected` audit events
// (reason code + client ip only — never the token, never the subject).
func RequireBearer(v Verifier, rec audit.Recorder, logger *slog.Logger, opts ...BearerOption) httpx.Middleware {
	var cfg bearerCfg
	for _, o := range opts {
		o(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			raw, err := bearerToken(r)
			if err == nil {
				var p Principal
				p, err = v.Verify(ctx, raw)
				if err == nil && cfg.prov != nil {
					p, err = cfg.prov.Provision(ctx, p)
					if err != nil {
						logger.ErrorContext(ctx, "provisioning failed",
							"error", err)
						httpx.WriteError(ctx, w, apperr.New(apperr.Unavailable,
							"PROVISIONING_UNAVAILABLE", "identity provisioning unavailable"))
						return
					}
				}
				if err == nil {
					next.ServeHTTP(w, r.WithContext(WithPrincipal(ctx, p)))
					return
				}
			}
			code := "TOKEN_MALFORMED"
			var ae *apperr.Error
			if errors.As(err, &ae) && ae.Code != "" {
				code = ae.Code
			}
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			logger.WarnContext(ctx, "bearer token rejected",
				"code", code, "client_ip", httpx.ClientIPFrom(ctx))
			if rec != nil {
				if rerr := rec.Record(ctx, audit.Event{
					Actor:  audit.Actor{Type: audit.ActorSystem, ID: "custos"},
					Action: "auth.token_rejected",
					Result: audit.ResultDeny,
					Details: map[string]any{
						"reason":    code,
						"client_ip": httpx.ClientIPFrom(ctx),
					},
				}); rerr != nil {
					logger.ErrorContext(ctx, "audit auth.token_rejected failed", "error", rerr)
				}
			}
			httpx.WriteError(ctx, w, errUnauthenticated(code, "invalid bearer token"))
		})
	}
}

// bearerToken extracts exactly one bearer token; anything else is
// missing/malformed.
func bearerToken(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errUnauthenticated("TOKEN_MISSING", "authorization header required")
	}
	scheme, rest, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", errUnauthenticated("TOKEN_MISSING", "bearer scheme required")
	}
	tok := strings.TrimSpace(rest)
	if tok == "" || strings.ContainsAny(tok, " \t") {
		return "", errUnauthenticated("TOKEN_MALFORMED", "malformed bearer token")
	}
	return tok, nil
}
