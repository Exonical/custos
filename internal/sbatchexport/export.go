// Package sbatchexport renders a portable sbatch script from a workflow
// task. It deliberately emits no tenant account: sites configure that at
// the destination Slurm installation.
package sbatchexport

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Exonical/custos/internal/workflowspec"
	"github.com/Exonical/custos/internal/workflowspec/expr"
)

// UnsupportedError identifies a workflow field that cannot be faithfully
// represented by a standalone Slurm script.
type UnsupportedError struct {
	Field  string
	Reason string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("EXPORT_UNSUPPORTED %s: %s", e.Field, e.Reason)
}

func unsupported(field, reason string) error {
	return &UnsupportedError{Field: field, Reason: reason}
}

var shellNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var directiveSafeRE = regexp.MustCompile(`^[A-Za-z0-9_./%:+,-]+$`)

// RenderOptions carries version context required by run.version references.
type RenderOptions struct {
	WorkflowVersion *int
}

// Render creates a standalone script for one task. Expressions that depend
// on the execution DAG or secret values are rejected rather than omitted.
func Render(w workflowspec.Workflow, task workflowspec.Task, script []byte) (string, error) {
	return RenderWithOptions(w, task, script, RenderOptions{})
}

// RenderWithOptions creates a standalone script using caller-supplied context.
func RenderWithOptions(w workflowspec.Workflow, task workflowspec.Task, script []byte,
	options RenderOptions) (string, error) {
	if task.Image != nil {
		return "", unsupported("spec.tasks."+task.Name+".image", "container images require a cluster-selected runtime and cannot be exported portably")
	}
	if task.Multinode != nil {
		return "", unsupported("spec.tasks."+task.Name+".multinode", "multinode launch configuration is not supported by sbatch export")
	}
	if task.Type == "condition" {
		return "", unsupported("spec.tasks."+task.Name+".type", "condition tasks do not map to Slurm")
	}
	if task.Type == "stageIn" || task.Type == "stageOut" || task.Type == "interactive" {
		return "", unsupported("spec.tasks."+task.Name+".type", "task type is not exportable in v1alpha1")
	}
	launch := task.EffectiveLaunch()
	if launch != workflowspec.LaunchSbatch && launch != workflowspec.LaunchSrun {
		return "", unsupported("spec.tasks."+task.Name+".launch", "launch must be sbatch or srun")
	}
	if (len(task.Command) > 0) == (task.Script != nil) {
		return "", unsupported("spec.tasks."+task.Name, "exactly one of command or script is required")
	}
	if task.Script != nil && task.Script.HasInline() {
		script = []byte(task.Script.Inline)
	}
	if task.Script != nil && len(script) == 0 {
		return "", unsupported("spec.tasks."+task.Name+".script", "script payload is unavailable")
	}

	resources, err := resolveResources(task)
	if err != nil {
		return "", err
	}
	language := workflowspec.LanguageBash
	if task.Script != nil {
		language = task.Script.EffectiveLanguage()
	}
	if affinity := task.Resources.CPUAffinity; affinity != "" && affinity != "none" && launch != workflowspec.LaunchSrun {
		return "", unsupported("spec.tasks."+task.Name+".resources.cpuAffinity", "CPU affinity export requires srun")
	}
	pythonScript := task.Script != nil && language == workflowspec.LanguagePython
	if task.Script != nil && language != workflowspec.LanguageBash &&
		language != workflowspec.LanguageSh && !pythonScript {
		return "", unsupported("spec.tasks."+task.Name+".script.language", "only bash, sh, and python payloads can be exported")
	}

	workingDirectory := task.WorkingDirectory
	if workingDirectory == "" && w.Spec.Defaults != nil {
		workingDirectory = w.Spec.Defaults.WorkingDirectory
	}
	workingDirectoryTemplated := strings.Contains(workingDirectory, "{{")

	lines := make([]string, 0, 40)
	shebang := "#!/bin/bash"
	if task.Script != nil && language == workflowspec.LanguageSh {
		shebang = "#!/bin/sh"
	}
	if pythonScript {
		shebang = "#!/usr/bin/env python3"
	}
	lines = append(lines, shebang)
	lines = append(lines,
		"# Exported from workflow: "+w.Metadata.Name,
		"# Task: "+task.Name,
		"# Account is site-managed; configure it at the destination Slurm site.")
	if task.Service != nil {
		lines = append(lines,
			"# Custos service lifecycle and autoStop behavior are not represented by this standalone export.")
	}
	if len(task.DependsOn) > 0 {
		lines = append(lines, "# Custos dependencies are not represented: "+strings.Join(task.DependsOn, ", "))
	}
	if task.When != "" {
		lines = append(lines, "# Custos when expression is not represented: "+task.When)
	}
	if task.FanOut != nil {
		lines = append(lines, "# Custos fanOut is not represented by this standalone export.")
	}
	if task.Retry != nil {
		lines = append(lines, "# Custos retry policy is not represented by this standalone export.")
	}
	if len(w.Spec.Secrets) > 0 {
		lines = append(lines, "# Custos secret references are not represented; configure required values at the site.")
	}
	if len(task.Software) > 0 {
		software := make([]string, 0, len(task.Software))
		for _, requirement := range task.Software {
			software = append(software, requirement.Name+"@"+requirement.Version)
		}
		lines = append(lines, "# requires software: "+strings.Join(software, ", ")+" (load your site's modules)")
	}

	directives := []string{"#SBATCH --job-name=" + directiveValue(task.Name)}
	if resources.Nodes > 0 {
		directives = append(directives, fmt.Sprintf("#SBATCH --nodes=%d", resources.Nodes))
	}
	if resources.Tasks > 0 {
		directives = append(directives, fmt.Sprintf("#SBATCH --ntasks=%d", resources.Tasks))
	}
	if resources.TasksPerNode > 0 {
		directives = append(directives, fmt.Sprintf("#SBATCH --ntasks-per-node=%d", resources.TasksPerNode))
	}
	if resources.CPUsPerTask > 0 {
		directives = append(directives, fmt.Sprintf("#SBATCH --cpus-per-task=%d", resources.CPUsPerTask))
	}
	if resources.MemoryPerNodeMiB > 0 {
		directives = append(directives, fmt.Sprintf("#SBATCH --mem=%dM", resources.MemoryPerNodeMiB))
	}
	if resources.MemoryPerCPUMiB > 0 {
		directives = append(directives, fmt.Sprintf("#SBATCH --mem-per-cpu=%dM", resources.MemoryPerCPUMiB))
	}
	if resources.GPU != nil && resources.GPU.Count > 0 {
		gres := "gpu"
		if resources.GPU.Type != "" {
			gres += ":" + resources.GPU.Type
		}
		gres += ":" + strconv.Itoa(resources.GPU.Count)
		directives = append(directives, "#SBATCH --gres="+directiveValue(gres))
	}
	if resources.Walltime > 0 {
		directives = append(directives, "#SBATCH --time="+formatSlurmTime(time.Duration(resources.Walltime)))
	}
	if task.Array != nil {
		directives = append(directives, "#SBATCH --array="+formatArray(task.Array))
	}
	partition := task.Partition
	qos := task.QoS
	if w.Spec.Defaults != nil {
		if partition == "" {
			partition = w.Spec.Defaults.Partition
		}
		if qos == "" {
			qos = w.Spec.Defaults.QoS
		}
	}
	if partition != "" {
		directives = append(directives, "#SBATCH --partition="+directiveValue(partition))
	}
	if qos != "" {
		directives = append(directives, "#SBATCH --qos="+directiveValue(qos))
	}
	if resources.Exclusive {
		directives = append(directives, "#SBATCH --exclusive")
	}
	if resources.Constraints != "" {
		directives = append(directives, "#SBATCH --constraint="+directiveValue(resources.Constraints))
	}
	if len(resources.Licenses) > 0 {
		directives = append(directives, "#SBATCH --licenses="+directiveValue(strings.Join(resources.Licenses, ",")))
	}
	if task.Stdout != "" {
		directives = append(directives, "#SBATCH --output="+directiveValue(task.Stdout))
	}
	if task.Stderr != "" {
		directives = append(directives, "#SBATCH --error="+directiveValue(task.Stderr))
	}
	if workingDirectory != "" && !workingDirectoryTemplated {
		directives = append(directives, "#SBATCH --chdir="+directiveValue(workingDirectory))
	}
	lines = append(lines, directives...)
	lines = append(lines, "")
	if pythonScript {
		lines = append(lines, "import os")
	}

	parameterLines, parameterVars, err := renderParameters(w, pythonScript)
	if err != nil {
		return "", err
	}
	lines = append(lines, parameterLines...)
	environment := make(map[string]string)
	if w.Spec.Defaults != nil {
		for name, value := range w.Spec.Defaults.Env {
			environment[name] = value
		}
	}
	for name, value := range task.Env {
		environment[name] = value
	}
	envLines, err := renderEnvironment(w, task, resources, environment, parameterVars, pythonScript, options)
	if err != nil {
		return "", err
	}
	lines = append(lines, envLines...)
	if workingDirectoryTemplated {
		cd, err := renderValue(w, task, resources, parameterVars, workingDirectory, "spec.tasks."+task.Name+".workingDirectory", pythonScript, options)
		if err != nil {
			return "", err
		}
		if pythonScript {
			lines = append(lines, "os.chdir("+cd+")")
		} else {
			lines = append(lines, "cd "+cd)
		}
	}
	body, err := renderBody(w, task, resources, parameterVars, script, pythonScript, options)
	if err != nil {
		return "", err
	}
	lines = append(lines, body...)
	return strings.Join(lines, "\n") + "\n", nil
}

