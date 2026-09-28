package safehttp

import (
	"context"
	"net"
	"testing"

	"github.com/Exonical/custos/internal/platform/apperr"
)

func withLookup(t *testing.T, ips []net.IP) {
	t.Helper()
	orig := lookupIP
	lookupIP = func(context.Context, string) ([]net.IP, error) {
		return ips, nil
	}
	t.Cleanup(func() { lookupIP = orig })
}

func TestVetHostMixedAnswersFailClosed(t *testing.T) {
	// A name resolving to public + private is a rebinding smell: one
	// denied address poisons the whole answer.
	withLookup(t, []net.IP{
		net.ParseIP("203.0.113.7"), net.ParseIP("10.0.0.9"),
	})
	if _, err := VetHost(context.Background(), "example.test", DialPolicy{},
		false); !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("mixed answers: want Forbidden, got %v", err)
	}

	// Same with link-local mixed in.
	withLookup(t, []net.IP{
		net.ParseIP("203.0.113.7"), net.ParseIP("169.254.1.5"),
	})
	if _, err := VetHost(context.Background(), "example.test",
		DialPolicy{AllowPrivate: true}, false); !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("link-local mix: want Forbidden, got %v", err)
	}
}

func TestVetHostAllPermittedReturnsFirst(t *testing.T) {
	withLookup(t, []net.IP{
		net.ParseIP("203.0.113.7"), net.ParseIP("203.0.113.8"),
	})
	a, err := VetHost(context.Background(), "example.test", DialPolicy{}, false)
	if err != nil {
		t.Fatalf("all-permitted answer denied: %v", err)
	}
	if a.String() != "203.0.113.7" {
		t.Fatalf("want first address 203.0.113.7, got %s", a)
	}
}

func TestVetHostEmptyAnswerDenied(t *testing.T) {
	withLookup(t, nil)
	if _, err := VetHost(context.Background(), "example.test", DialPolicy{},
		false); !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("empty answer: want Forbidden, got %v", err)
	}
}
