package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/clusters"
	"github.com/Exonical/custos/internal/executions"
	"github.com/Exonical/custos/internal/jobs"
	jobssvc "github.com/Exonical/custos/internal/jobs/service"
	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/platform/workqueue"
	"github.com/Exonical/custos/internal/tenants"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/validation/pipeline"
	"github.com/Exonical/custos/internal/workflows"
	"github.com/Exonical/custos/internal/workflowspec"
	"github.com/Exonical/custos/internal/workflowspec/expr"
	wfvalidate "github.com/Exonical/custos/internal/workflowspec/validate"
)

// Admit returns the task.admit handler. Item key: task:<id>.
// READY → ADMITTING → (validate) → (render) → (admission.Build) →
// SUBMITTING with the frozen ExecutionSpec and the jobs row + job.submit
// enqueue in one transaction. Transient failures return the task to
// READY and let the work item's backoff retry.
func Admit(d Deps) workqueue.Handler {
	return func(ctx context.Context, it workqueue.Item) error {
		var p struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(it.Payload, &p); err != nil {
			return workqueue.Perm(fmt.Errorf("%s payload: %w", it.Kind, err))
		}
		id, err := uuid.Parse(p.TaskID)
		if err != nil {
			return workqueue.Perm(fmt.Errorf("%s task_id: %w", it.Kind, err))
		}
		scope := tenants.PlatformScope()
		t, err := d.Execs.GetTask(ctx, scope, uuid.Nil, id)
		if err != nil {
			if apperr.Is(err, apperr.NotFound) {
				return nil
			}
			return err
		}
		if t.State != executions.TaskReady {
			return nil // already admitted, canceled, or superseded
		}
		to := executions.TaskAdmitting
		t, err = d.Execs.TransitionTask(ctx, scope, t.ID,
			executions.TaskReady, t.Version,
			executions.TaskPatch{State: &to}, nil)
		if err != nil {
			if executions.IsStale(err) {
				return nil
			}
			return err
		}

		err = admitTask(ctx, d, t)
		if err == nil {
			return nil
		}
		var de *denyError
		if asDeny(err, &de) {
			// Permanent failure: validation or admission denial.
			to := executions.TaskFailed
			reason := de.reason
			if _, terr := d.Execs.TransitionTask(ctx, scope, t.ID,
				executions.TaskAdmitting, t.Version,
				executions.TaskPatch{State: &to, Reason: &reason},
				func(ex executions.Execer) error {
					_, err := workqueue.Enqueue(ctx, ex,
						workqueue.EnqueueRequest{
							Kind:     KindAdvance,
							Key:      "execution:" + t.ExecutionID.String(),
							TenantID: &t.TenantID,
							Payload: map[string]string{
								"execution_id": t.ExecutionID.String()},
						})
					return err
				}); terr != nil && !executions.IsStale(terr) {
				return terr
			}
			d.record(ctx, audit.Event{
				Actor:  audit.Actor{Type: audit.ActorSystem, ID: "custos"},
				Action: "workflow.task.admit",
				Result: audit.ResultDeny,
				Reason: de.reason,
				Target: audit.Target{Type: "task_execution",
					ID: t.ID.String()},
				TenantID: &t.TenantID,
				Details:  de.details,
			})
			return nil
		}
		// Transient: back to READY; the work item retry re-admits.
		to = executions.TaskReady
		reason := "TRANSIENT"
		if _, terr := d.Execs.TransitionTask(ctx, scope, t.ID,
			executions.TaskAdmitting, t.Version,
			executions.TaskPatch{State: &to, Reason: &reason},
			nil); terr != nil && !executions.IsStale(terr) {
			return terr
		}
		return err
	}
}

// denyError marks a permanent admit failure (validation/admission).
type denyError struct {
	reason  string
	details map[string]any
}

func (e *denyError) Error() string { return e.reason }

func denyFail(reason, field, msg string) *denyError {
	return &denyError{reason: reason, details: map[string]any{
		"stage": reason, "field": field, "message": msg}}
}

func asDeny(err error, out **denyError) bool {
	if de, ok := err.(*denyError); ok {
		*out = de
		return true
	}
	return false
}