func resolveResources(task workflowspec.Task) (workflowspec.Resources, error) {
	if task.Resources.Empty() && task.Multinode == nil {
		return workflowspec.Resources{}, nil
	}
	resources, errs := task.ResolveResources("spec.tasks." + task.Name + ".resources")
	if len(errs) > 0 {
		return workflowspec.Resources{}, unsupported(errs[0].Path, errs[0].Message)
	}
	return resources, nil
}

func renderParameters(w workflowspec.Workflow, python bool) ([]string, map[string]string, error) {
	names := make([]string, 0, len(w.Spec.Parameters))
	for name := range w.Spec.Parameters {
		names = append(names, name)
	}
	sort.Strings(names)
	reserved := make(map[string]bool, len(names))
	for _, name := range names {
		if shellNameRE.MatchString(name) && (!python || pythonIdentifier(name)) {
			reserved[name] = true
		}
	}
	lines := make([]string, 0, len(names)+2)
	variables := make(map[string]string, len(names))
	used := make(map[string]bool, len(names))
	for _, name := range names {
		parameter := w.Spec.Parameters[name]
		variable := name
		if !shellNameRE.MatchString(name) || (python && !pythonIdentifier(name)) {
			variable = "CUSTOS_PARAM_" + strings.ToUpper(hex.EncodeToString([]byte(name)))
			for reserved[variable] || used[variable] {
				variable += "_"
			}
		}
		used[variable] = true
		variables[name] = variable
		if python {
			if parameter.Default == nil && parameter.Required {
				lines = append(lines, fmt.Sprintf("%s = os.environ.get(%s)", variable, strconv.Quote(name)))
				lines = append(lines, fmt.Sprintf("if %s is None: raise SystemExit(%s)", variable, strconv.Quote("required parameter "+name)))
			} else {
				fallback, err := pythonDefault(parameter.Default)
				if err != nil {
					return nil, nil, unsupported("spec.parameters."+name+".default", err.Error())
				}
				lines = append(lines, fmt.Sprintf("%s = os.environ.get(%s) or %s", variable, strconv.Quote(name), fallback))
			}
			continue
		}
		if parameter.Default == nil && parameter.Required {
			lines = append(lines, fmt.Sprintf("%s=\"${%s:?required parameter %s}\"", variable, variable, name))
			continue
		}
		fallback, err := shellDefault(parameter.Default)
		if err != nil {
			return nil, nil, unsupported("spec.parameters."+name+".default", err.Error())
		}
		lines = append(lines, fmt.Sprintf("%s=\"${%s:-%s}\"", variable, variable, escapeDoubleQuoted(fallback)))
	}
	return lines, variables, nil
}

