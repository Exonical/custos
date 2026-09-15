// Package api mounts the v1 HTTP routes on the ServeMux. Route
// registration lives here (not in cmd) so all commands mount the same
// surface.
package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	apiv1 "github.com/Exonical/custos/pkg/api/v1"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/health"
	"github.com/Exonical/custos/internal/platform/httpx"
)

// specJSON is computed once at init from the spec embedded in the
// generated package (single source of truth: api/openapi/v1.yaml).
var specJSON []byte
var specETag string

func init() {
	doc, err := apiv1.GetSpec()
	if err != nil {
		panic("api: embedded spec does not parse: " + err.Error())
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic("api: spec JSON encode: " + err.Error())
	}
	specJSON = b
	sum := sha256.Sum256(b)
	specETag = fmt.Sprintf("\"%x\"", sum[:8])
}

// Deps are the dependencies Mount needs.
type Deps struct {
	Health      *health.Registry
	ReadyBudget time.Duration
	Logger      *slog.Logger
}

// Mount registers the v1 routes on mux. The generated types in
// pkg/api/v1 describe the shapes; the health routes delegate to the
// health package handlers directly since they already emit the exact
// documented JSON.
func Mount(mux *http.ServeMux, deps Deps) {
	mux.Handle("GET /api/v1/openapi.json", openapiHandler())
	mux.Handle("GET /health/live", deps.Health.LiveHandler())
	mux.Handle("GET /health/ready",
		deps.Health.ReadyHandler(deps.ReadyBudget, deps.Logger))

	// ServeMux's built-in 404/405 responses are plain text; give unknown
	// paths the error envelope instead. (ServeMux method-mismatch 405s are
	// still plain text — documented limitation.)
	mux.Handle("/", notFound())
}

func openapiHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if match := r.Header.Get("If-None-Match"); match == specETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("ETag", specETag)
		h.Set("Cache-Control", "no-store")
		_, _ = w.Write(specJSON)
	})
}

func notFound() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(r.Context(), w,
			apperr.New(apperr.NotFound, "NOT_FOUND", "not found"))
	})
}

// SpecJSON exposes the JSON-encoded OpenAPI document (tests/tooling).
func SpecJSON() []byte { return specJSON }
