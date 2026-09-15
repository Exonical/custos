package db

import "testing"

func TestVerifyCAOnlyEmptyCerts(t *testing.T) {
	if err := verifyCAOnly(nil, nil); err == nil {
		t.Fatal("expected error for empty peer certificate list")
	}
}