func renderEnvironment(w workflowspec.Workflow, task workflowspec.Task, resources workflowspec.Resources,
	environment map[string]string, parameterVars map[string]string, python bool,
	options RenderOptions) ([]string, error) {
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		if !envNameRE.MatchString(name) {
			return nil, unsupported("spec.tasks."+task.Name+".env."+name, "environment name is not portable")
		}
		value, err := renderValue(w, task, resources, parameterVars, environment[name], "spec.tasks."+task.Name+".env."+name, python, options)
		if err != nil {
			return nil, err
		}
		if python {
			lines = append(lines, fmt.Sprintf("os.environ[%s] = %s", strconv.Quote(name), value))
		} else {
			lines = append(lines, "export "+name+"="+value)
		}
	}
	return lines, nil
}

func renderBody(w workflowspec.Workflow, task workflowspec.Task, resources workflowspec.Resources,
	parameterVars map[string]string, script []byte, python bool,
	options RenderOptions) ([]string, error) {
	field := "spec.tasks." + task.Name
	args := make([]string, 0, len(task.Command)+len(task.Args))
	for i, value := range task.Command {
		rendered, err := renderValue(w, task, resources, parameterVars, value,
			fmt.Sprintf("%s.command[%d]", field, i), python, options)
		if err != nil {
			return nil, err
		}
		args = append(args, rendered)
	}
	for i, value := range task.Args {
		rendered, err := renderValue(w, task, resources, parameterVars, value,
			fmt.Sprintf("%s.args[%d]", field, i), python, options)
		if err != nil {
			return nil, err
		}
		args = append(args, rendered)
	}
	launchPrefix := srunPrefix(task.EffectiveLaunch(), resources.Tasks, task.Resources.CPUAffinity)
	if python {
		return renderPythonBody(task, script, launchPrefix, args)
	}
	if task.Script == nil {
		if len(args) == 0 {
			return nil, unsupported(field, "command task has no command")
		}
		command := strings.Join(args, " ")
		if launchPrefix != "" {
			command = launchPrefix + " " + command
		}
		return []string{"exec " + command}, nil
	}
	body := stripShebang(script)
	interpreter, err := shellInterpreter(task.Script.EffectiveLanguage())
	if err != nil {
		return nil, unsupported(field+".script.language", err.Error())
	}
	argumentText := strings.Join(args, " ")
	if launchPrefix != "" {
		delimiter := heredocDelimiter(body)
		lines := []string{
			`f=$(mktemp "${TMPDIR:-/tmp}/custos-export.XXXXXX")`,
			`trap 'rm -f "$f"' EXIT`,
			`cat > "$f" <<'` + delimiter + `'`,
			string(body),
			delimiter,
			// No exec: the EXIT trap must still remove the staged payload.
			launchPrefix + " " + interpreter + ` "$f"` + optionalSpace(argumentText),
		}
		return lines, nil
	}
	lines := make([]string, 0, 2)
	if argumentText != "" {
		lines = append(lines, "set -- "+argumentText)
	}
	lines = append(lines, string(body))
	return lines, nil
}

