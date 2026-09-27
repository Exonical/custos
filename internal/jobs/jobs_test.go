package jobs

import "testing"

func TestSubmittingCanAdoptCompletedSlurmJob(t *testing.T) {
	if !TransitionOK(StateSubmitting, StateCompleted) {
		t.Fatal("lost-submit adoption of a completed Slurm job must be allowed")
	}
}