// admitTask performs the admit pipeline for one ADMITTING task;
// returns a *denyError for permanent failures.
func admitTask(ctx context.Context, d Deps,
	t executions.TaskExecution) error {
	scope := tenants.PlatformScope()
	e, err := d.Execs.Get(ctx, scope, uuid.Nil, t.ExecutionID)
	if err != nil {
		return err
	}
	wf, v, spec, err := d.loadSpec(ctx, e)
	if err != nil {
		return err
	}
	st := findTaskSpec(spec, t.TaskName)
	if st == nil {
		return denyFail("SPEC_TAMPERED", "task",
			"task is not in the pinned spec")
	}
	params, perrs := resolveParams(spec, e)
	if len(perrs) > 0 {
		return denyFail("PARAMETERS_INVALID", "parameters",
			perrs[0].Message)
	}
	res, resErrs := st.Resources.Resolve("")
	if len(resErrs) > 0 {
		return denyFail("RESOURCES_INVALID", "resources",
			resErrs[0].Message)
	}
	sc := admitScope{
		spec: spec, workflow: wf.Name, version: v.Number,
		exec: e, params: params, self: &t, res: res,
	}
	allTasks, err := d.Execs.ListTasks(ctx, scope, e.TenantID, e.ID)
	if err != nil {
		return err
	}
	sc.tasks = allTasks

	// Render argv/env/paths at admit time (templates over the admit
	// scope; {{ array.taskId }} only as a whole element).
	argv, err := renderArgv(append(
		append([]string{}, st.Command...), st.Args...), sc)
	if err != nil {
		return denyFail("TEMPLATE", "argv", err.Error())
	}
	secretInfo := map[string]wfvalidate.SecretReferenceInfo{}
	for handle, use := range spec.Spec.Secrets {
		if d.SecretReference == nil {
			return denyFail("SECRET_REFERENCE_NOT_FOUND", "secrets", "secret lookup unavailable")
		}
		info, ok := d.SecretReference(ctx, e.TenantID, use.Ref)
		if !ok {
			return denyFail("SECRET_REFERENCE_NOT_FOUND", "secrets."+handle, "secret reference not found")
		}
		secretInfo[handle] = info
	}
	env, err := renderEnv(st.Env, sc, spec.Spec.Secrets, secretInfo)
	if err != nil {
		return denyFail("TEMPLATE", "env", err.Error())
	}
	var wrapped []string
	for handle, use := range spec.Spec.Secrets {
		if use.Use != "wrapped_token" {
			continue
		}
		id, err := uuid.Parse(secretInfo[handle].ID)
		if err != nil {
			return denyFail("SECRET_REFERENCE_NOT_FOUND", "secrets."+handle, "secret reference id invalid")
		}
		env.SecretRefs = append(env.SecretRefs, admission.SecretEnvRef{
			Name:        "CUSTOS_SECRET_" + workflowspec.SecretEnvName(handle, use) + "_WRAP_TOKEN",
			ReferenceID: id, Mode: use.Use, Handle: handle})
		wrapped = append(wrapped, handle)
	}
	slices.Sort(wrapped)
	slices.SortFunc(env.SecretRefs, func(a, b admission.SecretEnvRef) int {
		return strings.Compare(a.Handle, b.Handle)
	})
	literal := func(src, field string) (string, error) {
		return renderLiteral(src, sc, field)
	}
	workDir, err := literal(st.WorkingDirectory, "workingDirectory")
	if err != nil {
		return denyFail("TEMPLATE", "workingDirectory", err.Error())
	}
	stdout, err := literal(st.Stdout, "stdout")
	if err != nil {
		return denyFail("TEMPLATE", "stdout", err.Error())
	}
	stderr, err := literal(st.Stderr, "stderr")
	if err != nil {
		return denyFail("TEMPLATE", "stderr", err.Error())
	}

	// Validation: script tasks need a current valid ScriptValidation.
	var (
		svID    *uuid.UUID
		in      validation.Input
		cluster *clusters.Cluster
	)
	if st.Script != nil {
		var c *clusters.Cluster
		in, c, err = workflows.TaskSnapshot(ctx, scope, d.Scripts,
			d.Clusters, e.TenantID, spec, *st)
		if err != nil {
			return err
		}
		cluster = c
		var cid *uuid.UUID
		if cluster != nil {
			cid = &cluster.ID
		}
		epol, efp, err := d.VPolicy.Effective(ctx, scope, e.TenantID, cid)
		if err != nil {
			return err
		}
		in.Policy = epol.Effective()
		sv, ok := d.currentValidation(ctx, scope, e, v, t, in, efp)
		if !ok {
			sv, err = d.runValidation(ctx, scope, e, v, t, in, efp)
			if err != nil {
				return err
			}
		}
		if !sv.Valid {
			return denyFail("VALIDATION_FAILED", "script",
				firstBlocking(sv, epol.BlockAt))
		}
		svID = &sv.ID
	} else {
		cluster, err = placementCluster(ctx, d, spec, *st)
		if err != nil {
			return err
		}
	}
	if cluster == nil {
		return denyFail("CLUSTER_UNKNOWN", "placement",
			"task has no resolvable placement cluster")
	}
	if cluster.Capabilities == nil ||
		cluster.State == clusters.StateDisabled {
		return apperr.New(apperr.Conflict, "CLUSTER_UNAVAILABLE",
			"cluster capabilities are unavailable") // transient
	}
	binding, err := d.Projects.ResolveBinding(ctx, scope, e.ProjectID,
		cluster.ID)
	if err != nil {
		return denyFail("ADMISSION_DENIED", "placement",
			"project has no enabled binding to this cluster")
	}
	pol, err := d.Policies.Effective(ctx, scope, e.TenantID, e.ProjectID)
	if err != nil {
		return err
	}

	jobID := uuid.Must(uuid.NewV7())
	espec := admission.ExecutionSpec{
		ID: jobID, TenantID: e.TenantID, ProjectID: e.ProjectID,
		PrincipalID:       e.RequestedBy,
		WorkflowVersionID: e.WorkflowVersionID,
		TaskName:          t.TaskName,
		Attempt:           t.Attempt,
		Cluster: admission.ClusterRef{
			ID: cluster.ID, Name: cluster.Name,
			APIVersion: cluster.APIVersion,
		},
		Partition:   st.Partition,
		QoS:         st.QoS,
		Environment: env,
		Argv:        argv,
		WorkingDir:  workDir,
		Stdout:      stdout,
		Stderr:      stderr,
		Security: admission.SecurityContext{
			SlurmUser:         cluster.ServiceUser,
			ImpersonationMode: string(cluster.IdentityMode),
			ShellTask:         st.Type == "shell",
			WrappedTokenRefs:  wrapped,
		},
	}
	if st.Script != nil {
		interp, err := interpreterFor(in.Language)
		if err != nil {
			return denyFail("LANGUAGE_UNSUPPORTED", "script",
				err.Error())
		}
		espec.Payload = admission.PayloadRef{
			ScriptID:    jobID,
			Digest:      in.Digest,
			Language:    in.Language,
			Interpreter: interp,
		}
	}
	built, denial := admission.Build(admission.BuildInput{
		Spec: espec, Request: res, Policy: pol,
		Binding: binding, Cluster: workflows.ClusterSnapshot(cluster),
	})
	if denial != nil {
		return denyFail("ADMISSION_DENIED", denial.Field,
			denial.Code+": "+denial.Message)
	}
	built.AdmittedAt = d.now()

	now := d.now()
	j := jobs.Job{
		ID: jobID, TenantID: e.TenantID, ProjectID: e.ProjectID,
		ClusterID: cluster.ID, CreatedBy: e.RequestedBy,
		Name:            jobName(wf.Name, t),
		State:           jobs.StateSubmitting,
		ResourceRequest: res,
		ExecutionSpec:   built, ExecutionSpecDigest: built.Digest,
		ScriptLanguage:     in.Language,
		ScriptValidationID: svID,
		TaskExecutionID:    &t.ID,
		Version:            1, CreatedAt: now, UpdatedAt: now,
	}
	if st.Script != nil {
		j.ScriptDigest = in.Digest
	}
	to := executions.TaskSubmitting
	specDigest := built.Digest
	_, err = d.Execs.AdmitTask(ctx, scope, t.ID,
		executions.TaskAdmitting, t.Version, executions.TaskPatch{
			State: &to, JobID: &jobID, Spec: &built,
			SpecDigest: &specDigest, ValidationID: svID,
		}, j, func(ex executions.Execer) error {
			_, err := workqueue.Enqueue(ctx, ex,
				workqueue.EnqueueRequest{
					Kind:     jobssvc.KindSubmit,
					Key:      "job:" + jobID.String(),
					TenantID: &e.TenantID,
					Payload: map[string]string{
						"job_id": jobID.String()},
					MaxAttempts: 8,
				})
			return err
		})
	if err != nil {
		if executions.IsStale(err) {
			return nil
		}
		return err
	}
	d.record(ctx, audit.Event{
		Actor:    audit.Actor{Type: audit.ActorSystem, ID: "custos"},
		Action:   "workflow.task.admit",
		Result:   audit.ResultAllow,
		Target:   audit.Target{Type: "task_execution", ID: t.ID.String()},
		TenantID: &t.TenantID,
		Details: map[string]any{
			"spec_digest":   built.Digest.String(),
			"script_digest": j.ScriptDigest.String(),
			"job_id":        jobID.String(),
			"cluster":       cluster.Name,
		},
	})
	return nil
}

