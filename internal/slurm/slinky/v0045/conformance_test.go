package v0045_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Exonical/custos/internal/secrets"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/conformance"
	v0045 "github.com/Exonical/custos/internal/slurm/slinky/v0045"
)

// stub serves testdata fixtures keyed by URL path. It also records the
// last-seen auth headers so tests can assert the request editor ran.
type stub struct {
	srv    *httptest.Server
	mux    map[string]string // path suffix -> fixture filename
	status int
}

func newStub(t *testing.T, name string) *stub {
	t.Helper()
	s := &stub{status: http.StatusOK, mux: map[string]string{
		"/slurm/v0.0.45/ping/":           "ping.json",
		"/slurm/v0.0.45/partitions/":     "partitions.json",
		"/slurm/v0.0.45/nodes/":          "nodes.json",
		"/slurm/v0.0.45/reservations/":   "reservations.json",
		"/slurm/v0.0.45/diag/":           "diag.json",
		"/slurmdb/v0.0.45/accounts/":     "accounts.json",
		"/slurmdb/v0.0.45/qos/":          "qos.json",
		"/slurmdb/v0.0.45/associations/": "associations.json",
	}}
	switch {
	case strings.HasSuffix(name, "_errors"):
		s.mux["/slurm/v0.0.45/ping/"] = "ping_errors.json"
	case strings.HasSuffix(name, "_unauthorized"):
		s.status = http.StatusUnauthorized
	case strings.HasSuffix(name, "_version_mismatch"):
		s.mux["/slurm/v0.0.45/nodes/"] = "wrong_version.json"
	case strings.HasSuffix(name, "_nil_heavy"):
		s.mux["/slurm/v0.0.45/nodes/"] = "nil_heavy.json"
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.status != http.StatusOK {
			w.WriteHeader(s.status)
			return
		}
		fx, ok := s.mux[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(filepath.Join("testdata", fx))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func open(t *testing.T) (slurm.Cluster, slurm.Accounting) {
	cred := slurm.Credential{
		UserName: "custos",
		Token:    secrets.NewValue([]byte("test-token")),
	}
	if strings.HasSuffix(t.Name(), "_unavailable") {
		// Nothing listens on 127.0.0.1:1 — a refusal, not a stub.
		c, err := v0045.New(slurm.Endpoint{BaseURL: "http://127.0.0.1:1"},
			cred, http.DefaultClient)
		if err != nil {
			t.Fatal(err)
		}
		return c, c
	}
	s := newStub(t, t.Name())
	c, err := v0045.New(slurm.Endpoint{BaseURL: s.srv.URL}, cred, s.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c, c
}

func TestConformance(t *testing.T) {
	conformance.Run(t, open)
}

// The request editor must attach the slurmrestd auth headers per call,
// reading the token at call time.
func TestAuthHeaders(t *testing.T) {
	var seenUser, seenTok string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenUser = r.Header.Get("X-SLURM-USER-NAME")
		seenTok = r.Header.Get("X-SLURM-USER-TOKEN")
		b, _ := os.ReadFile(filepath.Join("testdata", "ping.json"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	c, err := v0045.New(slurm.Endpoint{BaseURL: srv.URL}, slurm.Credential{
		UserName: "svc-custos",
		Token:    secrets.NewValue([]byte("jwt-abc")),
	}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if seenUser != "svc-custos" || seenTok != "jwt-abc" {
		t.Fatalf("headers: user=%q tok=%q", seenUser, seenTok)
	}
}
