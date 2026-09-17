package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

type stub struct {
	name     string
	langs    []workflowspec.Language
	diags    []validation.Diagnostic
	err      error
	delay    time.Duration
	panicMsg string
	version  string
}

func (s stub) Name() string { return s.name }
func (s stub) Languages() []workflowspec.Language {
	return s.langs
}
func (s stub) Validate(ctx context.Context, _ validation.Input) (validation.Result, error) {
	if s.panicMsg != "" {
		panic(s.panicMsg)
	}
	if s.delay > 0 {
		select {
		case <-ctx.Done():
			return validation.Result{}, ctx.Err()
		case <-time.After(s.delay):
		}
	}
	if s.err != nil {
		return validation.Result{}, s.err
	}
	return validation.Result{
		Diagnostics: s.diags,
		Tool:        validation.ToolVersion{Name: s.name, Version: s.version},
	}, nil
}

func req(body string) Request {
	return Request{
		In: validation.Input{
			Language: workflowspec.LanguageBash,
			Script:   []byte(body),
			Digest:   validation.DigestOf([]byte(body)),
			Policy:   validation.EffectivePolicy{BlockAt: validation.SeverityError},
		},
		PolicyVersion: 42,
	}
}

func TestRunMergesAndSorts(t *testing.T) {
	p := New([]validation.ScriptValidator{
		stub{name: "b", langs: []workflowspec.Language{workflowspec.LanguageBash},
			version: "2", diags: []validation.Diagnostic{
				{Source: "b", Code: "X2", Severity: validation.SeverityInfo, Line: 9},
			}},
		stub{name: "a", version: "1", diags: []validation.Diagnostic{
			{Source: "a", Code: "X1", Severity: validation.SeverityWarning, Line: 3},
		}},
	})
	sv := p.Run(context.Background(), uuid.New(), req("echo hi"))
	if !sv.Valid {
		t.Fatalf("expected valid: %+v", sv.Diagnostics)
	}
	if len(sv.Diagnostics) != 2 ||
		sv.Diagnostics[0].Code != "X1" || sv.Diagnostics[1].Code != "X2" {
		t.Fatalf("unsorted merge: %+v", sv.Diagnostics)
	}
	if sv.ToolVersions["a"] != "1" || sv.ToolVersions["b"] != "2" {
		t.Fatalf("tool versions: %+v", sv.ToolVersions)
	}
	if sv.PolicyVersion != 42 || sv.ExpiresAt.IsZero() {
		t.Fatalf("meta: %+v", sv)
	}
}

func TestRunValidatorErrorFailsClosed(t *testing.T) {
	p := New([]validation.ScriptValidator{
		stub{name: "bad", err: errors.New("boom")},
	})
	sv := p.Run(context.Background(), uuid.New(), req("x"))
	if sv.Valid {
		t.Fatal("validator error must fail closed")
	}
	if sv.Diagnostics[0].Code != "CUSTOS900" ||
		sv.Diagnostics[0].Severity != validation.SeverityError {
		t.Fatalf("want CUSTOS900 ERROR, got %+v", sv.Diagnostics[0])
	}
}

func TestRunTimeoutFailsClosed(t *testing.T) {
	p := New([]validation.ScriptValidator{
		stub{name: "slow", delay: time.Second},
	})
	p.PerValidatorTimeout = 20 * time.Millisecond
	sv := p.Run(context.Background(), uuid.New(), req("x"))
	if sv.Valid || sv.Diagnostics[0].Code != "CUSTOS900" {
		t.Fatalf("want CUSTOS900, got %+v", sv.Diagnostics)
	}
}

func TestRunPanicFailsClosed(t *testing.T) {
	p := New([]validation.ScriptValidator{
		stub{name: "crash", panicMsg: "segfault"},
	})
	sv := p.Run(context.Background(), uuid.New(), req("x"))
	if sv.Valid || sv.Diagnostics[0].Code != "CUSTOS900" {
		t.Fatalf("want CUSTOS900, got %+v", sv.Diagnostics)
	}
}

