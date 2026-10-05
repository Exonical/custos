package validate

import "testing"

func TestValidImageURISchemes(t *testing.T) {
	for uri, want := range map[string]bool{
		"docker://ghcr.io/acme/app:1":    true,
		"oras://ghcr.io/acme/app:1":      true,
		"/shared/images/app.sif":         true,
		"https://ghcr.io/acme/app":       false,
		"oci://ghcr.io/acme/app":         false,
		"docker:/ghcr.io/acme/app":       false,
		"dockerXoras://ghcr.io/acme/app": false,
		"docker://ghcr.io/acme/../app":   false,
		"relative/images/app.sif":        false,
	} {
		if got := validImageURI(uri); got != want {
			t.Errorf("validImageURI(%q) = %v, want %v", uri, got, want)
		}
	}
}