// jobName builds a deterministic Custos-controlled job name.
func jobName(wfName string, t executions.TaskExecution) string {
	name := wfName + "-" + t.TaskName
	if t.Count > 1 {
		name += fmt.Sprintf("-%d", t.Index)
	}
	if t.Attempt > 1 {
		name += fmt.Sprintf("-a%d", t.Attempt)
	}
	return name
}

// currentValidation mirrors the publish gate's currency check.
func (d Deps) currentValidation(ctx context.Context,
	scope tenants.Scope, e executions.Execution,
	v workflows.Version, t executions.TaskExecution,
	in validation.Input, policyVersion int64) (
	validation.ScriptValidation, bool) {
	sv, err := d.Validations.Latest(ctx, scope, e.TenantID,
		in.Digest, policyVersion, pipeline.InputHash(in))
	if err != nil || !sv.Valid || d.now().After(sv.ExpiresAt) {
		return validation.ScriptValidation{}, false
	}
	if sv.WorkflowVersionID == nil || *sv.WorkflowVersionID != v.ID ||
		sv.TaskName != t.TaskName {
		return validation.ScriptValidation{}, false
	}
	return sv, true
}

// runValidation executes the pipeline and persists the result scoped
// to (version, task).
func (d Deps) runValidation(ctx context.Context,
	scope tenants.Scope, e executions.Execution,
	v workflows.Version, t executions.TaskExecution,
	in validation.Input, policyVersion int64) (
	validation.ScriptValidation, error) {
	sv := d.Pipeline.Run(ctx, e.TenantID, pipeline.Request{
		In:                in,
		PolicyVersion:     policyVersion,
		WorkflowVersionID: &v.ID,
		TaskName:          t.TaskName,
	})
	if d.Metrics != nil {
		d.Metrics.Observe(ctx, sv)
	}
	if err := d.Validations.Put(ctx, scope, sv); err != nil {
		return validation.ScriptValidation{}, err
	}
	return sv, nil
}

