// Package fake is an in-memory implementation of slurm.Cluster and
// slurm.Accounting for tests: configurable topology, a job state machine,
// and error-injection hooks (docs/slurm.md "Fake for tests").
package fake

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Exonical/custos/internal/slurm"
)

// Cluster is a thread-safe in-memory Slurm.
type Cluster struct {
	mu sync.Mutex

	ping         slurm.PingResult
	partitions   []slurm.Partition
	nodes        []slurm.Node
	reservations []slurm.Reservation
	accounts     []slurm.Account
	qos          []slurm.QoS
	associations []slurm.Association

	jobs   map[uint32]*jobState
	nextID uint32
	subs   []slurm.JobSubmission

	failNext   int
	failErr    error
	loseSubmit bool
}

type jobState struct {
	job   slurm.Job
	state slurm.JobState
}

var (
	_ slurm.Cluster    = (*Cluster)(nil)
	_ slurm.Accounting = (*Cluster)(nil)
)

// New returns an empty fake.
func New() *Cluster {
	return &Cluster{jobs: map[uint32]*jobState{}, nextID: 1}
}

// --- configuration -----------------------------------------------------

// SetPing overrides the ping result.
func (c *Cluster) SetPing(p slurm.PingResult) { c.mu.Lock(); c.ping = p; c.mu.Unlock() }

// SetPartitions replaces the partition list.
func (c *Cluster) SetPartitions(p []slurm.Partition) {
	c.mu.Lock()
	c.partitions = p
	c.mu.Unlock()
}

// SetNodes replaces the node list.
func (c *Cluster) SetNodes(n []slurm.Node) { c.mu.Lock(); c.nodes = n; c.mu.Unlock() }

// SetReservations replaces the reservation list.
func (c *Cluster) SetReservations(r []slurm.Reservation) {
	c.mu.Lock()
	c.reservations = r
	c.mu.Unlock()
}

// SetAccounts replaces the slurmdb account list.
func (c *Cluster) SetAccounts(a []slurm.Account) {
	c.mu.Lock()
	c.accounts = a
	c.mu.Unlock()
}

// SetQoS replaces the QoS list.
func (c *Cluster) SetQoS(q []slurm.QoS) { c.mu.Lock(); c.qos = q; c.mu.Unlock() }

// SetAssociations replaces the association list.
func (c *Cluster) SetAssociations(a []slurm.Association) {
	c.mu.Lock()
	c.associations = a
	c.mu.Unlock()
}

// FailNext makes the next n calls return err (transient injection).
func (c *Cluster) FailNext(n int, err error) {
	c.mu.Lock()
	c.failNext, c.failErr = n, err
	c.mu.Unlock()
}

// LoseNextSubmitResponse makes the next SubmitJob behave as if the
// request was accepted but the response was lost: the job is recorded
// and an ErrUnavailable is returned.
func (c *Cluster) LoseNextSubmitResponse() {
	c.mu.Lock()
	c.loseSubmit = true
	c.mu.Unlock()
}

func (c *Cluster) inject() error {
	if c.failNext > 0 {
		c.failNext--
		return c.failErr
	}
	return nil
}

// Advance sets a job's state (the fake's scheduler).
func (c *Cluster) Advance(id uint32, state slurm.JobState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if j, ok := c.jobs[id]; ok {
		j.state = state
		j.job.State = state
		if terminal(state) {
			j.job.EndTime = time.Now().UTC()
		} else if state == slurm.JobRunning && j.job.StartTime.IsZero() {
			j.job.StartTime = time.Now().UTC()
		}
	}
}

func terminal(s slurm.JobState) bool {
	switch s {
	case slurm.JobCompleted, slurm.JobFailed, slurm.JobCancelled,
		slurm.JobTimeout, slurm.JobNodeFail, slurm.JobPreempted,
		slurm.JobBootFail, slurm.JobDeadline, slurm.JobOutOfMemory:
		return true
	}
	return false
}

// --- slurm.Cluster -----------------------------------------------------

// Ping implements slurm.Cluster.
func (c *Cluster) Ping(_ context.Context) (slurm.PingResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inject(); err != nil {
		return slurm.PingResult{}, err
	}
	if c.ping.Hostname == "" {
		return slurm.PingResult{
			Hostname: "fake-ctld", Mode: "primary",
			Responding: true, Latency: time.Millisecond,
		}, nil
	}
	return c.ping, nil
}

// Capabilities implements slurm.Cluster.
func (c *Cluster) Capabilities(ctx context.Context) (slurm.Capabilities, error) {
	c.mu.Lock()
	err := c.inject()
	c.mu.Unlock()
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
	capb := slurm.Capabilities{
		SlurmVersion: "26.05.4",
		APIVersion:   "v0.0.45",
		Partitions:   parts,
		NodeSummary:  map[string]int{},
		CollectedAt:  time.Now().UTC(),
	}
	qos, gres, feats := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, p := range parts {
		for _, q := range p.AllowedQoS {
			qos[q] = true
		}
	}
	for _, n := range nodes {
		for _, s := range n.State {
			capb.NodeSummary[s]++
		}
		for _, f := range n.Features {
			feats[f] = true
		}
		for _, g := range strings.Split(n.GRES, ",") {
			if g = strings.TrimSpace(g); g != "" {
				parts := strings.Split(g, ":")
				if len(parts) > 1 {
					gres[strings.Join(parts[:len(parts)-1], ":")] = true
				} else {
					gres[g] = true
				}
			}
		}
	}
	capb.QoSNames = sortedKeys(qos)
	capb.GRESTypes = sortedKeys(gres)
	capb.Features = sortedKeys(feats)
	return capb, nil
}