func renderPythonBody(task workflowspec.Task, script []byte, launcher string, args []string) ([]string, error) {
	if task.Script == nil || task.Script.EffectiveLanguage() != workflowspec.LanguagePython {
		return nil, unsupported("spec.tasks."+task.Name+".script.language", "Python export requires a python script payload")
	}
	body := stripShebang(script)
	if launcher == "" {
		lines := []string{"import sys"}
		if len(args) > 0 {
			lines = append(lines, "sys.argv = [sys.argv[0]] + ["+strings.Join(args, ", ")+"]")
		}
		lines = append(lines, string(body))
		return lines, nil
	}
	command := make([]string, 0, len(args)+4)
	for _, token := range strings.Fields(launcher) {
		command = append(command, strconv.Quote(token))
	}
	command = append(command, strconv.Quote("python3"), "script_path")
	command = append(command, args...)
	return []string{
		"import atexit, os, subprocess, tempfile",
		"script_file = tempfile.NamedTemporaryFile(mode='w', encoding='utf-8', prefix='custos-export-', suffix='.py', delete=False)",
		"script_file.write(" + strconv.Quote(string(body)) + ")",
		"script_file.close()",
		"script_path = script_file.name",
		"atexit.register(lambda: os.unlink(script_path))",
		"subprocess.run([" + strings.Join(command, ", ") + "], check=True)",
	}, nil
}