// firstBlocking returns the first blocking diagnostic's code:message.
func firstBlocking(sv validation.ScriptValidation,
	blockAt validation.Severity) string {
	for _, dg := range sv.Diagnostics {
		if dg.Severity.AtLeast(blockAt) ||
			dg.Severity == validation.SeverityPolicy ||
			dg.Severity == validation.SeveritySecurity {
			return dg.Code + ": " + dg.Message
		}
	}
	if len(sv.Diagnostics) > 0 {
		return sv.Diagnostics[0].Code + ": " +
			sv.Diagnostics[0].Message
	}
	return "script validation failed"
}

// resolveParams resolves execution parameters against the spec.
func resolveParams(spec workflowspec.Workflow,
	e executions.Execution) (map[string]any, []workflowspec.FieldError) {
	return wfvalidate.ResolveParameters(spec.Spec.Parameters,
		e.Parameters)
}

// placementCluster resolves a task's placement cluster for
// payload-less (command) tasks — the same rule TaskSnapshot applies.
func placementCluster(ctx context.Context, d Deps,
	spec workflowspec.Workflow,
	t workflowspec.Task) (*clusters.Cluster, error) {
	name := ""
	if spec.Spec.Placement != nil {
		name = spec.Spec.Placement.Cluster
	}
	if t.Placement != nil && t.Placement.Cluster != "" {
		name = t.Placement.Cluster
	}
	if name == "" {
		return nil, nil
	}
	c, err := d.Clusters.GetByNameOrID(ctx, name)
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			return nil, apperr.New(apperr.Validation, "CLUSTER_UNKNOWN",
				"cluster is not registered")
		}
		return nil, err
	}
	return &c, nil
}

