package authz

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "regenerate testdata/roles.golden.txt")

// TestRoleTableGolden pins the role->permission table in reviewable
// form. Regenerate with `go test ./internal/authz -update`.
func TestRoleTableGolden(t *testing.T) {
	var b strings.Builder
	for _, role := range Roles() {
		perms := Permissions(role) // already sorted
		b.WriteString(role)
		b.WriteString(":")
		for _, p := range perms {
			b.WriteString(" ")
			b.WriteString(string(p))
		}
		b.WriteString("\n")
	}
	path := filepath.Join("testdata", "roles.golden.txt")
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if got := b.String(); got != string(want) {
		t.Fatalf("role table drifted; run `go test ./internal/authz -update`\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// TestPlatformAuditorReadOnly asserts the auditor never holds a
// mutating permission.
func TestPlatformAuditorReadOnly(t *testing.T) {
	for _, p := range Permissions(RolePlatformAuditor) {
		s := string(p)
		for _, bad := range []string{"manage", "create", "publish", "execute",
			"approve", "submit", "cancel", "use", "assign"} {
			if strings.Contains(s, bad) {
				t.Fatalf("platform-auditor holds mutating permission %s", p)
			}
		}
	}
}
