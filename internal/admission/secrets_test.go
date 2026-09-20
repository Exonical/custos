package admission_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/admission"
)

func TestSecretRefsAffectDigest(t *testing.T) {
	base := admission.ExecutionSpec{ID: uuid.New(), TenantID: uuid.New(),
		Environment: admission.EnvSet{SecretRefs: []admission.SecretEnvRef{{
			Name: "HF_TOKEN", ReferenceID: uuid.New(), Mode: "env", Handle: "hf"}}}}
	a := base
	if err := a.Freeze(); err != nil {
		t.Fatal(err)
	}
	b := base
	b.Environment.SecretRefs[0].ReferenceID = uuid.New()
	if err := b.Freeze(); err != nil {
		t.Fatal(err)
	}
	if a.Digest == b.Digest {
		t.Fatal("reference id did not affect digest")
	}
	c := base
	c.Environment.SecretRefs[0].Mode = "wrapped_token"
	if err := c.Freeze(); err != nil {
		t.Fatal(err)
	}
	if a.Digest == c.Digest {
		t.Fatal("delivery mode did not affect digest")
	}
}

func TestExecutionSpecHasNoSecretValueStorage(t *testing.T) {
	var walk func(reflect.Type)
	seen := map[reflect.Type]bool{}
	walk = func(typ reflect.Type) {
		if typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}
		if seen[typ] {
			return
		}
		seen[typ] = true
		if typ.Kind() == reflect.Slice && typ.Elem().Kind() == reflect.Uint8 {
			t.Fatalf("[]byte field reachable through %v", typ)
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Type == reflect.TypeOf([]byte(nil)) {
				t.Fatalf("secret-capable []byte field %s", f.Name)
			}
			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(admission.ExecutionSpec{}))
}
