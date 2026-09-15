// Package health implements /health/live and /health/ready with required
// and optional (degradable) checkers. Checker error strings are logged,
// never returned in the response body — they may contain DSNs.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"
)

// Checker is a named health check.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// CheckerFunc adapts a function to Checker.
func CheckerFunc(name string, fn func(ctx context.Context) error) Checker {
	return checkFunc{name: name, fn: fn}
}

type checkFunc struct {
	name string
	fn   func(ctx context.Context) error
}

func (c checkFunc) Name() string                    { return c.name }
func (c checkFunc) Check(ctx context.Context) error { return c.fn(ctx) }

type registered struct {
	c        Checker
	required bool
}

// Registry holds the health checkers.
type Registry struct {
	mu     sync.Mutex
	checks []registered
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Register adds a checker; required checkers gate readiness.
func (r *Registry) Register(c Checker, required bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, registered{c: c, required: required})
}

// LiveHandler always answers 200 {"status":"ok"} — liveness is "the
// process is alive and its event loop responsive", never dependency state.
func (r *Registry) LiveHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

type checkResult struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // "ok" | "fail"
	LatencyMS int64  `json:"latency_ms"`
	required  bool
	err       error
}

// ReadyHandler runs all checkers concurrently, each bounded by
// perCheckBudget. 200 when all required pass ("degraded" when only
// optional ones fail); 503 otherwise.
func (r *Registry) ReadyHandler(perCheckBudget time.Duration, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		checks := slices.Clone(r.checks)
		r.mu.Unlock()

		results := make([]checkResult, len(checks))
		var wg sync.WaitGroup
		for i, reg := range checks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				ctx, cancel := context.WithTimeout(req.Context(), perCheckBudget)
				defer cancel()
				err := reg.c.Check(ctx)
				res := checkResult{
					Name:      reg.c.Name(),
					Status:    "ok",
					LatencyMS: time.Since(start).Milliseconds(),
					required:  reg.required,
					err:       err,
				}
				if err != nil {
					res.Status = "fail"
					logger.WarnContext(req.Context(), "health check failed",
						"check", reg.c.Name(), "required", reg.required,
						"error", err.Error())
				}
				results[i] = res
			}()
		}
		wg.Wait()

		status, httpStatus := "ok", http.StatusOK
		for _, res := range results {
			if res.Status == "fail" {
				if res.required {
					status, httpStatus = "unavailable", http.StatusServiceUnavailable
				} else if status == "ok" {
					status = "degraded"
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatus)
		_ = json.NewEncoder(w).Encode(struct {
			Status string        `json:"status"`
			Checks []checkResult `json:"checks"`
		}{Status: status, Checks: results})
	})
}
