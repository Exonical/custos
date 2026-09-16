package fake_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/conformance"
	"github.com/Exonical/custos/internal/slurm/fake"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (slurm.Cluster, slurm.Accounting) {
		c := fake.New()
		seed(c)
		// The subtest name selects the backend behavior (conformance
		// convention); the fake implements it via FailNext.
		switch {
		case strings.HasSuffix(t.Name(), "_errors"),
			strings.HasSuffix(t.Name(), "_unavailable"):
			c.FailNext(1, slurm.ErrUnavailable)
		case strings.HasSuffix(t.Name(), "_unauthorized"):
			c.FailNext(1, slurm.ErrUnauthorized)
		case strings.HasSuffix(t.Name(), "_version_mismatch"):
			t.Skip("fake has no API version to mismatch")
		}
		return c, c
	})
}

func seed(c *fake.Cluster) {
	c.SetPing(slurm.PingResult{
		Hostname: "fake-ctld", Mode: "primary",
		Responding: true, Latency: time.Millisecond,
	})
	c.SetPartitions([]slurm.Partition{
		{Name: "normal", State: "UP", Nodes: 2, TotalCPUs: 128,
			IsDefault: true, AllowedQoS: []string{"normal"}},
	})
	c.SetNodes([]slurm.Node{
		{Name: "n1", State: []string{"IDLE"}, CPUs: 64,
			MemoryMiB: 256000, Partitions: []string{"normal"},
			Features: []string{"zen4"}},
		{Name: "n2", State: []string{"ALLOCATED"}, CPUs: 64, AllocCPUs: 32,
			MemoryMiB: 256000, AllocMemoryMiB: 128000,
			Partitions: []string{"normal"}, GRES: "gpu:h100:4"},
	})
	c.SetReservations([]slurm.Reservation{
		{Name: "maint", Nodes: 1, Users: []string{"root"}},
	})
}

func TestJobStateMachine(t *testing.T) {
	c := fake.New()
	ref, err := c.SubmitJob(t.Context(), slurm.JobSubmission{Name: "j1"})
	if err != nil {
		t.Fatal(err)
	}
	j, err := c.GetJob(t.Context(), ref.ID)
	if err != nil || j.State != slurm.JobPending {
		t.Fatalf("job = %+v err=%v", j, err)
	}
	c.Advance(ref.ID.ID, slurm.JobRunning)
	j, _ = c.GetJob(t.Context(), ref.ID)
	if j.State != slurm.JobRunning || j.StartTime.IsZero() {
		t.Fatalf("running job = %+v", j)
	}
	if err := c.CancelJob(t.Context(), ref.ID, slurm.CancelOptions{}); err != nil {
		t.Fatal(err)
	}
	j, _ = c.GetJob(t.Context(), ref.ID)
	if j.State != slurm.JobCancelled {
		t.Fatalf("cancelled job = %+v", j)
	}
}

func TestErrorInjectionAndLostSubmit(t *testing.T) {
	c := fake.New()
	c.FailNext(1, slurm.ErrUnavailable)
	if _, err := c.Ping(t.Context()); err == nil {
		t.Fatal("expected injected error")
	}
	if _, err := c.Ping(t.Context()); err != nil {
		t.Fatalf("injection should be consumed: %v", err)
	}

	c.LoseNextSubmitResponse()
	if _, err := c.SubmitJob(t.Context(), slurm.JobSubmission{Name: "lost"}); err == nil {
		t.Fatal("expected lost-response error")
	}
	// The job was nevertheless accepted.
	jobs, err := c.ListJobs(t.Context(), slurm.JobFilter{Names: []string{"lost"}})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("lost job not recorded: %v %v", jobs, err)
	}
}
