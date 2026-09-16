package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Exonical/custos/internal/platform/apperr"
)

// maxFileSecret bounds file provider reads.
const maxFileSecret = 64 * 1024

// FileResolver resolves references against the local filesystem. Paths
// must resolve (after Clean + EvalSymlinks) inside one of the allow-listed
// roots; anything else is denied. Intended for dev, tests, and deployments
// that mount cluster credentials as files (see ADR-014).
type FileResolver struct {
	roots []string
}

// NewFileResolver validates roots (must be absolute and exist) and returns
// a resolver. Roots are canonicalized with EvalSymlinks once.
func NewFileResolver(roots ...string) (*FileResolver, error) {
	if len(roots) == 0 {
		return nil, apperr.New(apperr.Invalid, "secrets.no_roots",
			"file resolver requires at least one root")
	}
	canon := make([]string, 0, len(roots))
	for _, r := range roots {
		if !filepath.IsAbs(r) {
			return nil, apperr.New(apperr.Invalid, "secrets.root_not_absolute",
				"file secret root must be absolute: "+r)
		}
		resolved, err := filepath.EvalSymlinks(r)
		if err != nil {
			return nil, apperr.New(apperr.Invalid, "secrets.root_unreadable",
				"file secret root not readable: "+r)
		}
		canon = append(canon, resolved)
	}
	return &FileResolver{roots: canon}, nil
}

// Resolve implements Resolver.
func (f *FileResolver) Resolve(_ context.Context, ref Reference) (Value, error) {
	path, err := f.vet(ref.Path)
	if err != nil {
		return Value{}, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return Value{}, mapErr(err, ref.Path)
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxFileSecret {
		return Value{}, apperr.New(apperr.Invalid, "secrets.file_too_large",
			"secret file exceeds 64KiB or is not a regular file")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- path is vetted against roots
	if err != nil {
		return Value{}, mapErr(err, ref.Path)
	}
	raw = bytes.TrimRight(raw, "\r\n")
	if ref.Key != "" {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return Value{}, apperr.New(apperr.Invalid, "secrets.invalid_json",
				"secret file is not valid JSON")
		}
		s, ok := doc[ref.Key].(string)
		if !ok {
			return Value{}, apperr.New(apperr.NotFound, "secrets.key_not_found",
				"key not present as a string in secret file")
		}
		raw = []byte(s)
	}
	return NewValue(raw), nil
}

// vet cleans and resolves path and requires it to live inside a root.
func (f *FileResolver) vet(path string) (string, error) {
	clean := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", mapErr(err, path)
	}
	for _, root := range f.roots {
		rel, err := filepath.Rel(root, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	return "", apperr.New(apperr.Forbidden, "secrets.path_outside_root",
		"secret path resolves outside all allowed roots")
}

func mapErr(err error, _ string) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return apperr.New(apperr.NotFound, "secrets.not_found",
			"secret file not found")
	case errors.Is(err, fs.ErrPermission):
		return apperr.New(apperr.Forbidden, "secrets.forbidden",
			"secret file not readable")
	default:
		return apperr.New(apperr.Internal, "secrets.read_failed",
			"secret file read failed")
	}
}
