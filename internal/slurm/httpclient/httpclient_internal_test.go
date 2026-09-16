package httpclient

import (
	"context"
	"net"
	"testing"
	"time"

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

func TestVetDialMixedAnswersFailClosed(t *testing.T) {
	d := &net.Dialer{Timeout: 50 * time.Millisecond}

	// A name resolving to public + private is a rebinding smell: one
	// denied address poisons the whole answer.
	withLookup(t, []net.IP{
		net.ParseIP("203.0.113.7"), net.ParseIP("10.0.0.9"),
	})
	_, err := vetDial(context.Background(), d, DialPolicy{},
		"tcp", "example.test:443", false)
	if !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("mixed answers: want Forbidden, got %v", err)
	}

	// Same with link-local mixed in.
	withLookup(t, []net.IP{
		net.ParseIP("203.0.113.7"), net.ParseIP("169.254.1.5"),
	})
	_, err = vetDial(context.Background(), d, DialPolicy{AllowPrivate: true},
		"tcp", "example.test:443", false)
	if !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("link-local mix: want Forbidden, got %v", err)
	}
}

func TestVetDialAllPermittedDialsFirst(t *testing.T) {
	withLookup(t, []net.IP{
		net.ParseIP("203.0.113.7"), net.ParseIP("203.0.113.8"),
	})
	d := &net.Dialer{Timeout: 50 * time.Millisecond}
	_, err := vetDial(context.Background(), d, DialPolicy{},
		"tcp", "example.test:1", false)
	// All addresses permitted → dial attempted on the first; the
	// failure is a refused/timeout dial, NOT a policy denial.
	if err == nil {
		t.Fatal("expected a dial error (nothing listens), got nil")
	}
	if apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("all-permitted answer denied: %v", err)
	}
}