// SubmitJob implements slurm.Cluster.
func (c *Cluster) SubmitJob(_ context.Context, req slurm.JobSubmission) (slurm.JobRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inject(); err != nil {
		return slurm.JobRef{}, err
	}
	c.subs = append(c.subs, req)
	id := c.nextID
	c.nextID++
	j := &jobState{state: slurm.JobPending}
	j.job = slurm.Job{
		ID: slurm.JobID{ID: id}, Name: req.Name, Account: req.Account,
		Partition: req.Partition, QoS: req.QoS, UserName: req.UserName,
		State: slurm.JobPending, SubmitTime: time.Now().UTC(),
		Nodes: req.Nodes, Comment: req.Comment,
	}
	c.jobs[id] = j
	if c.loseSubmit {
		c.loseSubmit = false
		return slurm.JobRef{}, slurm.ErrUnavailable
	}
	return slurm.JobRef{ID: j.job.ID, State: j.state}, nil
}

// Submissions returns every JobSubmission passed to SubmitJob, in order
// (invariant tests inspect the exact request).
func (c *Cluster) Submissions() []slurm.JobSubmission {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]slurm.JobSubmission(nil), c.subs...)
}

// GetJob implements slurm.Cluster.
func (c *Cluster) GetJob(_ context.Context, id slurm.JobID) (slurm.Job, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inject(); err != nil {
		return slurm.Job{}, err
	}
	j, ok := c.jobs[id.ID]
	if !ok {
		return slurm.Job{}, slurm.ErrNotFound
	}
	return j.job, nil
}

// ListJobs implements slurm.Cluster.
func (c *Cluster) ListJobs(_ context.Context, f slurm.JobFilter) ([]slurm.Job, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inject(); err != nil {
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
	var out []slurm.Job
	for _, j := range c.jobs {
		if len(names) > 0 && !names[j.job.Name] {
			continue
		}
		if len(states) > 0 && !states[j.job.State] {
			continue
		}
		if f.Since != nil && j.job.SubmitTime.Before(*f.Since) {
			continue
		}
		out = append(out, j.job)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID.ID < out[k].ID.ID })
	return out, nil
}

// CancelJob implements slurm.Cluster.
func (c *Cluster) CancelJob(_ context.Context, id slurm.JobID, _ slurm.CancelOptions) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inject(); err != nil {
		return err
	}
	j, ok := c.jobs[id.ID]
	if !ok {
		return slurm.ErrNotFound
	}
	if !terminal(j.job.State) {
		j.job.State = slurm.JobCancelled
		j.state = slurm.JobCancelled
		j.job.EndTime = time.Now().UTC()
	}
	return nil
}

// GetNodes implements slurm.Cluster.
func (c *Cluster) GetNodes(_ context.Context) ([]slurm.Node, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inject(); err != nil {
		return nil, err
	}
	return append([]slurm.Node(nil), c.nodes...), nil
}

// GetPartitions implements slurm.Cluster.
func (c *Cluster) GetPartitions(_ context.Context) ([]slurm.Partition, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inject(); err != nil {
		return nil, err
	}
	return append([]slurm.Partition(nil), c.partitions...), nil
}

// GetReservations implements slurm.Cluster.
func (c *Cluster) GetReservations(_ context.Context) ([]slurm.Reservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inject(); err != nil {
		return nil, err
	}
	return append([]slurm.Reservation(nil), c.reservations...), nil
}

// --- slurm.Accounting --------------------------------------------------

// GetAccounts implements slurm.Accounting.
func (c *Cluster) GetAccounts(_ context.Context) ([]slurm.Account, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inject(); err != nil {
		return nil, err
	}
	return append([]slurm.Account(nil), c.accounts...), nil
}

// GetQoS implements slurm.Accounting.
func (c *Cluster) GetQoS(_ context.Context) ([]slurm.QoS, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inject(); err != nil {
		return nil, err
	}
	return append([]slurm.QoS(nil), c.qos...), nil
}

// GetAssociations implements slurm.Accounting.
func (c *Cluster) GetAssociations(_ context.Context,
	f slurm.AssociationFilter) ([]slurm.Association, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inject(); err != nil {
		return nil, err
	}
	var out []slurm.Association
	for _, a := range c.associations {
		if len(f.Users) > 0 && !contains(f.Users, a.User) {
			continue
		}
		if len(f.Accounts) > 0 && !contains(f.Accounts, a.Account) {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// GetJobRecords implements slurm.Accounting (returns jobs as records).
func (c *Cluster) GetJobRecords(_ context.Context,
	f slurm.JobRecordFilter) ([]slurm.JobRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inject(); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, n := range f.Names {
		names[n] = true
	}
	var out []slurm.JobRecord
	for _, j := range c.jobs {
		if len(names) > 0 && !names[j.job.Name] {
			continue
		}
		if f.Since != nil && j.job.SubmitTime.Before(*f.Since) {
			continue
		}
		out = append(out, slurm.JobRecord{
			ID: j.job.ID, Name: j.job.Name, User: j.job.UserName,
			Account: j.job.Account, Partition: j.job.Partition,
			State: j.job.State, StartTime: j.job.StartTime,
			EndTime: j.job.EndTime,
		})
	}
	return out, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