func TestCacheHit(t *testing.T) {
	calls := 0
	v := &counting{stub: stub{name: "a"}, n: &calls}
	p := New([]validation.ScriptValidator{v})
	tid := uuid.New()
	wv := uuid.New()
	r2 := req("echo hi")
	r2.WorkflowVersionID = &wv
	r2.TaskName = "task-a"
	sv1 := p.Run(context.Background(), tid, req("echo hi"))
	sv2 := p.Run(context.Background(), tid, r2)
	if calls != 1 {
		t.Fatalf("cache miss on identical request: %d calls", calls)
	}
	// A hit reuses the diagnostics but mints a fresh ScriptValidation
	// carrying the request's scope (each persisted row is its own run).
	if sv1.ID == sv2.ID {
		t.Fatal("cached run must mint a new ScriptValidation ID")
	}
	if sv2.WorkflowVersionID == nil || *sv2.WorkflowVersionID != wv ||
		sv2.TaskName != "task-a" {
		t.Fatalf("scope not applied on cache hit: %+v", sv2)
	}
	if sv1.Valid != sv2.Valid || sv1.PolicyVersion != sv2.PolicyVersion ||
		sv1.ValidatedAt != sv2.ValidatedAt {
		t.Fatal("cached diagnostics/policy not reused")
	}
	// Different tenant or policy version = different key.
	p.Run(context.Background(), uuid.New(), req("echo hi"))
	r3 := req("echo hi")
	r3.PolicyVersion = 43
	p.Run(context.Background(), tid, r3)
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

type counting struct {
	stub
	n *int
}

func (c *counting) Validate(ctx context.Context, in validation.Input) (validation.Result, error) {
	*c.n++
	return c.stub.Validate(ctx, in)
}

func TestPolicyFloorUntouched(t *testing.T) {
	p := New([]validation.ScriptValidator{
		stub{name: "s", diags: []validation.Diagnostic{
			{Source: "s", Code: "CUSTOS101", Severity: validation.SeveritySecurity},
		}},
	})
	r := req("x")
	r.In.Policy.DisabledCodes = []string{"CUSTOS101"}
	r.In.Policy.Overrides = []validation.Override{
		{Source: "s", Code: "CUSTOS101", Severity: validation.SeverityInfo},
	}
	sv := p.Run(context.Background(), uuid.New(), r)
	if sv.Valid {
		t.Fatal("SECURITY_VIOLATION must survive overrides/disables")
	}
	if sv.Diagnostics[0].Severity != validation.SeveritySecurity {
		t.Fatalf("severity downgraded: %+v", sv.Diagnostics[0])
	}
}

func TestLanguageFilter(t *testing.T) {
	p := New([]validation.ScriptValidator{
		stub{name: "bashonly", langs: []workflowspec.Language{workflowspec.LanguageBash},
			diags: []validation.Diagnostic{{Source: "bashonly", Code: "B1",
				Severity: validation.SeverityInfo}}},
		stub{name: "all"}, // nil langs = every language
	})
	sv := p.Run(context.Background(), uuid.New(), Request{
		In: validation.Input{Language: workflowspec.LanguagePython,
			Script: []byte("x"), Policy: validation.EffectivePolicy{BlockAt: validation.SeverityError}},
	})
	for _, d := range sv.Diagnostics {
		if d.Source == "bashonly" {
			t.Fatal("bash-only validator ran on python")
		}
	}
}

func TestCacheKeyIncludesLanguageAndCluster(t *testing.T) {
	calls := 0
	v := &counting{stub: stub{name: "a"}, n: &calls}
	p := New([]validation.ScriptValidator{v})
	tid := uuid.New()
	p.Run(context.Background(), tid, req("echo hi"))
	// Same bytes, different language -> new run.
	r := req("echo hi")
	r.In.Language = workflowspec.LanguageSh
	p.Run(context.Background(), tid, r)
	if calls != 2 {
		t.Fatalf("language not in cache key: %d calls", calls)
	}
	// Same bytes, different cluster snapshot -> new run.
	r2 := req("echo hi")
	r2.In.Cluster = &validation.ClusterSnapshot{Partitions: []string{"gpu"}}
	p.Run(context.Background(), tid, r2)
	r3 := req("echo hi")
	r3.In.Cluster = &validation.ClusterSnapshot{Partitions: []string{"cpu"}}
	p.Run(context.Background(), tid, r3)
	// Same bytes, same snapshot -> hit.
	r4 := req("echo hi")
	r4.In.Cluster = &validation.ClusterSnapshot{Partitions: []string{"gpu"}}
	p.Run(context.Background(), tid, r4)
	if calls != 4 {
		t.Fatalf("cluster snapshot not in cache key: %d calls", calls)
	}
}

type extStub struct{ stub }

func (extStub) External() bool { return true }

func TestUnavailableSeverityScope(t *testing.T) {
	// External validator failures honor the dev-mode downgrade.
	p := New([]validation.ScriptValidator{
		extStub{stub{name: "shellcheck", err: errors.New("sidecar down")}},
	})
	p.UnavailableSeverity = validation.SeverityWarning
	sv := p.Run(context.Background(), uuid.New(), req("x"))
	if sv.Diagnostics[0].Severity != validation.SeverityWarning {
		t.Fatalf("external downgrade: %+v", sv.Diagnostics[0])
	}
	// In-process validator failures stay ERROR even with a downgrade
	// configured — an unscanned script must never get through.
	p2 := New([]validation.ScriptValidator{
		stub{name: "sbatchscan", err: errors.New("boom")},
	})
	p2.UnavailableSeverity = validation.SeverityWarning
	sv2 := p2.Run(context.Background(), uuid.New(), req("x"))
	if sv2.Diagnostics[0].Severity != validation.SeverityError || sv2.Valid {
		t.Fatalf("in-process failure must stay ERROR: %+v", sv2.Diagnostics[0])
	}
}
