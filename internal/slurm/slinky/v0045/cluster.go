package v0045

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Exonical/custos/internal/slurm"
)

// Ping implements slurm.Cluster.
func (c *Client) Ping(ctx context.Context) (slurm.PingResult, error) {
	rsp, err := c.api.SlurmV0045GetPingWithResponse(ctx)
	if err != nil {
		return slurm.PingResult{}, unavailable(err)
	}
	var body *pingBody
	if rsp.JSON200 != nil {
		body = (*pingBody)(rsp.JSON200)
	} else if rsp.JSONDefault != nil {
		body = (*pingBody)(rsp.JSONDefault)
	}
	if body == nil {
		return slurm.PingResult{}, fmt.Errorf("%w: %v",
			slurm.ErrUnavailable, errEmpty)
	}
	if err := apiError(body.Errors, rsp.StatusCode()); err != nil {
		return slurm.PingResult{}, err
	}
	if err := checkMeta(body.Meta); err != nil {
		return slurm.PingResult{}, err
	}
	out := slurm.PingResult{}
	if len(body.Pings) > 0 {
		p := body.Pings[0]
		out.Hostname = strV(p.Hostname)
		if p.Primary {
			out.Mode = "primary"
		} else {
			out.Mode = "backup"
		}
		out.Responding = p.Responding
		out.Latency = time.Duration(i64(p.Latency)) * time.Microsecond
	}
	return out, nil
}

// Capabilities implements slurm.Cluster: composes ping/diag metadata
// with partitions and nodes.
func (c *Client) Capabilities(ctx context.Context) (slurm.Capabilities, error) {
	ping, err := c.Ping(ctx)
	if err != nil {
		return slurm.Capabilities{}, err
	}
	parts, err := c.GetPartitions(ctx)
	if err != nil {
		return slurm.Capabilities{}, err
	}
	nodes, err := c.GetNodes(ctx)
	if err != nil {
		return slurm.Capabilities{}, err
	}
	// slurm release comes from diag's meta block.
	drsp, err := c.api.SlurmV0045GetDiagWithResponse(ctx)
	if err != nil {
		return slurm.Capabilities{}, unavailable(err)
	}
	var release string
	if drsp.JSON200 != nil {
		if err := apiError(drsp.JSON200.Errors, drsp.StatusCode()); err != nil {
			return slurm.Capabilities{}, err
		}
		if err := checkMeta(drsp.JSON200.Meta); err != nil {
			return slurm.Capabilities{}, err
		}
		release = slurmRelease(drsp.JSON200.Meta)
	} else if drsp.JSONDefault != nil {
		if err := apiError(drsp.JSONDefault.Errors, drsp.StatusCode()); err != nil {
			return slurm.Capabilities{}, err
		}
	}

	capb := slurm.Capabilities{
		SlurmVersion: release,
		APIVersion:   APIVersion,
		Partitions:   parts,
		NodeSummary:  map[string]int{},
		CollectedAt:  time.Now().UTC(),
	}
	qos, gres, feats := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, p := range parts {
		for _, q := range p.AllowedQoS {
			qos[q] = true
		}
		if p.DefaultQoS != "" {
			qos[p.DefaultQoS] = true
		}
	}
	for _, n := range nodes {
		for _, s := range n.State {
			capb.NodeSummary[s]++
		}
		for _, f := range n.Features {
			feats[f] = true
		}
		for _, g := range gresTypes(n.GRES) {
			gres[g] = true
		}
	}
	_ = ping
	capb.QoSNames = sortedKeys(qos)
	capb.GRESTypes = sortedKeys(gres)
	capb.Features = sortedKeys(feats)
	return capb, nil
}

// gresTypes extracts "name:type" tokens from a GRES string such as
// "gpu:h100:4(S:0-1),shard:2".
func gresTypes(gres string) []string {
	var out []string
	for _, ent := range strings.Split(gres, ",") {
		ent = strings.TrimSpace(ent)
		if ent == "" || ent == "(null)" {
			continue
		}
		// strip "(S:0-1)"-style suffixes
		if i := strings.IndexByte(ent, '('); i >= 0 {
			ent = ent[:i]
		}
		parts := strings.Split(ent, ":")
		switch len(parts) {
		case 0:
		case 1:
			out = append(out, parts[0])
		default:
			// name:type[:count] -> name:type (count is numeric)
			last := parts[len(parts)-1]
			if isAllDigits(last) {
				out = append(out, strings.Join(parts[:len(parts)-1], ":"))
			} else {
				out = append(out, ent)
			}
		}
	}
	return out
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// GetPartitions implements slurm.Cluster.
func (c *Client) GetPartitions(ctx context.Context) ([]slurm.Partition, error) {
	rsp, err := c.api.SlurmV0045GetPartitionsWithResponse(ctx,
		&partitionParams)
	if err != nil {
		return nil, unavailable(err)
	}
	var body *partitionResp
	if rsp.JSON200 != nil {
		body = (*partitionResp)(rsp.JSON200)
	} else if rsp.JSONDefault != nil {
		body = (*partitionResp)(rsp.JSONDefault)
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
	out := make([]slurm.Partition, 0, len(body.Partitions))
	for _, p := range body.Partitions {
		out = append(out, mapPartition(p))
	}
	return out, nil
}

// GetNodes implements slurm.Cluster.
func (c *Client) GetNodes(ctx context.Context) ([]slurm.Node, error) {
	rsp, err := c.api.SlurmV0045GetNodesWithResponse(ctx, &nodeParams)
	if err != nil {
		return nil, unavailable(err)
	}
	var body *nodesResp
	if rsp.JSON200 != nil {
		body = (*nodesResp)(rsp.JSON200)
	} else if rsp.JSONDefault != nil {
		body = (*nodesResp)(rsp.JSONDefault)
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
	out := make([]slurm.Node, 0, len(body.Nodes))
	for _, n := range body.Nodes {
		out = append(out, mapNode(n))
	}
	return out, nil
}

// GetReservations implements slurm.Cluster.
func (c *Client) GetReservations(ctx context.Context) ([]slurm.Reservation, error) {
	rsp, err := c.api.SlurmV0045GetReservationsWithResponse(ctx, &resParams)
	if err != nil {
		return nil, unavailable(err)
	}
	var body *resResp
	if rsp.JSON200 != nil {
		body = (*resResp)(rsp.JSON200)
	} else if rsp.JSONDefault != nil {
		body = (*resResp)(rsp.JSONDefault)
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
	out := make([]slurm.Reservation, 0, len(body.Reservations))
	for _, r := range body.Reservations {
		out = append(out, mapReservation(r))
	}
	return out, nil
}

// --- job ops -----------------------------------------------------------
// Implemented in jobs.go (SubmitJob, GetJob, ListJobs, CancelJob).
