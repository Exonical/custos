package apperr

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestKindOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Kind
	}{
		{"nil", nil, Unknown},
		{"plain error", errors.New("boom"), Unknown},
		{"typed", New(NotFound, "x", "gone"), NotFound},
		{"wrapped typed", fmt.Errorf("outer: %w", New(Conflict, "x", "dup")), Conflict},
		{"double wrapped", fmt.Errorf("a: %w", fmt.Errorf("b: %w", New(Invalid, "x", "bad"))), Invalid},
		{"context canceled", context.Canceled, Unavailable},
		{"context deadline", context.DeadlineExceeded, Unavailable},
		{"wrapped canceled", fmt.Errorf("op: %w", context.DeadlineExceeded), Unavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KindOf(tt.err); got != tt.want {
				t.Fatalf("KindOf = %v, want %v", got, tt.want)
			}
			if !Is(tt.err, tt.want) {
				t.Fatalf("Is(err, %v) = false", tt.want)
			}
		})
	}
}

func TestWrapAndUnwrap(t *testing.T) {
	cause := errors.New("disk full")
	e := Wrap(cause, Unavailable, "db.unavailable", "database unavailable")
	if e.Error() != "database unavailable: disk full" {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, cause) {
		t.Fatal("Unwrap did not expose cause")
	}
	if e.Unwrap() != cause {
		t.Fatal("Unwrap returned wrong error")
	}
}

func TestKindString(t *testing.T) {
	for k, want := range map[Kind]string{
		Invalid: "invalid", NotFound: "not_found", Conflict: "conflict",
		Forbidden: "forbidden", Unauthenticated: "unauthenticated",
		Unavailable: "unavailable", RateLimited: "rate_limited",
		Internal: "internal", Unknown: "unknown",
	} {
		if k.String() != want {
			t.Fatalf("Kind(%d).String() = %q, want %q", k, k.String(), want)
		}
	}
}