// --- template rendering -------------------------------------------------------

// renderArgv renders command/args elements: each element is a template;
// {{ array.taskId }} renders to a runtime ArgvElement only when it is
// the whole element.
func renderArgv(elems []string, sc admitScope) (
	[]admission.ArgvElement, error) {
	out := make([]admission.ArgvElement, 0, len(elems))
	for i, s := range elems {
		tpl, err := expr.ParseTemplate(s)
		if err != nil {
			return nil, fmt.Errorf("argv[%d]: %w", i, err)
		}
		parts, err := tpl.RenderParts(sc)
		if err != nil {
			return nil, fmt.Errorf("argv[%d]: %w", i, err)
		}
		if len(parts) == 1 && parts[0].Runtime != "" {
			out = append(out,
				admission.ArgvElement{Runtime: parts[0].Runtime})
			continue
		}
		var b strings.Builder
		for _, p := range parts {
			if p.Runtime != "" {
				return nil, fmt.Errorf(
					"argv[%d]: runtime ref must be the whole element", i)
			}
			b.WriteString(p.Literal)
		}
		out = append(out, admission.ArgvElement{Literal: b.String()})
	}
	return out, nil
}

// renderEnv renders env values into User literals and Runtime refs.
func renderEnv(env map[string]string, sc admitScope,
	uses map[string]workflowspec.SecretUse,
	infos map[string]wfvalidate.SecretReferenceInfo) (admission.EnvSet, error) {
	out := admission.EnvSet{
		User:    map[string]string{},
		Runtime: map[string]string{},
	}
	for k, s := range env {
		tpl, err := expr.ParseTemplate(s)
		if err != nil {
			return out, fmt.Errorf("env %s: %w", k, err)
		}
		if e, ok := tpl.SoleExpr(); ok {
			if p, sole := e.SoleRef(); sole && len(p) == 2 && p[0] == "secrets" {
				handle := p[1]
				use, exists := uses[handle]
				info, found := infos[handle]
				id, parseErr := uuid.Parse(info.ID)
				if !exists || !found || use.Use != "env" || parseErr != nil {
					return out, fmt.Errorf("env %s: invalid secret reference", k)
				}
				out.SecretRefs = append(out.SecretRefs, admission.SecretEnvRef{
					Name: workflowspec.SecretEnvName(handle, use), ReferenceID: id,
					Mode: "env", Handle: handle})
				continue
			}
		}
		parts, err := tpl.RenderParts(sc)
		if err != nil {
			return out, fmt.Errorf("env %s: %w", k, err)
		}
		if len(parts) == 1 && parts[0].Runtime != "" {
			out.Runtime[k] = parts[0].Runtime
			continue
		}
		var b strings.Builder
		for _, p := range parts {
			if p.Runtime != "" {
				return out, fmt.Errorf(
					"env %s: runtime ref must be the whole value", k)
			}
			b.WriteString(p.Literal)
		}
		out.User[k] = b.String()
	}
	if len(out.Runtime) == 0 {
		out.Runtime = nil
	}
	return out, nil
}

// renderLiteral renders a template that must produce only literal
// text (workingDirectory/stdout/stderr).
func renderLiteral(src string, sc admitScope, field string) (string, error) {
	if src == "" {
		return "", nil
	}
	tpl, err := expr.ParseTemplate(src)
	if err != nil {
		return "", err
	}
	parts, err := tpl.RenderParts(sc)
	if err != nil {
		return "", fmt.Errorf("%s: %w", field, err)
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Runtime != "" {
			return "", fmt.Errorf(
				"%s: runtime ref is not allowed here", field)
		}
		b.WriteString(p.Literal)
	}
	return b.String(), nil
}

// interpreterFor maps payload language to a fixed interpreter.
func interpreterFor(l workflowspec.Language) (admission.Interpreter, error) {
	switch l {
	case workflowspec.LanguageBash:
		return admission.InterpreterBash, nil
	case workflowspec.LanguageSh:
		return admission.InterpreterSh, nil
	default:
		return "", apperr.New(apperr.Validation, "LANGUAGE_UNSUPPORTED",
			"script language "+string(l)+" is not supported for jobs")
	}
}
