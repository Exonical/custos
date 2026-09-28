package v0045

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	api "github.com/SlinkyProject/slurm-client/api/v0045"

	"github.com/Exonical/custos/internal/slurm"
)

// GetAccounts implements slurm.Accounting for slurmdb accounts.
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
		out = append(out, associationRecord(a))
	}
	return out, nil
}

func assocDefaultQoS(a api.V0045Assoc) *string {
	if a.Default == nil {
		return nil
	}
	return a.Default.Qos
}

func associationRecord(a api.V0045Assoc) slurm.Association {
	assoc := slurm.Association{ID: i32s(a.Id), User: a.User, Account: strV(a.Account), Cluster: strV(a.Cluster), Partition: strV(a.Partition), IsDefault: a.IsDefault != nil && *a.IsDefault, DefaultQoS: strV(assocDefaultQoS(a)), Comment: strV(a.Comment), ParentAccount: strV(a.ParentAccount), GrpTRESMins: groupTRESMins(a)}
	if a.Qos != nil {
		assoc.QoS = append([]string(nil), (*a.Qos)...)
	}
	return assoc
}

func groupTRESMins(a api.V0045Assoc) map[string]int64 {
	if a.Max == nil || a.Max.Tres == nil || a.Max.Tres.Group == nil || a.Max.Tres.Group.Minutes == nil {
		return nil
	}
	out := map[string]int64{}
	addTres(out, *a.Max.Tres.Group.Minutes)
	return out
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

// UpsertAccounts implements slurm.AccountingAdmin. Each account is created
// together with its cluster association under ParentAccount through
// accounts_association: POST /accounts/ alone creates no association, and a
// later user-less POST /associations/ ignores parent_account, leaving an
// association outside the hierarchy that account deletion never removes.
func (c *Client) UpsertAccounts(ctx context.Context, accounts []slurm.Account) error {
	for _, a := range accounts {
		if a.Cluster == "" {
			return fmt.Errorf("%w: account %q has no cluster for its association", slurm.ErrRejected, a.Name)
		}
		parent := a.ParentAccount
		if parent == "" {
			parent = "root"
		}
		body := api.V0045OpenapiAccountsAddCondResp{
			AssociationCondition: api.V0045AccountsAddCond{
				Accounts:    api.V0045StringList{a.Name},
				Clusters:    &api.V0045StringList{a.Cluster},
				Association: &api.V0045AssocRecSet{Parent: &parent},
			},
			Account: &api.V0045AccountShort{Description: &a.Description, Organization: &a.Organization},
		}
		rsp, err := c.api.SlurmdbV0045PostAccountsAssociationWithResponse(ctx, body)
		if err != nil {
			return unavailable(err)
		}
		result := rsp.JSON200
		if result == nil {
			result = rsp.JSONDefault
		}
		if result == nil {
			if err = apiError(nil, rsp.StatusCode()); err != nil {
				return err
			}
			continue
		}
		if err = apiError(result.Errors, rsp.StatusCode()); err != nil {
			return err
		}
		if err = checkMeta(result.Meta); err != nil {
			return err
		}
	}
	return nil
}

// UpsertAssociations implements slurm.AccountingAdmin.
func (c *Client) UpsertAssociations(ctx context.Context, associations []slurm.Association) error {
	ordered := append([]slurm.Association(nil), associations...)
	sort.Slice(ordered, func(i, j int) bool {
		if (ordered[i].User == "") != (ordered[j].User == "") {
			return ordered[i].User == ""
		}
		return associationWriteKey(ordered[i]) < associationWriteKey(ordered[j])
	})
	return c.postAssociations(ctx, ordered)
}

func associationWriteKey(a slurm.Association) string {
	return strings.Join([]string{a.Account, a.Cluster, a.User, a.Partition}, "|")
}

func (c *Client) postAssociations(ctx context.Context, associations []slurm.Association) error {
	if len(associations) == 0 {
		return nil
	}
	body := api.V0045OpenapiAssocsResp{Associations: make(api.V0045AssocList, 0, len(associations))}
	for _, a := range associations {
		x, err := associationForWrite(a)
		if err != nil {
			return err
		}
		body.Associations = append(body.Associations, x)
	}
	rsp, err := c.api.SlurmdbV0045PostAssociationsWithResponse(ctx, body)
	if err != nil {
		return unavailable(err)
	}
	var result *api.V0045OpenapiResp
	if rsp.JSON200 != nil {
		result = rsp.JSON200
	} else {
		result = rsp.JSONDefault
	}
	if result == nil {
		return apiError(nil, rsp.StatusCode())
	}
	if err = apiError(result.Errors, rsp.StatusCode()); err != nil {
		return err
	}
	return checkMeta(result.Meta)
}

func associationForWrite(a slurm.Association) (api.V0045Assoc, error) {
	body := map[string]any{"account": a.Account, "cluster": a.Cluster, "user": a.User}
	if a.User != "" {
		qos := a.QoS
		if qos == nil {
			qos = []string{}
		}
		body["qos"] = qos
		body["default"] = map[string]any{"qos": a.DefaultQoS}
	}
	if a.Comment != "" {
		body["comment"] = a.Comment
	}
	if a.Partition != "" {
		body["partition"] = a.Partition
	}
	if len(a.GrpTRESMins) > 0 {
		keys := make([]string, 0, len(a.GrpTRESMins))
		for key := range a.GrpTRESMins {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		tres := make(api.V0045TresList, 0, len(keys))
		for _, key := range keys {
			count := a.GrpTRESMins[key]
			typ, name, hasName := strings.Cut(key, "/")
			item := api.V0045Tres{Type: typ, Count: &count}
			if hasName {
				nameCopy := name
				item.Name = &nameCopy
			}
			tres = append(tres, item)
		}
		body["max"] = map[string]any{"tres": map[string]any{"group": map[string]any{"minutes": tres}}}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return api.V0045Assoc{}, err
	}
	var out api.V0045Assoc
	err = json.Unmarshal(encoded, &out)
	return out, err
}

// DeleteAssociation implements slurm.AccountingAdmin.
func (c *Client) DeleteAssociation(ctx context.Context, key slurm.AssociationKey) error {
	filters := slurm.AssociationFilter{Accounts: []string{key.Account}, Clusters: []string{key.Cluster}}
	if key.User != "" {
		filters.Users = []string{key.User}
	}
	if key.Partition != "" {
		filters.Partitions = []string{key.Partition}
	}
	associations, err := c.GetAssociations(ctx, filters)
	if err != nil {
		return err
	}
	var matches []slurm.Association
	for _, assoc := range associations {
		if assoc.Account == key.Account && assoc.User == key.User && assoc.Cluster == key.Cluster && assoc.Partition == key.Partition {
			if assoc.IsDefault {
				return fmt.Errorf("%w: refusing to delete Slurm's default association", slurm.ErrRejected)
			}
			matches = append(matches, assoc)
		}
	}
	for _, assoc := range matches {
		if assoc.ID <= 0 {
			return fmt.Errorf("%w: slurmdbd did not return an association ID for deletion", slurm.ErrUnavailable)
		}
		id := strconv.Itoa(int(assoc.ID))
		params := &api.SlurmdbV0045DeleteAssociationParams{Id: &id, Account: &key.Account, User: &key.User, Cluster: &key.Cluster, Partition: &key.Partition}
		rsp, err := c.api.SlurmdbV0045DeleteAssociationWithResponse(ctx, params)
		if err != nil {
			return unavailable(err)
		}
		var result *api.V0045OpenapiAssocsRemovedResp
		if rsp.JSON200 != nil {
			result = rsp.JSON200
		} else {
			result = rsp.JSONDefault
		}
		if result == nil {
			if err := apiError(nil, rsp.StatusCode()); err != nil {
				return err
			}
			continue
		}
		if err = apiError(result.Errors, rsp.StatusCode()); err != nil {
			return err
		}
		if err = checkMeta(result.Meta); err != nil {
			return err
		}
	}
	return nil
}

// DeleteAccount implements slurm.AccountingAdmin.
func (c *Client) DeleteAccount(ctx context.Context, name string) error {
	rsp, err := c.api.SlurmdbV0045DeleteAccountWithResponse(ctx, name)
	if err != nil {
		return unavailable(err)
	}
	var result *api.V0045OpenapiAccountsRemovedResp
	if rsp.JSON200 != nil {
		result = rsp.JSON200
	} else {
		result = rsp.JSONDefault
	}
	if result == nil {
		return apiError(nil, rsp.StatusCode())
	}
	if err = apiError(result.Errors, rsp.StatusCode()); err != nil {
		return err
	}
	return checkMeta(result.Meta)
}

var _ slurm.AccountingAdmin = (*Client)(nil)
