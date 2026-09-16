package v0045

import (
	"context"
	"fmt"
	"strings"

	api "github.com/SlinkyProject/slurm-client/api/v0045"

	"github.com/Exonical/custos/internal/platform/apperr"
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

// GetJobRecords implements slurm.Accounting (stub until M7).
func (c *Client) GetJobRecords(_ context.Context,
	_ slurm.JobRecordFilter) ([]slurm.JobRecord, error) {
	return nil, apperr.New(apperr.Internal, "slurm.not_implemented",
		"v0045 adapter: GetJobRecords not implemented until M7")
}
