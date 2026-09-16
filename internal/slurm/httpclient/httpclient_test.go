package httpclient_test

import (
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/slurm"
	"github.com/Exonical/custos/internal/slurm/httpclient"
)

func TestSchemePolicy(t *testing.T) {
	// http refused without dev flags.
	if _, err := httpclient.New(slurm.Endpoint{BaseURL: "http://x/"},
		httpclient.DialPolicy{}); !apperr.Is(err, apperr.Invalid) {
		t.Fatalf("http without dev: %v", err)
	}
	// http allowed only with AllowHTTP+AllowLoopback.
	if _, err := httpclient.New(slurm.Endpoint{BaseURL: "http://127.0.0.1/"},
		httpclient.DialPolicy{AllowHTTP: true, AllowLoopback: true}); err != nil {
		t.Fatalf("http dev: %v", err)
	}
	// ftp-like schemes refused.
	if _, err := httpclient.New(slurm.Endpoint{BaseURL: "ftp://x/"},
		httpclient.DialPolicy{}); !apperr.Is(err, apperr.Invalid) {
		t.Fatalf("ftp: %v", err)
	}
}

func TestDialPolicyMatrix(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		policy httpclient.DialPolicy
		want   bool // dial permitted
	}{
		{"loopback denied by default", "https://127.0.0.1:1/",
			httpclient.DialPolicy{}, false},
		{"loopback allowed", "https://127.0.0.1:1/",
			httpclient.DialPolicy{AllowLoopback: true}, true},
		{"private denied", "https://10.0.0.1:1/",
			httpclient.DialPolicy{}, false},
		{"private allowed", "https://10.0.0.1:1/",
			httpclient.DialPolicy{AllowPrivate: true}, true},
		{"metadata always denied", "https://169.254.169.254:1/",
			httpclient.DialPolicy{AllowPrivate: true, AllowLoopback: true}, false},
		{"link-local denied", "https://169.254.1.1:1/",
			httpclient.DialPolicy{AllowPrivate: true}, false},
		{"deny CIDR", "https://203.0.113.5:1/",
			httpclient.DialPolicy{
				DenyCIDRs: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")},
			}, false},
	}
	for _, c := range cases {
		hc, err := httpclient.New(slurm.Endpoint{BaseURL: c.url}, c.policy)
		if err != nil {
			t.Fatalf("%s: build: %v", c.name, err)
		}
		_, err = hc.Get(c.url) //nolint:bodyclose // error path only
		denied := apperr.Is(err, apperr.Forbidden)
		if c.want && denied {
			t.Errorf("%s: expected dial permitted, got denied: %v", c.name, err)
		}
		if !c.want && !denied {
			t.Errorf("%s: expected dial denied, got err=%v", c.name, err)
		}
	}
}

func TestHTTPOnlyLoopback(t *testing.T) {
	// http is plaintext: even with AllowPrivate set, the dialed target
	// must be loopback.
	hc, err := httpclient.New(slurm.Endpoint{BaseURL: "http://10.0.0.1:1/"},
		httpclient.DialPolicy{
			AllowHTTP: true, AllowLoopback: true, AllowPrivate: true,
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hc.Get("http://10.0.0.1:1/"); !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("http to private IP: want Forbidden, got %v", err)
	}
}

func TestTLSServerAllowLoopback(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	// Extract the test server's CA cert as our pinned bundle.
	cert := srv.Certificate()
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})

	hc, err := httpclient.New(slurm.Endpoint{
		BaseURL:     srv.URL,
		CABundlePEM: caPEM,
	}, httpclient.DialPolicy{AllowLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestRedirectRefused(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/other", http.StatusFound)
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	caPEM := pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	hc, err := httpclient.New(slurm.Endpoint{
		BaseURL: srv.URL, CABundlePEM: caPEM,
	}, httpclient.DialPolicy{AllowLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = hc.Get(srv.URL + "/redir") //nolint:bodyclose // error expected
	if !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("want Forbidden, got %v", err)
	}
}

func TestNoInsecureSkipVerify(t *testing.T) {
	// A server presenting a cert NOT in the bundle must fail even to a
	// loopback host — proves no InsecureSkipVerify path exists.
	srv := httptest.NewTLSServer(http.HandlerFunc(
		func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	hc, err := httpclient.New(slurm.Endpoint{BaseURL: srv.URL},
		httpclient.DialPolicy{AllowLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hc.Get(srv.URL); err == nil {
		t.Fatal("expected TLS verification failure against untrusted cert")
	}
}
