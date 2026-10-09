package nodehooks

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"
)

//go:embed scripts templates
var assets embed.FS

// File is one rendered bundle member.
type File struct {
	Name string // path relative to the extraction root, e.g. etc/custos/node/mounts.tsv
	Mode int64
	Data []byte
}

// Binding links a tenant to a Slurm account bound on the cluster.
type Binding struct {
	TenantID string
	Account  string
}

// RenderInput is everything the bundle depends on.
type RenderInput struct {
	Cluster     string
	Revision    int64
	Config      Config            // normalized and validated
	TenantSlugs map[string]string // tenant uuid -> slug
	Bindings    []Binding
}

// Rendered is the bundle plus non-fatal findings.
type Rendered struct {
	Files   []File
	Skipped []string // accounts or tenants that produced no tenant mounts
}

// Bundle path layout.
const (
	nodeDir       = "etc/custos/node/"
	prologMounts  = nodeDir + "prolog.d/900-custos-mounts"
	epilogUnmount = nodeDir + "epilog.d/100-custos-unmount"
	customBase    = 800
)

// HookFileName maps a hook to its bundle path. Slurm runs Prolog= and
// Epilog= directory globs in reverse alphabetical order, so a lower admin
// order gets a higher number and therefore runs earlier.
func HookFileName(h Hook) string {
	return fmt.Sprintf("%s%s.d/%03d-%s", nodeDir, h.Phase, customBase-h.Order, h.Name)
}

func asset(name string) []byte {
	b, err := assets.ReadFile(name)
	if err != nil {
		panic("nodehooks: missing embedded asset " + name)
	}
	return b
}

func validAccount(a string) bool {
	if a == "" || len(a) > 128 {
		return false
	}
	for _, r := range a {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// Render builds the deterministic bundle for a validated configuration.
func Render(in RenderInput) (Rendered, error) {
	cfg := in.Config
	var out Rendered
	add := func(name string, mode int64, data []byte) {
		out.Files = append(out.Files, File{Name: name, Mode: mode, Data: data})
	}

	tsv, skipped := renderMounts(in)
	out.Skipped = skipped
	add(nodeDir+"mounts.tsv", 0o644, tsv)
	add(nodeDir+"lib/common.sh", 0o644, asset("scripts/lib/common.sh"))
	add(prologMounts, 0o755, asset("scripts/prolog-mounts.sh"))
	add(epilogUnmount, 0o755, asset("scripts/epilog-unmount.sh"))
	add(nodeDir+"custos-node-sync", 0o755, asset("scripts/custos-node-sync"))
	if cfg.IsolationMode == ModeNamespace {
		add(nodeDir+"ns-clone.sh", 0o755, asset("scripts/ns-clone.sh"))
		add(nodeDir+"ns-epilog.sh", 0o755, asset("scripts/ns-epilog.sh"))
	}
	for _, h := range cfg.Hooks {
		script := h.Script
		if !strings.HasSuffix(script, "\n") {
			script += "\n"
		}
		add(HookFileName(h), 0o755, []byte(script))
	}
	add("etc/systemd/system/custos-node-sync.service", 0o644, asset("templates/custos-node-sync.service"))
	add("etc/systemd/system/custos-node-sync.timer", 0o644, asset("templates/custos-node-sync.timer"))

	readme, err := renderReadme(in)
	if err != nil {
		return out, err
	}
	add("README.md", 0o644, readme)

	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Name < out.Files[j].Name })
	return out, nil
}

func renderReadme(in RenderInput) ([]byte, error) {
	t, err := template.New("readme").Parse(string(asset("templates/README.md.tmpl")))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	err = t.Execute(&buf, map[string]any{
		"Cluster":   in.Cluster,
		"Revision":  in.Revision,
		"Mode":      in.Config.IsolationMode,
		"Timeout":   in.Config.MountTimeoutSeconds,
		"Wait":      in.Config.MountTimeoutSeconds + 5,
		"Namespace": in.Config.IsolationMode == ModeNamespace,
	})
	return buf.Bytes(), err
}

func optString(o []string) string { return strings.Join(o, ",") }

func renderMounts(in RenderInput) ([]byte, []string) {
	cfg := in.Config
	var b strings.Builder
	line := func(fields ...string) {
		b.WriteString(strings.Join(fields, "\t"))
		b.WriteByte('\n')
	}
	line("meta", "revision", strconv.FormatInt(in.Revision, 10))
	line("meta", "mode", cfg.IsolationMode)
	line("meta", "timeout", strconv.Itoa(cfg.MountTimeoutSeconds))

	shared := append([]Mount(nil), cfg.SharedMounts...)
	sort.Slice(shared, func(i, j int) bool {
		if shared[i].Target != shared[j].Target {
			return shared[i].Target < shared[j].Target
		}
		return shared[i].Name < shared[j].Name
	})
	for _, m := range shared {
		line("shared", m.Name, m.FSType, m.Source, m.Target, optString(m.Options))
	}

	// account -> distinct tenants (over ALL bindings, so a shared account
	// is detected even when only one of the tenants has mounts).
	accountTenants := map[string]map[string]bool{}
	var skipped []string
	for _, bd := range in.Bindings {
		if !validAccount(bd.Account) {
			skipped = append(skipped, "account:"+bd.Account)
			continue
		}
		if accountTenants[bd.Account] == nil {
			accountTenants[bd.Account] = map[string]bool{}
		}
		accountTenants[bd.Account][strings.ToLower(bd.TenantID)] = true
	}
	tenantAccounts := map[string][]string{}
	var conflicts []string
	for acct, ts := range accountTenants {
		if len(ts) > 1 {
			conflicts = append(conflicts, acct)
			continue
		}
		for t := range ts {
			tenantAccounts[t] = append(tenantAccounts[t], acct)
		}
	}
	sort.Strings(conflicts)

	type row struct{ account, slug, name, fstype, source, target, opts string }
	var rows []row
	for _, tm := range cfg.TenantMounts {
		slug, ok := in.TenantSlugs[tm.Tenant]
		if !ok {
			skipped = append(skipped, "tenant:"+tm.Tenant)
			continue
		}
		for _, acct := range tenantAccounts[tm.Tenant] {
			rows = append(rows, row{acct, slug, tm.Name, tm.FSType, tm.Source, tm.Target, optString(tm.Options)})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, c := rows[i], rows[j]
		switch {
		case a.account != c.account:
			return a.account < c.account
		case a.slug != c.slug:
			return a.slug < c.slug
		case a.target != c.target:
			return a.target < c.target
		}
		return a.name < c.name
	})
	for _, r := range rows {
		line("account", r.account, r.slug, r.name, r.fstype, r.source, r.target, r.opts)
	}
	for _, c := range conflicts {
		line("conflict", c)
	}
	sort.Strings(skipped)
	return []byte(b.String()), skipped
}

// Archive packs files into a deterministic gzip-compressed tar.
func Archive(files []File) ([]byte, error) {
	sorted := append([]File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(zw)
	for _, f := range sorted {
		hdr := &tar.Header{
			Typeflag: tar.TypeReg, Name: f.Name, Mode: f.Mode,
			Size: int64(len(f.Data)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.Data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// BundleSHA256 is the hex sha256 of the archive bytes (the HTTP ETag).
func BundleSHA256(archive []byte) string {
	sum := sha256.Sum256(archive)
	return hex.EncodeToString(sum[:])
}
