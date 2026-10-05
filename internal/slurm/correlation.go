package slurm

import (
	"strings"

	"github.com/google/uuid"
)

// jobNamePrefix and jobCommentPrefix form the scheduler correlation
// protocol linking a Slurm job back to its Custos job.
const (
	jobNamePrefix    = "custos-"
	jobCommentPrefix = "custos:"
)

// CustosJobName returns the deterministic Slurm job name for Custos job
// id ("custos-<uuid>"), the primary correlation key for lost-submit
// adoption, orphan handling, and accounting attribution.
func CustosJobName(id uuid.UUID) string {
	return jobNamePrefix + id.String()
}

// CustosJobComment returns the Slurm job comment for Custos job id and
// task ("custos:<uuid>/<task>"), the secondary correlation key.
func CustosJobComment(id uuid.UUID, task string) string {
	return jobCommentPrefix + id.String() + "/" + task
}

// ParseCustosJobName extracts the Custos job id from a Slurm job name
// produced by CustosJobName. It reports false for any other name.
func ParseCustosJobName(name string) (uuid.UUID, bool) {
	rest, ok := strings.CutPrefix(name, jobNamePrefix)
	if !ok || rest == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(rest)
	return id, err == nil
}
