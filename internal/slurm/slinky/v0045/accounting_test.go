package v0045

import (
	"encoding/json"
	"testing"
	"time"

	api "github.com/SlinkyProject/slurm-client/api/v0045"
)

func TestJobRecordMapping(t *testing.T) {
	raw := []byte(`{"job_id":42,"name":"custos-x","user":"alice","account":"acct","partition":"debug","allocation_nodes":2,"state":{"current":["COMPLETED"]},"exit_code":{"return_code":{"set":true,"number":0}},"time":{"submission":100,"eligible":110,"start":120,"end":180,"elapsed":60},"tres":{"allocated":[{"type":"cpu","count":4},{"type":"mem","count":2048},{"type":"gres","name":"gpu","count":2}]},"steps":[{"tres":{"consumed":{"total":[{"type":"energy","count":99}]}}}]}`)
	var j api.V0045Job
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	got := jobRecord(j)
	if got.ID.ID != 42 || got.Name != "custos-x" || got.NodeCount != 2 || got.TRESAlloc["cpu"] != 4 || got.TRESAlloc["mem"] != 2048 || got.TRESAlloc["gres/gpu"] != 2 || got.TRESUsage["energy"] != 99 || got.Steps != 1 {
		t.Fatalf("record=%+v", got)
	}
	if !got.SubmitTime.Equal(time.Unix(100, 0)) || got.Elapsed != time.Minute {
		t.Fatalf("times=%+v", got)
	}
}
