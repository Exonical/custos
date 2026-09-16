// Package conformance runs the same port-contract tests against every
// adapter (and the fake). Drivers supply an open function; it is called
// once per subtest and may inspect t.Name() to shape the backend for
// error-path cases (documented convention — see the drivers).
package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/slurm"
)

// Open builds a fresh Cluster+Accounting pair for one subtest.
type Open func(t *testing.T) (slurm.Cluster, slurm.Accounting)

// Run executes the contract table. Subtests named *_errors, *_unauthorized,
// *_unavailable, *_version_mismatch, *_nil_heavy signal the driver which
// backend behavior to configure inside open.
func Run(t *testing.T, open Open) {
	ctx := context.Background()

	t.Run("ping_ok", func(t *testing.T) {
		c, _ := open(t)
		p, err := c.Ping(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !p.Responding || p.Hostname == "" {
			t.Fatalf("ping = %+v", p)
		}
	})

	t.Run("ping_errors", func(t *testing.T) {
		c, _ := open(t)
		if _, err := c.Ping(ctx); err == nil {
			t.Skip("driver did not configure an error path")
		} else if !errors.Is(err, slurm.ErrUnavailable) &&
			!errors.Is(err, slurm.ErrUnauthorized) {
			t.Fatalf("unclassified error: %v", err)
		}
	})

	t.Run("partitions", func(t *testing.T) {
		c, _ := open(t)
		parts, err := c.GetPartitions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(parts) == 0 {
			t.Skip("no partitions configured")
		}
		p := parts[0]
		if p.Name == "" {
			t.Fatal("partition name empty")
		}
		var sawDefault bool
		for _, q := range parts {
			sawDefault = sawDefault || q.IsDefault
		}
		if !sawDefault {
			t.Log("no default partition (acceptable if fixture lacks one)")
		}
	})

	t.Run("nodes", func(t *testing.T) {
		c, _ := open(t)
		nodes, err := c.GetNodes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) == 0 {
			t.Skip("no nodes configured")
		}
		if nodes[0].Name == "" || len(nodes[0].State) == 0 {
			t.Fatalf("node = %+v", nodes[0])
		}
	})

	t.Run("reservations", func(t *testing.T) {
		c, _ := open(t)
		res, err := c.GetReservations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) == 0 {
			t.Skip("no reservations configured")
		}
		if res[0].Name == "" {
			t.Fatal("reservation name empty")
		}
	})

	t.Run("capabilities", func(t *testing.T) {
		c, _ := open(t)
		capb, err := c.Capabilities(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if capb.APIVersion == "" {
			t.Fatal("APIVersion empty")
		}
		if capb.NodeSummary == nil {
			t.Fatal("NodeSummary nil")
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		c, _ := open(t)
		_, err := c.Ping(ctx)
		if err == nil {
			t.Skip("driver did not configure an auth failure")
		}
		if !errors.Is(err, slurm.ErrUnauthorized) {
			t.Fatalf("want ErrUnauthorized, got %v", err)
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		c, _ := open(t)
		_, err := c.Ping(ctx)
		if err == nil {
			t.Skip("driver did not configure unavailability")
		}
		if !errors.Is(err, slurm.ErrUnavailable) {
			t.Fatalf("want ErrUnavailable, got %v", err)
		}
	})

	t.Run("version_mismatch", func(t *testing.T) {
		c, _ := open(t)
		_, err := c.GetNodes(ctx)
		if err == nil {
			t.Skip("driver did not configure a version mismatch")
		}
		if !apperr.Is(err, apperr.Invalid) {
			t.Fatalf("want Invalid (api_version_mismatch), got %v", err)
		}
	})

	t.Run("nil_heavy", func(t *testing.T) {
		c, _ := open(t)
		// A response with all optional fields absent must map without
		// panics.
		if _, err := c.GetNodes(ctx); err != nil {
			t.Fatalf("nil-heavy mapping: %v", err)
		}
	})
}
