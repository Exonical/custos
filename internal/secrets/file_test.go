package secrets_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Exonical/custos/internal/platform/apperr"
	"github.com/Exonical/custos/internal/secrets"
)

func setup(t *testing.T) (*secrets.FileResolver, string) {
	t.Helper()
	root := t.TempDir()
	r, err := secrets.NewFileResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	return r, root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFileResolve(t *testing.T) {
	r, root := setup(t)
	write(t, filepath.Join(root, "tok"), "s3cr3t\n")
	v, err := r.Resolve(context.Background(), secrets.Reference{
		Provider: "file", Path: filepath.Join(root, "tok"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(v.Reveal()) != "s3cr3t" {
		t.Fatalf("value = %q", v.Reveal())
	}
}

func TestFileEscapeDenied(t *testing.T) {
	r, root := setup(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "tok"), "x")
	_, err := r.Resolve(context.Background(), secrets.Reference{
		Provider: "file",
		Path:     filepath.Join(root, "..", filepath.Base(outside), "tok"),
	})
	if !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("want Forbidden, got %v", err)
	}
}

func TestFileSymlinkEscapeDenied(t *testing.T) {
	r, root := setup(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "tok"), "x")
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(outside, "tok"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := r.Resolve(context.Background(), secrets.Reference{
		Provider: "file", Path: link,
	})
	if !apperr.Is(err, apperr.Forbidden) {
		t.Fatalf("want Forbidden, got %v", err)
	}
}

func TestFileJSONKey(t *testing.T) {
	r, root := setup(t)
	write(t, filepath.Join(root, "doc"), `{"token":"abc","n":3}`)
	v, err := r.Resolve(context.Background(), secrets.Reference{
		Provider: "file", Path: filepath.Join(root, "doc"), Key: "token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(v.Reveal()) != "abc" {
		t.Fatalf("value = %q", v.Reveal())
	}
	if _, err := r.Resolve(context.Background(), secrets.Reference{
		Provider: "file", Path: filepath.Join(root, "doc"), Key: "n",
	}); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("non-string key: want NotFound, got %v", err)
	}
}

func TestFileSizeCap(t *testing.T) {
	r, root := setup(t)
	big := make([]byte, 70*1024)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(root, "big"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(context.Background(), secrets.Reference{
		Provider: "file", Path: filepath.Join(root, "big"),
	}); !apperr.Is(err, apperr.Invalid) {
		t.Fatalf("want Invalid, got %v", err)
	}
}

func TestFileRootValidation(t *testing.T) {
	if _, err := secrets.NewFileResolver(); !apperr.Is(err, apperr.Invalid) {
		t.Fatalf("empty roots: %v", err)
	}
	if _, err := secrets.NewFileResolver("relative/path"); !apperr.Is(err, apperr.Invalid) {
		t.Fatalf("relative root: %v", err)
	}
}

func TestMultiDispatch(t *testing.T) {
	r, _ := setup(t)
	m := secrets.Multi{"file": r}
	if _, err := m.Resolve(context.Background(), secrets.Reference{
		Provider: "openbao", Path: "x",
	}); !apperr.Is(err, apperr.Invalid) {
		t.Fatalf("want Invalid, got %v", err)
	}
}

func TestValueRedaction(t *testing.T) {
	v := secrets.NewValue([]byte("hunter2"))
	if v.String() != "[REDACTED]" {
		t.Fatal(v.String())
	}
	if v.LogValue().String() != "[REDACTED]" {
		t.Fatal(v.LogValue())
	}
	v.Wipe()
	if len(v.Reveal()) != 0 {
		t.Fatal("wipe failed")
	}
}