func renderValue(w workflowspec.Workflow, task workflowspec.Task, resources workflowspec.Resources,
	parameterVars map[string]string, source, field string, python bool,
	options RenderOptions) (string, error) {
	var parts []string
	addLiteral := func(value string) {
		if value == "" {
			return
		}
		if python {
			parts = append(parts, strconv.Quote(value))
		} else {
			parts = append(parts, shellQuote(value))
		}
	}
	remaining := source
	for {
		start := strings.Index(remaining, "{{")
		if start < 0 {
			addLiteral(remaining)
			break
		}
		addLiteral(remaining[:start])
		end := strings.Index(remaining[start+2:], "}}")
		if end < 0 {
			return "", unsupported(field, "unclosed template expression")
		}
		inner := strings.TrimSpace(remaining[start+2 : start+2+end])
		expression, err := expr.Parse(inner)
		if err != nil {
			return "", unsupported(field, err.Error())
		}
		path, ok := expression.SoleRef()
		if !ok {
			return "", unsupported(field, "only direct references can be exported; arithmetic and compound expressions are unsupported")
		}
		reference, err := renderReference(w, task, resources, parameterVars, path, field, python, options)
		if err != nil {
			return "", err
		}
		parts = append(parts, reference)
		remaining = remaining[start+2+end+2:]
	}
	if len(parts) == 0 {
		if python {
			return `""`, nil
		}
		return "''", nil
	}
	if python {
		return strings.Join(parts, " + "), nil
	}
	return strings.Join(parts, ""), nil
}

func renderReference(w workflowspec.Workflow, task workflowspec.Task, resources workflowspec.Resources,
	parameterVars map[string]string, ref expr.Path, field string, python bool,
	options RenderOptions) (string, error) {
	switch {
	case len(ref) == 2 && ref[0] == "parameters":
		name, ok := parameterVars[ref[1]]
		if !ok {
			return "", unsupported(field, "references undeclared parameter "+ref[1])
		}
		if python {
			return "str(" + name + ")", nil
		}
		return `"${` + name + `}"`, nil
	case len(ref) == 3 && ref[0] == "task" && ref[1] == "resources":
		value, ok := resourceNumber(resources, ref[2])
		if !ok {
			return "", unsupported(field, "task.resources."+ref[2]+" is not a numeric resource field")
		}
		return strconv.FormatInt(value, 10), nil
	case len(ref) == 2 && ref[0] == "array" && ref[1] == "taskId":
		if task.Array == nil {
			return "", unsupported(field, "array.taskId requires an array spec")
		}
		if python {
			return `os.environ.get("SLURM_ARRAY_TASK_ID", "")`, nil
		}
		return `"$SLURM_ARRAY_TASK_ID"`, nil
	case len(ref) == 2 && ref[0] == "run" && ref[1] == "id":
		if python {
			return `os.environ.get("SLURM_JOB_ID", "")`, nil
		}
		return `"$SLURM_JOB_ID"`, nil
	case len(ref) == 2 && ref[0] == "run" && ref[1] == "workflow":
		if python {
			return strconv.Quote(w.Metadata.Name), nil
		}
		return shellQuote(w.Metadata.Name), nil
	case len(ref) == 2 && ref[0] == "run" && ref[1] == "version":
		if options.WorkflowVersion == nil {
			return "", unsupported(field, "run.version requires a workflow version number")
		}
		return strconv.Itoa(*options.WorkflowVersion), nil
	default:
		return "", unsupported(field, "reference "+ref.String()+" is not representable in a standalone sbatch script")
	}
}

