package db_test

import (
	"testing"

	"github.com/Exonical/custos/internal/platform/db"
)

func TestJSONOrNil(t *testing.T) {
	if got := db.JSONOrNil(nil); got != nil {
		t.Fatalf("nil: got %q, want nil", got)
	}
	if got := string(db.JSONOrNil(map[string]float64{"cpu": 1.5})); got != `{"cpu":1.5}` {
		t.Fatalf("map: got %s", got)
	}
	var m map[string]float64
	if got := string(db.JSONOrNil(m)); got != "null" {
		t.Fatalf("typed nil map: got %s, want null", got)
	}
}
