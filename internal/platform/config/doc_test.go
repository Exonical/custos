package config

import (
	"reflect"
	"testing"
	"time"
)

// Every config field must carry a doc tag — docs/configuration.md is
// generated from them.
func TestDocTagsComplete(t *testing.T) {
	var check func(t reflect.Type, path string)
	check = func(typ reflect.Type, path string) {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Tag.Get("yaml") == "-" {
				continue
			}
			name := path + f.Tag.Get("yaml")
			if f.Tag.Get("doc") == "" {
				t.Errorf("%s: missing doc tag", name)
			}
			if f.Type.Kind() == reflect.Struct &&
				f.Type != reflect.TypeOf(time.Duration(0)) {
				check(f.Type, name+".")
			}
		}
	}
	check(reflect.TypeOf(Config{}), "")
}
