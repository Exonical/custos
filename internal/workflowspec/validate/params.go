package validate

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/Exonical/custos/internal/workflowspec"
)

// ResolveParameters coerces raw request parameters against the spec's
// declared Parameter set (VALIDATING stage of execution.advance):
// type coercion, required, default, pattern, minimum/maximum, enum —
// all errors are returned at once under spec.parameters.<name>.
func ResolveParameters(declared map[string]workflowspec.Parameter,
	raw []byte) (map[string]any, []FieldError) {
	var errs []FieldError
	fe := func(name, code, msg string) {
		errs = append(errs, FieldError{
			Path: "spec.parameters." + name, Code: code, Message: msg})
	}
	var provided map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &provided); err != nil {
			fe("", "PARAMETERS_INVALID",
				"parameters body is not a JSON object")
			return nil, errs
		}
	}
	out := map[string]any{}
	for name, v := range provided {
		if _, ok := declared[name]; !ok {
			fe(name, "PARAMETER_UNKNOWN", "parameter is not declared")
			continue
		}
		out[name] = v
	}
	for name, p := range declared {
		v, present := provided[name]
		if !present {
			if p.Default != nil {
				out[name] = p.Default
				continue
			}
			if p.Required {
				fe(name, "PARAMETER_REQUIRED",
					"parameter "+name+" is required")
			}
			continue
		}
		switch p.Type {
		case "string":
			s, ok := v.(string)
			if !ok {
				fe(name, "PARAMETER_TYPE", "expected string")
				continue
			}
			if p.Pattern != "" {
				if re, err := regexp.Compile(p.Pattern); err == nil &&
					!re.MatchString(s) {
					fe(name, "PARAMETER_PATTERN",
						"value does not match "+p.Pattern)
				}
			}
		case "integer":
			f, ok := v.(float64)
			if !ok || f != float64(int64(f)) {
				fe(name, "PARAMETER_TYPE", "expected integer")
				continue
			}
			n := int64(f)
			if p.Minimum != nil && n < *p.Minimum {
				fe(name, "PARAMETER_RANGE",
					fmt.Sprintf("below minimum %d", *p.Minimum))
			}
			if p.Maximum != nil && n > *p.Maximum {
				fe(name, "PARAMETER_RANGE",
					fmt.Sprintf("above maximum %d", *p.Maximum))
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				fe(name, "PARAMETER_TYPE", "expected boolean")
				continue
			}
		default:
			fe(name, "PARAMETER_TYPE",
				"declared type "+p.Type+" is unsupported")
			continue
		}
		if len(p.Enum) > 0 {
			found := false
			for _, e := range p.Enum {
				if fmt.Sprint(e) == fmt.Sprint(v) {
					found = true
				}
			}
			if !found {
				fe(name, "PARAMETER_ENUM",
					"value is not in the declared enum")
			}
		}
	}
	return out, errs
}