func resourceNumber(resources workflowspec.Resources, field string) (int64, bool) {
	switch field {
	case "cpu", "cpusPerTask":
		return int64(resources.CPUsPerTask), true
	case "nodes":
		return int64(resources.Nodes), true
	case "tasksPerNode":
		return int64(resources.TasksPerNode), true
	case "memoryMiB":
		return resources.MemoryPerNodeMiB, true
	case "walltimeSeconds":
		return int64(time.Duration(resources.Walltime) / time.Second), true
	default:
		return 0, false
	}
}

func shellInterpreter(language workflowspec.Language) (string, error) {
	switch language {
	case workflowspec.LanguageBash:
		return "bash", nil
	case workflowspec.LanguageSh:
		return "sh", nil
	default:
		return "", fmt.Errorf("unsupported shell interpreter for %s", language)
	}
}

func renderPythonHeaderValueDefault(value any) string {
	return fmt.Sprint(value)
}

func pythonDefault(value any) (string, error) {
	if value == nil {
		return `""`, nil
	}
	switch v := value.(type) {
	case string:
		return strconv.Quote(v), nil
	case bool:
		if v {
			return "True", nil
		}
		return "False", nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("unsupported default type %T (%s)", value, renderPythonHeaderValueDefault(value))
	}
}

func shellDefault(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	switch v := value.(type) {
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("unsupported default type %T", value)
	}
}

func pythonIdentifier(name string) bool {
	return shellNameRE.MatchString(name) && !pythonKeyword[name]
}

var pythonKeyword = map[string]bool{
	"False": true, "None": true, "True": true, "and": true, "as": true, "assert": true,
	"async": true, "await": true, "break": true, "class": true, "continue": true,
	"def": true, "del": true, "elif": true, "else": true, "except": true, "finally": true,
	"for": true, "from": true, "global": true, "if": true, "import": true, "in": true,
	"is": true, "lambda": true, "nonlocal": true, "not": true, "or": true, "pass": true,
	"raise": true, "return": true, "try": true, "while": true, "with": true, "yield": true,
}

func srunPrefix(launch string, tasks int, affinity string) string {
	if launch != workflowspec.LaunchSrun {
		return ""
	}
	words := []string{"srun"}
	bind := map[string]string{"core": "cores", "socket": "sockets", "numa": "ldoms"}[affinity]
	if bind != "" {
		words = append(words, "--cpu-bind="+bind)
	}
	if tasks > 0 {
		words = append(words, fmt.Sprintf("--ntasks=%d", tasks))
	}
	return strings.Join(words, " ")
}

func formatSlurmTime(duration time.Duration) string {
	seconds := int64(duration / time.Second)
	days := seconds / 86400
	seconds %= 86400
	hours := seconds / 3600
	seconds %= 3600
	minutes := seconds / 60
	seconds %= 60
	if days > 0 {
		return fmt.Sprintf("%d-%02d:%02d:%02d", days, hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}

func formatArray(array *workflowspec.ArraySpecYAML) string {
	value := fmt.Sprintf("%d-%d", array.Start, array.End)
	if array.Step > 0 {
		value += ":" + strconv.Itoa(array.Step)
	}
	if array.MaxConcurrent > 0 {
		value += "%" + strconv.Itoa(array.MaxConcurrent)
	}
	return value
}

func directiveValue(value string) string {
	if directiveSafeRE.MatchString(value) {
		return value
	}
	return shellQuote(value)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func escapeDoubleQuoted(value string) string {
	var escaped strings.Builder
	escaped.Grow(len(value))
	for _, r := range value {
		if r == '\\' || r == '"' || r == '$' || r == '`' {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(r)
	}
	return escaped.String()
}

func optionalSpace(value string) string {
	if value == "" {
		return ""
	}
	return " " + value
}

func stripShebang(script []byte) []byte {
	line, remainder, found := bytes.Cut(script, []byte("\n"))
	if found && bytes.HasPrefix(line, []byte("#!")) {
		return remainder
	}
	return script
}

func heredocDelimiter(script []byte) string {
	base := "CUSTOS_EXPORTED_SCRIPT"
	for suffix := 0; ; suffix++ {
		candidate := base
		if suffix > 0 {
			candidate += "_" + strconv.Itoa(suffix)
		}
		if !bytes.Contains(script, []byte(candidate)) {
			return candidate
		}
	}
}
