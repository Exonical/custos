package v0045

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	api "github.com/SlinkyProject/slurm-client/api/v0045"

	"github.com/Exonical/custos/internal/slurm"
)

// Response body aliases.
type (
	jobsResp   = api.V0045OpenapiJobInfoResp
	submitResp = api.V0045OpenapiJobSubmitResponse
)

// jobDescAllowList enumerates the only V0045JobDescMsg fields this
// adapter may set (docs/slurm.md "Job description generation"). A unit
// test asserts via reflection that no other field is ever non-nil.
var jobDescAllowList = map[string]bool{
	"Account":                 true,
	"Partition":               true,
	"Qos":                     true,
	"Reservation":             true,
	"Nodes":                   true, // "N" string: min=max
	"Tasks":                   true,
	"TasksPerNode":            true,
	"CpusPerTask":             true,
	"MemoryPerNode":           true,
	"MemoryPerCpu":            true,
	"TresPerNode":             true, // gres string, e.g. "gpu:h100:8"
	"Constraints":             true,
	"Licenses":                true,
	"TimeLimit":               true, // minutes
	"CurrentWorkingDirectory": true,
	"StandardOutput":          true,
	"StandardError":           true,
	"Environment":             true, // explicit K=V array; no inheritance
	"Dependency":              true,
	"Array":                   true,
	"Nice":                    true,
	"Name":                    true,
	"Comment":                 true,
	"Script":                  true,
	"Argv":                    true,
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func i32p(v int) *int32 {
	if v == 0 {
		return nil
	}
	n := int32(v) // #nosec G115 -- task counts are bounded by admission policy
	return &n
}

// u64nv builds a NoVal field (set, non-infinite).
func u64nv(mib int64) *api.V0045Uint64NoValStruct {
	if mib <= 0 {
		return nil
	}
	set := true
	return &api.V0045Uint64NoValStruct{Set: &set, Number: &mib}
}

func u32nv(minutes int64) *api.V0045Uint32NoValStruct {
	if minutes <= 0 {
		return nil
	}
	set := true
	n := int32(minutes) // #nosec G115 -- walltime is bounded by admission policy
	return &api.V0045Uint32NoValStruct{Set: &set, Number: &n}
}

// gresString renders the tres_per_node list ("gpu:h100:8,...").
func gresString(g []slurm.GRESRequest) string {
	if len(g) == 0 {
		return ""
	}
	parts := make([]string, 0, len(g))
	for _, r := range g {
		s := r.Name
		if r.Type != "" {
			s += ":" + r.Type
		}
		if r.Count > 0 {
			s += ":" + strconv.FormatInt(r.Count, 10)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}

// depString renders "afterok:1:2,afternotok:3".
func depString(deps []slurm.Dependency) string {
	if len(deps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(deps))
	for _, d := range deps {
		ids := make([]string, 0, len(d.JobIDs))
		for _, j := range d.JobIDs {
			ids = append(ids, strconv.FormatUint(uint64(j.ID), 10))
		}
		parts = append(parts, string(d.Kind)+":"+strings.Join(ids, ":"))
	}
	return strings.Join(parts, ",")
}

// arrayString renders "0-99:4%10" / "0-99%10" forms.
func arrayString(a *slurm.ArraySpec) string {
	if a == nil {
		return ""
	}
	s := strconv.Itoa(a.Start) + "-" + strconv.Itoa(a.End)
	if a.Step > 0 {
		s += ":" + strconv.Itoa(a.Step)
	}
	if a.MaxConcurrent > 0 {
		s += "%" + strconv.Itoa(a.MaxConcurrent)
	}
	return s
}

// toJobDesc maps the neutral JobSubmission onto JobDescMsg fields from
// jobDescAllowList only. Never sets mail, get_user_environment, user_id,
// group_id or any other implicit-identity/environment field.
func toJobDesc(req slurm.JobSubmission) *api.V0045JobDescMsg {
	d := &api.V0045JobDescMsg{
		Account:     strp(req.Account),
		Partition:   strp(req.Partition),
		Qos:         strp(req.QoS),
		Reservation: strp(req.Reservation),
		Name:        strp(req.Name),
		Comment:     strp(req.Comment),
		Constraints: strp(req.Constraints),
		Script:      strp(req.Script),

		CurrentWorkingDirectory: strp(req.WorkingDir),
		StandardOutput:          strp(req.Stdout),
		StandardError:           strp(req.Stderr),

		Tasks:         i32p(req.Tasks),
		TasksPerNode:  i32p(req.TasksPerNode),
		CpusPerTask:   i32p(req.CPUsPerTask),
		MemoryPerNode: u64nv(req.MemoryPerNodeMiB),
		MemoryPerCpu:  u64nv(req.MemoryPerCPUMiB),
		TresPerNode:   strp(gresString(req.GRES)),
		Licenses:      strp(strings.Join(req.Licenses, ",")),
		Dependency:    strp(depString(req.Dependencies)),
		Array:         strp(arrayString(req.Array)),
	}
	if req.Nice != nil {
		n := int32(*req.Nice) // #nosec G115 -- nice is a small scheduler value
		d.Nice = &n
	}
	if req.Nodes > 0 {
		d.Nodes = strp(strconv.Itoa(req.Nodes)) // "N" means min=max=N
	}
	if req.Walltime > 0 {
		d.TimeLimit = u32nv(int64(req.Walltime / time.Minute))
	}
	// slurmrestd rejects submissions without an environment block
	// (errno 2127) — send a minimal PATH when the request carries none.
	if len(req.Environment) > 0 {
		env := make(api.V0045StringArray, 0, len(req.Environment))
		for k, v := range req.Environment {
			env = append(env, k+"="+v)
		}
		d.Environment = &env
	} else {
		env := api.V0045StringArray{"PATH=/usr/bin:/bin"}
		d.Environment = &env
	}
	if len(req.Argv) > 0 {
		argv := api.V0045StringArray(req.Argv)
		d.Argv = &argv
	}
	return d
}

// SubmitJob implements slurm.Cluster.
func (c *Client) SubmitJob(ctx context.Context, req slurm.JobSubmission) (slurm.JobRef, error) {
	rsp, err := c.api.SlurmV0045PostJobSubmitWithResponse(ctx,
		api.V0045JobSubmitReq{Job: toJobDesc(req)})
	if err != nil {
		return slurm.JobRef{}, unavailable(err)
	}
	var body *submitResp
	switch {
	case rsp.JSON200 != nil:
		body = rsp.JSON200
	case rsp.JSONDefault != nil:
		body = rsp.JSONDefault
	default:
		return slurm.JobRef{}, fmt.Errorf("%w: %v", slurm.ErrUnavailable, errEmpty)
	}
	if err := apiError(body.Errors, rsp.StatusCode()); err != nil {
		return slurm.JobRef{}, err
	}
	if err := checkMeta(body.Meta); err != nil {
		return slurm.JobRef{}, err
	}
	if body.JobId == nil || *body.JobId < 0 {
		return slurm.JobRef{}, fmt.Errorf("%w: submit returned no job_id",
			slurm.ErrUnavailable)
	}
	return slurm.JobRef{ID: slurm.JobID{ID: uint32(*body.JobId)},
		State: slurm.JobPending}, nil
}

// GetJob implements slurm.Cluster.
func (c *Client) GetJob(ctx context.Context, id slurm.JobID) (slurm.Job, error) {
	jobID := strconv.FormatUint(uint64(id.ID), 10)
	if id.ArrayTaskID != nil {
		jobID += "_" + strconv.FormatUint(uint64(*id.ArrayTaskID), 10)
	}
	rsp, err := c.api.SlurmV0045GetJobWithResponse(ctx, jobID,
		&api.SlurmV0045GetJobParams{})
	if err != nil {
		return slurm.Job{}, unavailable(err)
	}
	var body *jobsResp
	switch {
	case rsp.JSON200 != nil:
		body = rsp.JSON200
	case rsp.JSONDefault != nil:
		body = rsp.JSONDefault
	default:
		return slurm.Job{}, fmt.Errorf("%w: %v", slurm.ErrUnavailable, errEmpty)
	}
	if err := apiError(body.Errors, rsp.StatusCode()); err != nil {
		return slurm.Job{}, err
	}
	if err := checkMeta(body.Meta); err != nil {
		return slurm.Job{}, err
	}
	for _, j := range body.Jobs {
		if j.JobId != nil && int64(*j.JobId) == int64(id.ID) {
			return mapJob(j), nil
		}
	}
	return slurm.Job{}, slurm.ErrNotFound
}

// ListJobs implements slurm.Cluster. slurmrestd GET /jobs has no name
// filter, so jobs are fetched and filtered client-side by Names/Users/
// States/Since (docs/slurm.md notes the bound is the queue size).
func (c *Client) ListJobs(ctx context.Context, f slurm.JobFilter) ([]slurm.Job, error) {
	rsp, err := c.api.SlurmV0045GetJobsWithResponse(ctx,
		&api.SlurmV0045GetJobsParams{})
	if err != nil {
		return nil, unavailable(err)
	}
	var body *jobsResp
	switch {
	case rsp.JSON200 != nil:
		body = rsp.JSON200
	case rsp.JSONDefault != nil:
		body = rsp.JSONDefault
	default:
		return nil, fmt.Errorf("%w: %v", slurm.ErrUnavailable, errEmpty)
	}
	if err := apiError(body.Errors, rsp.StatusCode()); err != nil {
		return nil, err
	}
	if err := checkMeta(body.Meta); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, n := range f.Names {
		names[n] = true
	}
	states := map[slurm.JobState]bool{}
	for _, s := range f.States {
		states[s] = true
	}
	users := map[string]bool{}
	for _, u := range f.Users {
		users[u] = true
	}
	out := make([]slurm.Job, 0, len(body.Jobs))
	for _, j := range body.Jobs {
		m := mapJob(j)
		if len(names) > 0 && !names[m.Name] {
			continue
		}
		if len(users) > 0 && !users[m.UserName] {
			continue
		}
		if len(states) > 0 && !states[m.State] {
			continue
		}
		if f.Since != nil && !m.SubmitTime.IsZero() && m.SubmitTime.Before(*f.Since) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// CancelJob implements slurm.Cluster (DELETE /job/{id}?signal=).
func (c *Client) CancelJob(ctx context.Context, id slurm.JobID,
	opts slurm.CancelOptions) error {
	params := &api.SlurmV0045DeleteJobParams{}
	if opts.Signal != "" {
		params.Signal = &opts.Signal
	}
	rsp, err := c.api.SlurmV0045DeleteJobWithResponse(ctx,
		strconv.FormatUint(uint64(id.ID), 10), params)
	if err != nil {
		return unavailable(err)
	}
	switch {
	case rsp.JSON200 != nil:
		if err := apiError(rsp.JSON200.Errors, rsp.StatusCode()); err != nil {
			return err
		}
		return checkMeta(rsp.JSON200.Meta)
	case rsp.JSONDefault != nil:
		return apiError(rsp.JSONDefault.Errors, rsp.StatusCode())
	default:
		return apiError(nil, rsp.StatusCode())
	}
}

// --- mapping -----------------------------------------------------------

func mapJob(j api.V0045JobInfo) slurm.Job {
	out := slurm.Job{
		Name:         strV(j.Name),
		Account:      strV(j.Account),
		Partition:    strV(j.Partition),
		QoS:          strV(j.Qos),
		UserName:     strV(j.UserName),
		Comment:      strV(j.Comment),
		StateReason:  strV(j.StateReason),
		NodeList:     strV(j.Nodes),
		SubmitTime:   tsVal(j.SubmitTime),
		EligibleTime: tsVal(j.EligibleTime),
		StartTime:    tsVal(j.StartTime),
		EndTime:      tsVal(j.EndTime),
		Nodes:        u32v(j.NodeCount),
		CPUs:         u32v(j.Cpus),
		TRESAlloc:    tresMap(strV(j.TresAllocStr)),
	}
	if j.JobId != nil && *j.JobId >= 0 {
		out.ID.ID = uint32(*j.JobId)
	}
	if n := u32v(j.ArrayTaskId); j.ArrayTaskId != nil &&
		j.ArrayTaskId.Set != nil && *j.ArrayTaskId.Set {
		t := uint32(n) // #nosec G115 -- slurmrestd returns non-negative task ids
		out.ID.ArrayTaskID = &t
	}
	if st := j.JobState; st != nil && len(*st) > 0 {
		out.State = jobStateOf(string((*st)[0]))
	}
	if ec := j.ExitCode; ec != nil {
		out.ExitCode = &slurm.ExitCode{Code: u32v(ec.ReturnCode)}
		if ec.Signal != nil && ec.Signal.Id != nil &&
			ec.Signal.Id.Set != nil && *ec.Signal.Id.Set &&
			ec.Signal.Id.Number != nil {
			out.ExitCode.Signal = int(*ec.Signal.Id.Number)
		}
	}
	return out
}

// jobStateOf maps a Slurm state string to the neutral enum; unknown or
// composite strings collapse to JobUnknown.
func jobStateOf(s string) slurm.JobState {
	switch slurm.JobState(s) {
	case slurm.JobPending, slurm.JobRunning, slurm.JobSuspended,
		slurm.JobCompleting, slurm.JobCompleted, slurm.JobFailed,
		slurm.JobCancelled, slurm.JobTimeout, slurm.JobNodeFail,
		slurm.JobPreempted, slurm.JobBootFail, slurm.JobDeadline,
		slurm.JobOutOfMemory:
		return slurm.JobState(s)
	default:
		return slurm.JobUnknown
	}
}

// tresMap parses "cpu=4,mem=8000M,gres/gpu=8" into counts (M suffixes
// and fractional values are truncated; only the integer part is kept).
func tresMap(s string) map[string]int64 {
	if s == "" {
		return nil
	}
	out := map[string]int64{}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		v = strings.TrimRightFunc(v, func(r rune) bool {
			return r < '0' || r > '9'
		})
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			out[k] = n
		}
	}
	return out
}
