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

// GetAccounts implements slurm.Accounting (slurmdb, read-only).
func (c *Client) GetAccounts(ctx context.Context) ([]slurm.Account, error) {
	rsp, err := c.api.SlurmdbV0045GetAccountsWithResponse(ctx,
		&api.SlurmdbV0045GetAccountsParams{})
	if err != nil {
		return nil, unavailable(err)
	}
	var body *api.V0045OpenapiAccountsResp
	if rsp.JSON200 != nil {
		body = rsp.JSON200
	} else if rsp.JSONDefault != nil {
		body = rsp.JSONDefault
	}
	if body == nil {
		return nil, fmt.Errorf("%w: %v", slurm.ErrUnavailable, errEmpty)
	}
	if err := apiError(body.Errors, rsp.StatusCode()); err != nil {
		return nil, err
	}
	if err := checkMeta(body.Meta); err != nil {
		return nil, err
	}
	out := make([]slurm.Account, 0, len(body.Accounts))
	for _, a := range body.Accounts {
		out = append(out, slurm.Account{
			Name:         a.Name,
			Description:  a.Description,
			Organization: a.Organization,
		})
	}
	return out, nil
}

// GetQoS implements slurm.Accounting.
func (c *Client) GetQoS(ctx context.Context) ([]slurm.QoS, error) {
	rsp, err := c.api.SlurmdbV0045GetQosWithResponse(ctx,
		&api.SlurmdbV0045GetQosParams{})
	if err != nil {
		return nil, unavailable(err)
	}
	var body *api.V0045OpenapiSlurmdbdQosResp
	if rsp.JSON200 != nil {
		body = rsp.JSON200
	} else if rsp.JSONDefault != nil {
		body = rsp.JSONDefault
	}
	if body == nil {
		return nil, fmt.Errorf("%w: %v", slurm.ErrUnavailable, errEmpty)
	}
	if err := apiError(body.Errors, rsp.StatusCode()); err != nil {
		return nil, err
	}
	if err := checkMeta(body.Meta); err != nil {
		return nil, err
	}
	out := make([]slurm.QoS, 0, len(body.Qos))
	for _, q := range body.Qos {
		out = append(out, slurm.QoS{
			ID:          i32s(q.Id),
			Name:        strV(q.Name),
			Description: strV(q.Description),
			Priority:    i32u(q.Priority),
		})
	}
	return out, nil
}

// GetAssociations implements slurm.Accounting.
func (c *Client) GetAssociations(ctx context.Context,
	f slurm.AssociationFilter) ([]slurm.Association, error) {
	params := &api.SlurmdbV0045GetAssociationsParams{}
	if len(f.Users) > 0 {
		s := strings.Join(f.Users, ",")
		params.User = &s
	}
	if len(f.Accounts) > 0 {
		s := strings.Join(f.Accounts, ",")
		params.Account = &s
	}
	if len(f.Clusters) > 0 {
		s := strings.Join(f.Clusters, ",")
		params.Cluster = &s
	}
	if len(f.Partitions) > 0 {
		s := strings.Join(f.Partitions, ",")
		params.Partition = &s
	}
	rsp, err := c.api.SlurmdbV0045GetAssociationsWithResponse(ctx, params)
	if err != nil {
		return nil, unavailable(err)
	}
	var body *api.V0045OpenapiAssocsResp
	if rsp.JSON200 != nil {
		body = rsp.JSON200
	} else if rsp.JSONDefault != nil {
		body = rsp.JSONDefault
	}
	if body == nil {
		return nil, fmt.Errorf("%w: %v", slurm.ErrUnavailable, errEmpty)
	}
	if err := apiError(body.Errors, rsp.StatusCode()); err != nil {
		return nil, err
	}
	if err := checkMeta(body.Meta); err != nil {
		return nil, err
	}
	out := make([]slurm.Association, 0, len(body.Associations))
	for _, a := range body.Associations {
		out = append(out, slurm.Association{
			ID:         i32s(a.Id),
			User:       a.User,
			Account:    strV(a.Account),
			Cluster:    strV(a.Cluster),
			Partition:  strV(a.Partition),
			IsDefault:  a.IsDefault != nil && *a.IsDefault,
			DefaultQoS: strV(assocDefaultQoS(a)),
		})
	}
	return out, nil
}

func assocDefaultQoS(a api.V0045Assoc) *string {
	if a.Default == nil {
		return nil
	}
	return a.Default.Qos
}

