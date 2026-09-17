// Package pipeline is the durable validation pipeline of
// docs/script-validation.md §Pipeline: it runs every validator
// applicable to a language concurrently under bounded timeouts, merges
// and sorts diagnostics, applies the effective ValidationPolicy, and
// returns a ScriptValidation for persistence. Validators that fail
// (error, timeout, panic) yield a synthetic ERROR CUSTOS900 — the
// pipeline fails closed and never aborts a run.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/validation"
)

// Defaults per docs/script-validation.md §API.
const (
	DefaultPerValidatorTimeout = 10 * time.Second
	DefaultTotalBudget         = 20 * time.Second
	DefaultTTL                 = 30 * 24 * time.Hour

	cacheTTL     = 10 * time.Minute
	cacheMaxSize = 1024
)

// Pipeline runs the validator set.
type Pipeline struct {
	Validators          []validation.ScriptValidator
	PerValidatorTimeout time.Duration // default 10s
	TotalBudget         time.Duration // default 20s
	Now                 func() time.Time
	TTL                 time.Duration // default 30d -> ExpiresAt
	// UnavailableSeverity is the CUSTOS900 severity for EXTERNAL
	// validators only (those implementing External() bool); default
	// ERROR. An in-process validator failing closed is always ERROR —
	// a dev-mode downgrade must never let an unscanned script through.
	UnavailableSeverity validation.Severity

	mu    sync.Mutex
	cache map[cacheKey]cacheEntry
	order []cacheKey // FIFO eviction order
}

// Request is one pipeline run.
type Request struct {
	In                validation.Input // Policy field is the effective policy
	PolicyVersion     int64            // fingerprint from validation/policy
	WorkflowVersionID *uuid.UUID
	TaskName          string
}

// New returns a Pipeline with defaults applied.
func New(validators []validation.ScriptValidator) *Pipeline {
	return &Pipeline{Validators: validators}
}

func (p *Pipeline) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now().UTC()
}

func (p *Pipeline) perValidator() time.Duration {
	if p.PerValidatorTimeout > 0 {
		return p.PerValidatorTimeout
	}
	return DefaultPerValidatorTimeout
}

func (p *Pipeline) budget() time.Duration {
	if p.TotalBudget > 0 {
		return p.TotalBudget
	}
	return DefaultTotalBudget
}

func (p *Pipeline) ttl() time.Duration {
	if p.TTL > 0 {
		return p.TTL
	}
	return DefaultTTL
}

// cacheKey identifies a run by content, not request: language and the
// cluster snapshot participate so identical bytes under a different
// language or cluster cannot share a result. On a hit the pipeline
// returns the cached diagnostics verbatim but mints a fresh
// ScriptValidation (new ID; the request's WorkflowVersionID/TaskName)
// so task-scoped runs persist their own row.
type cacheKey struct {
	tenant        uuid.UUID
	digest        validation.Digest
	policyVersion int64
	configHash    uint64
}

type cacheEntry struct {
	sv      validation.ScriptValidation
	stamp   time.Time
	expires time.Time
}

func configHash(in validation.Input) uint64 {
	// Canonical JSON: encoding/json sorts map keys.
	b, _ := json.Marshal(struct {
		L string            `json:"l"`
		R any               `json:"r"`
		E map[string]string `json:"e"`
		S any               `json:"s"`
		C any               `json:"c"`
	}{string(in.Language), in.Resources, in.Environment, in.Software, in.Cluster})
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}

func (p *Pipeline) cached(tenantID uuid.UUID, req Request) (validation.ScriptValidation, bool) {
	key := cacheKey{tenantID, req.In.Digest, req.PolicyVersion, configHash(req.In)}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.cache[key]
	if !ok || !now.Before(e.expires) {
		return validation.ScriptValidation{}, false
	}
	sv := e.sv
	sv.ID = uuid.Must(uuid.NewV7())
	sv.WorkflowVersionID = req.WorkflowVersionID
	sv.TaskName = req.TaskName
	return sv, true
}

