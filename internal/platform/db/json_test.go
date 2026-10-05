package db_test

import (
	"math"
	"testing"

	"github.com/Exonical/custos/internal/platform/db"
)

func TestJSONOrNil(t *testing.T) {
	got, err := db.JSONOrNil(nil)
	if err != nil || got != nil {
		t.Fatalf("nil: got %q, %v; want nil, nil", got, err)
	}
	got, err = db.JSONOrNil(map[string]float64{"cpu": 1.5})
	if err != nil || string(got) != `{"cpu":1.5}` {
		t.Fatalf("map: got %s, %v", got, err)
	}
	var m map[string]float64
	got, err = db.JSONOrNil(m)
	if err != nil || string(got) != "null" {
		t.Fatalf("typed nil map: got %s, %v; want null", got, err)
	}
}

func TestJSONOrNilMarshalError(t *testing.T) {
	got, err := db.JSONOrNil(map[string]float64{"cpu": math.NaN()})
	if err == nil {
		t.Fatalf("NaN: got %q, want marshal error", got)
	}
	if got != nil {
		t.Fatalf("NaN: got %q, want nil bytes", got)
	}
}