// GetJobRecords implements slurm.Accounting.
func (c *Client) GetJobRecords(ctx context.Context,
	f slurm.JobRecordFilter) ([]slurm.JobRecord, error) {
	params := &api.SlurmdbV0045GetJobsParams{}
	if len(f.Names) > 0 {
		s := strings.Join(f.Names, ",")
		params.JobName = &s
	}
	if len(f.Users) > 0 {
		s := strings.Join(f.Users, ",")
		params.Users = &s
	}
	if len(f.Accounts) > 0 {
		s := strings.Join(f.Accounts, ",")
		params.Account = &s
	}
	if len(f.States) > 0 {
		states := make([]string, 0, len(f.States))
		for _, st := range f.States {
			states = append(states, string(st))
		}
		s := strings.Join(states, ",")
		params.State = &s
	}
	if f.Since != nil {
		s := strconv.FormatInt(f.Since.Unix(), 10)
		params.StartTime = &s
	}
	if f.Until != nil {
		s := strconv.FormatInt(f.Until.Unix(), 10)
		params.EndTime = &s
	}
	rsp, err := c.api.SlurmdbV0045GetJobsWithResponse(ctx, params)
	if err != nil {
		return nil, unavailable(err)
	}
	var body *api.V0045OpenapiSlurmdbdJobsResp
	if rsp.JSON200 != nil {
		body = rsp.JSON200
	} else if rsp.JSONDefault != nil {
		body = rsp.JSONDefault
	}
	if body == nil {
		return nil, fmt.Errorf("%w: %v", slurm.ErrUnavailable, errEmpty)
	}
	if err := apiError(body.Errors, rsp.StatusCode()); err != nil {
		return nil, err
	}
	if err := checkMeta(body.Meta); err != nil {
		return nil, err
	}
	out := make([]slurm.JobRecord, 0, len(body.Jobs))
	for _, j := range body.Jobs {
		out = append(out, jobRecord(j))
	}
	return out, nil
}

func jobRecord(j api.V0045Job) slurm.JobRecord {
	r := slurm.JobRecord{Name: strV(j.Name), User: strV(j.User), Account: strV(j.Account),
		Partition: strV(j.Partition), NodeCount: i32v(j.AllocationNodes), TRESAlloc: tresList(j.Tres)}
	if j.JobId != nil && *j.JobId >= 0 {
		r.ID.ID = uint32(*j.JobId)
	}
	if j.State != nil && j.State.Current != nil && len(*j.State.Current) > 0 {
		r.State = jobStateOf(string((*j.State.Current)[0]))
	}
	if j.ExitCode != nil {
		r.ExitCode = &slurm.ExitCode{Code: u32v(j.ExitCode.ReturnCode)}
	}
	if j.Time != nil {
		r.SubmitTime = unixTime(j.Time.Submission)
		r.EligibleTime = unixTime(j.Time.Eligible)
		r.StartTime = unixTime(j.Time.Start)
		r.EndTime = unixTime(j.Time.End)
		if j.Time.Elapsed != nil {
			r.Elapsed = time.Duration(*j.Time.Elapsed) * time.Second
		}
	}
	if j.Steps != nil {
		r.Steps = int32(len(*j.Steps)) // #nosec G115 -- bounded by response size
		r.TRESUsage = map[string]int64{}
		for _, step := range *j.Steps {
			if step.Tres != nil && step.Tres.Consumed != nil && step.Tres.Consumed.Total != nil {
				addTres(r.TRESUsage, *step.Tres.Consumed.Total)
			}
		}
		if len(r.TRESUsage) == 0 {
			r.TRESUsage = nil
		}
	}
	return r
}

func i32v(v *int32) int32 {
	if v == nil {
		return 0
	}
	return *v
}
func unixTime(v *int64) time.Time {
	if v == nil || *v <= 0 {
		return time.Time{}
	}
	return time.Unix(*v, 0).UTC()
}
func tresList(t *struct {
	Allocated *api.V0045TresList `json:"allocated,omitempty"`
	Requested *api.V0045TresList `json:"requested,omitempty"`
}) map[string]int64 {
	if t == nil || t.Allocated == nil {
		return nil
	}
	out := map[string]int64{}
	addTres(out, *t.Allocated)
	return out
}
func addTres(out map[string]int64, list api.V0045TresList) {
	for _, t := range list {
		if t.Count == nil {
			continue
		}
		key := strings.ToLower(t.Type)
		if t.Name != nil && *t.Name != "" {
			key += "/" + strings.ToLower(*t.Name)
		}
		out[key] += *t.Count
	}
}