func (p *Pipeline) store(tenantID uuid.UUID, req Request, sv validation.ScriptValidation) {
	key := cacheKey{tenantID, req.In.Digest, req.PolicyVersion, configHash(req.In)}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = make(map[cacheKey]cacheEntry, 64)
	}
	if _, ok := p.cache[key]; !ok {
		p.order = append(p.order, key)
	}
	p.cache[key] = cacheEntry{sv: sv, stamp: now, expires: now.Add(cacheTTL)}
	for len(p.order) > cacheMaxSize {
		delete(p.cache, p.order[0])
		p.order = p.order[1:]
	}
}

// applicable returns the validators that handle the input language; a
// validator with no Languages applies to every language (envcheck).
func (p *Pipeline) applicable(lang validation.Language) []validation.ScriptValidator {
	var out []validation.ScriptValidator
	for _, v := range p.Validators {
		langs := v.Languages()
		if len(langs) == 0 {
			out = append(out, v)
			continue
		}
		for _, l := range langs {
			if l == lang {
				out = append(out, v)
				break
			}
		}
	}
	return out
}

// externalValidator marks validators that execute outside the process
// (the ShellCheck sidecar); only their failures may be downgraded by
// UnavailableSeverity.
type externalValidator interface{ External() bool }

// runOne executes a validator under its own timeout with panic
// recovery; any failure yields a synthetic CUSTOS900 diagnostic.
func (p *Pipeline) runOne(ctx context.Context, v validation.ScriptValidator,
	in validation.Input) (res validation.Result) {
	defer func() {
		if r := recover(); r != nil {
			res = p.validatorDown(v)
		}
	}()
	res, err := v.Validate(ctx, in)
	if err != nil {
		return p.validatorDown(v)
	}
	return res
}

func (p *Pipeline) validatorDown(v validation.ScriptValidator) validation.Result {
	sev := validation.SeverityError
	if ev, ok := v.(externalValidator); ok && ev.External() &&
		p.UnavailableSeverity != "" {
		sev = p.UnavailableSeverity
	}
	return validation.Result{
		Tool: validation.ToolVersion{Name: v.Name(), Version: "unavailable"},
		Diagnostics: []validation.Diagnostic{{
			Source:   v.Name(),
			Code:     "CUSTOS900",
			Severity: sev,
			Message:  fmt.Sprintf("validator %s unavailable", v.Name()),
		}},
	}
}

// Run executes the pipeline and returns the ScriptValidation. The
// caller persists it; nothing is logged or persisted here.
func (p *Pipeline) Run(ctx context.Context, tenantID uuid.UUID,
	req Request) validation.ScriptValidation {
	if sv, ok := p.cached(tenantID, req); ok {
		return sv
	}
	now := p.now()

	ctx, cancel := context.WithTimeout(ctx, p.budget())
	defer cancel()

	validators := p.applicable(req.In.Language)
	results := make([]validation.Result, len(validators))
	var wg sync.WaitGroup
	for i, v := range validators {
		wg.Add(1)
		go func(i int, v validation.ScriptValidator) {
			defer wg.Done()
			vctx, vcancel := context.WithTimeout(ctx, p.perValidator())
			defer vcancel()
			results[i] = p.runOne(vctx, v, req.In)
		}(i, v)
	}
	wg.Wait()

	var diags []validation.Diagnostic
	tools := make(map[string]string, len(results))
	for _, r := range results {
		diags = append(diags, r.Diagnostics...)
		if r.Tool.Name != "" {
			tools[r.Tool.Name] = r.Tool.Version
		}
	}
	validation.SortDiagnostics(diags)
	diags, valid := validation.ApplyPolicy(diags, req.In.Policy)

	sv := validation.ScriptValidation{
		ID:                uuid.Must(uuid.NewV7()),
		TenantID:          tenantID,
		WorkflowVersionID: req.WorkflowVersionID,
		TaskName:          req.TaskName,
		ScriptDigest:      req.In.Digest,
		Language:          req.In.Language,
		Valid:             valid,
		Diagnostics:       diags,
		ToolVersions:      tools,
		PolicyVersion:     req.PolicyVersion,
		ValidatedAt:       now,
		ExpiresAt:         now.Add(p.ttl()),
	}
	p.store(tenantID, req, sv)
	return sv
}
