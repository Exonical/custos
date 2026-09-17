package v0045

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	api "github.com/SlinkyProject/slurm-client/api/v0045"

	"github.com/Exonical/custos/internal/slurm"
)

// fullSubmission sets every neutral field so the allow-list test covers
// the whole mapping.
func fullSubmission() slurm.JobSubmission {
	nice := -5
	return slurm.JobSubmission{
		Name: "custos-x", Account: "acct", Partition: "gpu", QoS: "high",
		Reservation: "resv", Script: "#!/bin/bash\necho hi\n",
		Argv: []string{"run"}, WorkingDir: "/tmp",
		Environment: map[string]string{"A": "1"},
		Stdout:      "out.%j", Stderr: "err.%j",
		Nodes: 2, Tasks: 4, TasksPerNode: 2, CPUsPerTask: 8,
		MemoryPerNodeMiB: 4096, MemoryPerCPUMiB: 512,
		GRES:        []slurm.GRESRequest{{Name: "gpu", Type: "h100", Count: 8}},
		Constraints: "avx512", Licenses: []string{"lic:2"},
		Walltime: 4 * time.Hour,
		Array:    &slurm.ArraySpec{Start: 0, End: 9, Step: 1, MaxConcurrent: 4},
		Dependencies: []slurm.Dependency{{
			Kind: slurm.DepAfterOK,
			JobIDs: []slurm.JobID{
				{ID: 42},
			},
		}},
		Nice:    &nice,
		Comment: "custos:x/adhoc", UserName: "svc",
	}
}

// TestJobDescAllowList is the adapter half of the typed-fields
// invariant: every non-nil V0045JobDescMsg field must be on the
// documented allow-list (never mail/user/group/env-inheritance).
func TestJobDescAllowList(t *testing.T) {
	d := toJobDesc(fullSubmission())
	v := reflect.ValueOf(*d)
	typ := v.Type()
	set := map[string]bool{}
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.IsNil() {
			continue
		}
		name := typ.Field(i).Name
		set[name] = true
		if !jobDescAllowList[name] {
			t.Errorf("JobDescMsg.%s set but not in allow-list", name)
		}
	}
	// Forbidden fields can never be set.
	for _, f := range []string{"MailType", "MailUser", "UserId", "GroupId",
		"GetUserEnvironment", "SpankEnvironment", "Immediate"} {
		if set[f] {
			t.Errorf("forbidden field %s was set", f)
		}
	}
}

// TestJobDescValues asserts the mapping values.
func TestJobDescValues(t *testing.T) {
	d := toJobDesc(fullSubmission())
	eq := func(want string, got *string, field string) {
		t.Helper()
		if got == nil || *got != want {
			t.Errorf("%s = %v, want %q", field, got, want)
		}
	}
	eq("acct", d.Account, "Account")
	eq("gpu", d.Partition, "Partition")
	eq("high", d.Qos, "Qos")
	eq("resv", d.Reservation, "Reservation")
	eq("2", d.Nodes, "Nodes")
	eq("gpu:h100:8", d.TresPerNode, "TresPerNode")
	eq("avx512", d.Constraints, "Constraints")
	eq("lic:2", d.Licenses, "Licenses")
	eq("/tmp", d.CurrentWorkingDirectory, "CWD")
	eq("out.%j", d.StandardOutput, "Stdout")
	eq("err.%j", d.StandardError, "Stderr")
	eq("afterok:42", d.Dependency, "Dependency")
	eq("0-9:1%4", d.Array, "Array")
	eq("custos-x", d.Name, "Name")
	if d.TimeLimit == nil || d.TimeLimit.Number == nil ||
		*d.TimeLimit.Number != 240 {
		t.Errorf("TimeLimit = %+v, want 240m", d.TimeLimit)
	}
	if d.Environment == nil || len(*d.Environment) != 1 ||
		(*d.Environment)[0] != "A=1" {
		t.Errorf("Environment = %v", d.Environment)
	}
	if d.MemoryPerNode == nil || *d.MemoryPerNode.Number != 4096 {
		t.Errorf("MemoryPerNode = %+v", d.MemoryPerNode)
	}
	if d.Nice == nil || *d.Nice != -5 {
		t.Errorf("Nice = %v", d.Nice)
	}
}

// TestJobDescOmitsZero verifies an empty submission leaves everything
// unset (no implicit defaults reach Slurm).
func TestJobDescOmitsZero(t *testing.T) {
	d := toJobDesc(slurm.JobSubmission{Script: "x"})
	v := reflect.ValueOf(*d)
	for i := 0; i < v.NumField(); i++ {
		if !v.Field(i).IsNil() &&
			v.Type().Field(i).Name != "Script" {
			t.Errorf("field %s unexpectedly set", v.Type().Field(i).Name)
		}
	}
}

// TestSubmitRequestShape marshals the wire request and checks the JSON
// keys — the request body must carry only allow-listed job fields.
func TestSubmitRequestShape(t *testing.T) {
	body, err := json.Marshal(api.V0045JobSubmitReq{
		Job: toJobDesc(fullSubmission()),
	})
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Job    map[string]json.RawMessage `json:"job"`
		Script *string                    `json:"script"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Script != nil {
		t.Error("top-level script field set (script belongs inside job)")
	}
	for k := range wire.Job {
		if k == "mail_type" || k == "mail_user" || k == "user_id" ||
			k == "group_id" || k == "get_user_environment" {
			t.Errorf("forbidden wire field %q present", k)
		}
	}
}
