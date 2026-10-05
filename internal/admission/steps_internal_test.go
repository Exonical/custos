package admission

import "testing"

func TestRegistryHostHelpers(t *testing.T) {
	cases := []struct {
		uri      string
		pyxis    string
		registry string
	}{
		{"docker://ubuntu:latest", "ubuntu:latest", "docker.io"},
		{"docker://library/ubuntu", "library/ubuntu", "docker.io"},
		{"docker://docker.io/library/ubuntu", "docker.io#library/ubuntu", "docker.io"},
		{"docker://ghcr.io/org/img:tag", "ghcr.io#org/img:tag", "ghcr.io"},
		{"docker://localhost/img", "localhost#img", "localhost"},
		{"docker://localhost", "localhost", "localhost"},
		{"docker://registry:5000/img", "registry:5000#img", "registry:5000"},
		{"docker://registry.example.com", "registry.example.com", "registry.example.com"},
		{"docker:///img", "/img", "docker.io"},
	}
	for _, tc := range cases {
		if got := pyxisImageURI(tc.uri); got != tc.pyxis {
			t.Errorf("pyxisImageURI(%q) = %q, want %q", tc.uri, got, tc.pyxis)
		}
		if got := dockerRegistryHost(tc.uri); got != tc.registry {
			t.Errorf("dockerRegistryHost(%q) = %q, want %q", tc.uri, got, tc.registry)
		}
	}
}
