package sbatchscan

import (
	"context"
	"fmt"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/validation"
	"github.com/Exonical/custos/internal/workflowspec"
)

// Mode selects how directives are judged.
type Mode int

const (
	// ModeReject turns every directive option into a SECURITY_VIOLATION.
	ModeReject Mode = iota
	// ModeImport converts recognized directives into a resource request
	// (M5; not implemented yet).
	ModeImport
)

// fixHints map field families to the Resources-panel fix hint.
var fixHints = map[Field]string{
	FieldAccount:       "Configure the account in the project cluster binding",
	FieldPartition:     "Configure the partition using Placement → Partition",
	FieldQoS:           "Configure QoS using Placement → QoS",
	FieldReservation:   "Configure the reservation using Placement → Reservation",
	FieldNodes:         "Configure node count using Resources → Nodes",
	FieldTasks:         "Configure task count using Resources → Tasks",
	FieldTasksPerNode:  "Configure tasks per node using Resources → Tasks per node",
	FieldCPUsPerTask:   "Configure CPUs per task using Resources → CPUs per task",
	FieldMemory:        "Configure memory using Resources → Memory",
	FieldGRES:          "Configure GPUs using Resources → GPU",
	FieldConstraints:   "Configure constraints using Resources → Constraints",
	FieldLicenses:      "Configure licenses using Resources → Licenses",
	FieldWalltime:      "Configure walltime using Resources → Walltime",
	FieldArray:         "Configure the array using Resources → Array",
	FieldDependencies:  "Configure dependencies in the workflow definition",
	FieldPriority:      "Priority is set by policy, not by scripts",
	FieldExclusive:     "Configure exclusivity using Resources → Exclusive",
	FieldNodeSelection: "Node selection is decided by placement",
	FieldTopology:      "Topology is decided by placement",
	FieldCluster:       "Cluster selection is decided by placement",
	FieldIdentityEnv:   "Configure environment variables in the task env field",
	FieldNaming:        "Naming is assigned by Custos for correlation",
	FieldIOPaths:       "Configure I/O paths in the task definition",
	FieldMail:          "Mail notification is disabled",
	FieldBehavior:      "Scheduler behaviour is controlled by Custos",
}

const rejectMsg = "Scheduler resource directives must be configured through the Custos resource panel."

// Judge converts scan findings into diagnostics under the given mode.
// Reject mode: every option yields SECURITY_VIOLATION with the
// field-family code; unrecognized options yield CUSTOS199.
func Judge(res ScanResult, mode Mode) ([]validation.Diagnostic, error) {
	if mode == ModeImport {
		return nil, apperr.New(apperr.Internal, "INTERNAL",
			"sbatchscan import mode not implemented")
	}
	var out []validation.Diagnostic
	out = append(out, res.Diagnostics...)
	for _, d := range res.Directives {
		note := ""
		if !d.Honored {
			note = " (not honored by sbatch here; rejected regardless)"
		}
		for _, po := range d.Options {
			code := FieldCode(po.Field)
			msg := rejectMsg + note
			if po.Abbrev {
				msg += fmt.Sprintf(" Option --%s resolves to --%s by prefix abbreviation.",
					po.Name[2:], po.Option.Long)
			}
			out = append(out, validation.Diagnostic{
				Source: "sbatchscan", Code: code,
				Severity: validation.SeveritySecurity,
				Line:     d.Line, Column: d.Column,
				Field:   string(po.Field),
				Message: msg,
				Fix:     fixHints[po.Field],
			})
		}
		for _, u := range d.Unknown {
			out = append(out, validation.Diagnostic{
				Source: "sbatchscan", Code: "CUSTOS199",
				Severity: validation.SeveritySecurity,
				Line:     d.Line, Column: d.Column,
				Field:   string(FieldUnknown),
				Message: fmt.Sprintf("Unknown scheduler directive option %s.%s", u, note),
				Fix:     "Remove the directive; configure resources in the Custos panel",
			})
		}
	}
	validation.SortDiagnostics(out)
	return out, nil
}

// Validator adapts the scanner to validation.ScriptValidator (reject
// mode; import mode is a separate endpoint concern).
type Validator struct{}

// Name returns the validator source name.
func (Validator) Name() string { return "sbatchscan" }

// Languages supported: shell payloads only.
func (Validator) Languages() []workflowspec.Language {
	return []workflowspec.Language{workflowspec.LanguageBash, workflowspec.LanguageSh}
}

// Validate scans the script and judges directives in reject mode.
func (Validator) Validate(ctx context.Context, in validation.Input) (validation.Result, error) {
	res := validation.Result{Tool: validation.ToolVersion{
		Name: "sbatchscan", Version: "slurm-26.05"}}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	scan, err := Scan(in.Script, in.Language)
	if err != nil {
		return res, err
	}
	diags, err := Judge(scan, ModeReject)
	if err != nil {
		return res, err
	}
	res.Diagnostics = diags
	return res, nil
}
