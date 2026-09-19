package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Exonical/custos/internal/platform/apperr"
)

var (
	durationType = reflect.TypeFor[time.Duration]()
	secretType   = reflect.TypeFor[Secret]()
)

func yamlTag(f reflect.StructField) (string, bool) {
	tag := f.Tag.Get("yaml")
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return "", false
	}
	if name == "" {
		name = strings.ToLower(f.Name)
	}
	return name, true
}

// applyYAMLNode walks the document and assigns fields by yaml tag.
// Mapping keys that match no field are errors (KnownFields behavior).
func applyYAMLNode(cfg *Config, doc *yaml.Node) error {
	if len(doc.Content) == 0 {
		return nil
	}
	return applyYAMLValue(reflect.ValueOf(cfg).Elem(), doc.Content[0], "")
}

func applyYAMLValue(sv reflect.Value, n *yaml.Node, path string) error {
	switch {
	case n.Kind == yaml.AliasNode:
		return applyYAMLValue(sv, n.Alias, path)
	case sv.Kind() == reflect.Pointer:
		if sv.IsNil() {
			sv.Set(reflect.New(sv.Type().Elem()))
		}
		return applyYAMLValue(sv.Elem(), n, path)
	case sv.Kind() == reflect.Struct && n.Kind == yaml.MappingNode:
		st := sv.Type()
		for i := 0; i < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			fv, fpath, ok := fieldByYAMLTag(sv, st, key.Value, path)
			if !ok {
				return fmt.Errorf("unknown field %q", joinPath(path, key.Value))
			}
			if err := applyYAMLValue(fv, val, fpath); err != nil {
				return err
			}
		}
		return nil
	case sv.Kind() == reflect.Map && n.Kind == yaml.MappingNode:
		if sv.IsNil() {
			sv.Set(reflect.MakeMap(sv.Type()))
		}
		et := sv.Type().Elem()
		for i := 0; i < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			ev := reflect.New(et).Elem()
			if err := applyYAMLValue(ev, val, joinPath(path, key.Value)); err != nil {
				return err
			}
			sv.SetMapIndex(reflect.ValueOf(key.Value), ev)
		}
		return nil
	case sv.Kind() == reflect.Slice && sv.Type().Elem().Kind() == reflect.String && n.Kind == yaml.SequenceNode:
		out := reflect.MakeSlice(sv.Type(), 0, len(n.Content))
		for _, item := range n.Content {
			s, err := scalarString(item, path)
			if err != nil {
				return err
			}
			out = reflect.Append(out, reflect.ValueOf(s))
		}
		sv.Set(out)
		return nil
	default:
		s, err := scalarString(n, path)
		if err != nil {
			return err
		}
		return setScalar(sv, s, path)
	}
}

func fieldByYAMLTag(sv reflect.Value, st reflect.Type, key, path string) (reflect.Value, string, bool) {
	for i := range st.NumField() {
		f := st.Field(i)
		name, ok := yamlTag(f)
		if !ok {
			continue
		}
		if name == key {
			return sv.Field(i), joinPath(path, name), true
		}
	}
	return reflect.Value{}, "", false
}

func scalarString(n *yaml.Node, path string) (string, error) {
	if n.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("%s: expected a scalar value", path)
	}
	return n.Value, nil
}

func setScalar(fv reflect.Value, raw, path string) error {
	if fv.Type() == secretType {
		fv.SetString(raw)
		return nil
	}
	if fv.Type() == durationType {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("%s: invalid duration %q", path, raw)
		}
		fv.SetInt(int64(d))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("%s: invalid bool %q", path, raw)
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int32, reflect.Int64:
		i, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("%s: invalid integer %q", path, raw)
		}
		fv.SetInt(i)
	case reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("%s: invalid float %q", path, raw)
		}
		fv.SetFloat(f)
	case reflect.Slice:
		// env fallback path: comma-separated
		if fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("%s: unsupported field type", path)
		}
		out := reflect.MakeSlice(fv.Type(), 0, 4)
		for _, p := range strings.Split(raw, ",") {
			out = reflect.Append(out, reflect.ValueOf(strings.TrimSpace(p)))
		}
		fv.Set(out)
	default:
		return fmt.Errorf("%s: unsupported field type %s", path, fv.Type())
	}
	return nil
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// applyEnv overlays CUSTOS_* variables. Nesting uses `__` and names are
// the uppercase of yaml tags (CUSTOS_SERVER__LISTEN). Secret fields also
// accept <NAME>_FILE pointing at a file; setting both is an error.
// Map fields (worker.kinds) are not supported via env.
func applyEnv(cfg *Config, lookup LookupEnv) error {
	return walkEnv(reflect.ValueOf(cfg).Elem(), "CUSTOS", "", lookup)
}

func walkEnv(sv reflect.Value, prefix, path string, lookup LookupEnv) error {
	st := sv.Type()
	for i := range st.NumField() {
		f := st.Field(i)
		name, ok := yamlTag(f)
		if !ok {
			continue
		}
		fv := sv.Field(i)
		sep := "__"
		if prefix == "CUSTOS" {
			sep = "_" // root: CUSTOS_X; nested: CUSTOS_X__Y
		}
		envName := prefix + sep + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		fpath := joinPath(path, name)
		switch {
		case fv.Kind() == reflect.Pointer && !fv.IsNil() && fv.Type().Elem().Kind() == reflect.Struct:
			if err := walkEnv(fv.Elem(), envName, fpath, lookup); err != nil {
				return err
			}
		case fv.Kind() == reflect.Pointer:
			continue
		case fv.Kind() == reflect.Struct && fv.Type() != durationType:
			if err := walkEnv(fv, envName, fpath, lookup); err != nil {
				return err
			}
		case fv.Kind() == reflect.Map:
			continue // maps unsupported via env; documented
		default:
			if err := applyEnvLeaf(fv, envName, fpath, lookup); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyEnvLeaf(fv reflect.Value, envName, path string, lookup LookupEnv) error {
	raw, ok := lookup(envName)
	isSecret := fv.Type() == secretType
	if isSecret {
		filePath, hasFile := lookup(envName + "_FILE")
		if ok && hasFile {
			return apperr.New(apperr.Invalid, "config.env",
				fmt.Sprintf("%s: both %s and %s_FILE are set", path, envName, envName))
		}
		if hasFile {
			b, err := os.ReadFile(filePath) // #nosec G304 -- _FILE path is operator-supplied config
			if err != nil {
				return apperr.Wrap(err, apperr.Invalid, "config.env",
					fmt.Sprintf("%s: cannot read %s_FILE", path, envName))
			}
			raw, ok = strings.TrimSpace(string(b)), true
		}
	}
	if !ok {
		return nil
	}
	if err := setScalar(fv, raw, path); err != nil {
		return apperr.Wrap(err, apperr.Invalid, "config.env", "invalid environment override")
	}
	return nil
}
