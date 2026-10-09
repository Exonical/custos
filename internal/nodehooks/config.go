// Package nodehooks models the platform-admin node configuration of a
// cluster (NFS mounts and custom prolog/epilog hooks), validates it, and
// renders the deterministic bundle that is installed on compute nodes
// (ADR-032, docs/node-hooks.md). Everything rendered into scripts and
// the mounts table comes from validated values only.
package nodehooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/validation/shsyntax"
	"github.com/Exonical/custos/internal/workflowspec"
)

// Isolation modes.
const (
	ModeNamespace       = "namespace"
	ModeTenantExclusive = "tenant_exclusive"
	ModeNodeExclusive   = "node_exclusive"
)

// Hook phases.
const (
	PhaseProlog = "prolog"
	PhaseEpilog = "epilog"
)

// Limits.
const (
	DefaultMountTimeoutSeconds = 30
	MinMountTimeoutSeconds     = 5
	MaxMountTimeoutSeconds     = 300
	MaxHookScriptBytes         = 64 * 1024
	MaxSharedMounts            = 64
	MaxTenantMounts            = 256
	MaxHooks                   = 32
	maxSourceLen               = 512
	maxTargetLen               = 256
)

// Validation error codes.
const (
	CodeMountInvalid  = "NODE_MOUNT_INVALID"
	CodeHookInvalid   = "NODE_HOOK_INVALID"
	CodeConfigInvalid = "NODE_CONFIG_INVALID"
)

// Config is the per-cluster node configuration.
type Config struct {
	IsolationMode       string        `json:"isolation_mode"`
	MountTimeoutSeconds int           `json:"mount_timeout_seconds"`
	SharedMounts        []Mount       `json:"shared_mounts"`
	TenantMounts        []TenantMount `json:"tenant_mounts"`
	Hooks               []Hook        `json:"hooks"`
}

// Mount is one NFS mount.
type Mount struct {
	Name    string   `json:"name"`
	FSType  string   `json:"fstype"`
	Source  string   `json:"source"`
	Target  string   `json:"target"`
	Options []string `json:"options"`
}

// TenantMount is a mount resolved through the job's Slurm account.
type TenantMount struct {
	Tenant  string   `json:"tenant"`
	Name    string   `json:"name"`
	FSType  string   `json:"fstype"`
	Source  string   `json:"source"`
	Target  string   `json:"target"`
	Options []string `json:"options"`
}

// Hook is a custom admin snippet run in the Prolog or Epilog.
type Hook struct {
	Name   string `json:"name"`
	Phase  string `json:"phase"`
	Order  int    `json:"order"`
	Script string `json:"script"`
}

// FieldError is a path-addressed validation failure.
type FieldError struct {
	Path    string
	Code    string
	Message string
}

var (
	nameRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	sourceRE = regexp.MustCompile(`^[A-Za-z0-9.-]+:/[A-Za-z0-9._/-]*$`)
	targetRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	posIntRE = regexp.MustCompile(`^[0-9]{1,9}$`)
)

// reservedTargets may not be mounted over or beneath.
var reservedTargets = []string{"/bin", "/boot", "/dev", "/etc", "/lib",
	"/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var",
	"/tmp", "/dev/shm"}

var plainOptions = map[string]bool{
	"ro": true, "rw": true, "hard": true, "soft": true, "noatime": true,
	"nodiratime": true, "relatime": true, "nosuid": true, "nodev": true,
	"noexec": true, "nolock": true, "_netdev": true,
}

type intRange struct{ lo, hi int }

var intOptions = map[string]intRange{
	"timeo": {1, 6000}, "retrans": {0, 100}, "rsize": {1024, 1048576},
	"wsize": {1024, 1048576}, "actimeo": {0, 3600}, "port": {1, 65535},
	"nconnect": {1, 16},
}

var enumOptions = map[string][]string{
	"nfsvers":     {"3", "4", "4.0", "4.1", "4.2"},
	"vers":        {"3", "4", "4.0", "4.1", "4.2"},
	"proto":       {"tcp", "rdma"},
	"sec":         {"sys", "krb5", "krb5i", "krb5p"},
	"lookupcache": {"all", "none", "pos", "positive"},
}

// Default returns the configuration used before an admin sets one.
func Default() Config {
	return Config{
		IsolationMode:       ModeNamespace,
		MountTimeoutSeconds: DefaultMountTimeoutSeconds,
		SharedMounts:        []Mount{},
		TenantMounts:        []TenantMount{},
		Hooks:               []Hook{},
	}
}

// Normalize applies defaults and canonicalizes options (deduplicated,
// sorted, with the implied ro/rw and tenant hardening options). It does
// not validate; call Validate on the result.
func Normalize(c Config) Config {
	out := Config{
		IsolationMode:       c.IsolationMode,
		MountTimeoutSeconds: c.MountTimeoutSeconds,
		SharedMounts:        make([]Mount, 0, len(c.SharedMounts)),
		TenantMounts:        make([]TenantMount, 0, len(c.TenantMounts)),
		Hooks:               make([]Hook, 0, len(c.Hooks)),
	}
	if out.IsolationMode == "" {
		out.IsolationMode = ModeNamespace
	}
	if out.MountTimeoutSeconds == 0 {
		out.MountTimeoutSeconds = DefaultMountTimeoutSeconds
	}
	for _, m := range c.SharedMounts {
		m.Options = canonOptions(m.Options, "ro", false)
		out.SharedMounts = append(out.SharedMounts, m)
	}
	for _, m := range c.TenantMounts {
		m.Tenant = strings.ToLower(strings.TrimSpace(m.Tenant))
		m.Options = canonOptions(m.Options, "rw", true)
		out.TenantMounts = append(out.TenantMounts, m)
	}
	out.Hooks = append(out.Hooks, c.Hooks...)
	return out
}

func canonOptions(in []string, defaultAccess string, harden bool) []string {
	set := map[string]bool{}
	for _, o := range in {
		o = strings.TrimSpace(o)
		if o != "" {
			set[o] = true
		}
	}
	if !set["ro"] && !set["rw"] {
		set[defaultAccess] = true
	}
	if harden {
		set["nosuid"] = true
		set["nodev"] = true
	}
	out := make([]string, 0, len(set))
	for o := range set {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}

// Validate checks a normalized configuration and returns every problem.
func Validate(c Config) []FieldError {
	var errs []FieldError
	add := func(path, code, msg string) {
		errs = append(errs, FieldError{Path: path, Code: code, Message: msg})
	}
	switch c.IsolationMode {
	case ModeNamespace, ModeTenantExclusive, ModeNodeExclusive:
	default:
		add("isolation_mode", CodeConfigInvalid,
			"must be namespace, tenant_exclusive or node_exclusive")
	}
	if c.MountTimeoutSeconds < MinMountTimeoutSeconds ||
		c.MountTimeoutSeconds > MaxMountTimeoutSeconds {
		add("mount_timeout_seconds", CodeConfigInvalid,
			fmt.Sprintf("must be %d..%d", MinMountTimeoutSeconds, MaxMountTimeoutSeconds))
	}
	if len(c.SharedMounts) > MaxSharedMounts {
		add("shared_mounts", CodeMountInvalid, fmt.Sprintf("at most %d entries", MaxSharedMounts))
	}
	if len(c.TenantMounts) > MaxTenantMounts {
		add("tenant_mounts", CodeMountInvalid, fmt.Sprintf("at most %d entries", MaxTenantMounts))
	}
	if len(c.Hooks) > MaxHooks {
		add("hooks", CodeHookInvalid, fmt.Sprintf("at most %d entries", MaxHooks))
	}

	sharedNames := map[string]bool{}
	sharedTargets := map[string]bool{}
	for i, m := range c.SharedMounts {
		p := fmt.Sprintf("shared_mounts[%d]", i)
		errs = append(errs, validateMount(p, m.Name, m.FSType, m.Source, m.Target, m.Options)...)
		if sharedNames[m.Name] {
			add(p+".name", CodeMountInvalid, "duplicate name")
		}
		sharedNames[m.Name] = true
		if sharedTargets[m.Target] {
			add(p+".target", CodeMountInvalid, "duplicate target")
		}
		sharedTargets[m.Target] = true
	}

	type tkey struct{ tenant, name string }
	names := map[tkey]bool{}
	byTenant := map[string][]string{}
	for i, m := range c.TenantMounts {
		p := fmt.Sprintf("tenant_mounts[%d]", i)
		if _, err := uuid.Parse(m.Tenant); err != nil {
			add(p+".tenant", CodeMountInvalid, "must be a tenant uuid")
		}
		errs = append(errs, validateMount(p, m.Name, m.FSType, m.Source, m.Target, m.Options)...)
		k := tkey{m.Tenant, m.Name}
		if names[k] {
			add(p+".name", CodeMountInvalid, "duplicate name for this tenant")
		}
		names[k] = true
		for s := range sharedTargets {
			if nested(m.Target, s) {
				add(p+".target", CodeMountInvalid,
					"must not equal or nest with shared mount target "+s)
			}
		}
		for _, other := range byTenant[m.Tenant] {
			if nested(m.Target, other) {
				add(p+".target", CodeMountInvalid,
					"must not equal or nest with another target of the same tenant: "+other)
			}
		}
		byTenant[m.Tenant] = append(byTenant[m.Tenant], m.Target)
	}

	hookNames := map[string]bool{}
	hookOrders := map[string]bool{}
	for i, h := range c.Hooks {
		p := fmt.Sprintf("hooks[%d]", i)
		errs = append(errs, validateHook(p, h)...)
		if hookNames[h.Name] {
			add(p+".name", CodeHookInvalid, "duplicate name")
		}
		hookNames[h.Name] = true
		ok := h.Phase + "/" + strconv.Itoa(h.Order)
		if hookOrders[ok] {
			add(p+".order", CodeHookInvalid, "order must be unique within a phase")
		}
		hookOrders[ok] = true
	}
	return errs
}

func validateMount(p, name, fstype, source, target string, opts []string) []FieldError {
	var errs []FieldError
	add := func(path, msg string) {
		errs = append(errs, FieldError{Path: p + "." + path, Code: CodeMountInvalid, Message: msg})
	}
	if !nameRE.MatchString(name) {
		add("name", "must match ^[a-z0-9][a-z0-9-]{0,62}$")
	}
	if fstype != "nfs" && fstype != "nfs4" {
		add("fstype", "must be nfs or nfs4")
	}
	if len(source) > maxSourceLen || !sourceRE.MatchString(source) ||
		hasDotDot(source[strings.IndexByte(source+":", ':')+1:]) {
		add("source", "must be host:/path with [A-Za-z0-9._/-], no '..', at most 512 characters")
	}
	if msg := targetProblem(target); msg != "" {
		add("target", msg)
	}
	for j, o := range opts {
		if msg := optionProblem(o); msg != "" {
			errs = append(errs, FieldError{
				Path: fmt.Sprintf("%s.options[%d]", p, j), Code: CodeMountInvalid, Message: msg})
		}
	}
	if slices.Contains(opts, "ro") && slices.Contains(opts, "rw") {
		add("options", "ro and rw are mutually exclusive")
	}
	if slices.Contains(opts, "hard") && slices.Contains(opts, "soft") {
		add("options", "hard and soft are mutually exclusive")
	}
	return errs
}

func hasDotDot(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func targetProblem(t string) string {
	switch {
	case t == "" || t[0] != '/':
		return "must be an absolute path"
	case t == "/":
		return "must not be /"
	case len(t) > maxTargetLen:
		return "at most 256 characters"
	case !targetRE.MatchString(t):
		return "may only contain [A-Za-z0-9._/-]"
	case strings.HasSuffix(t, "/"):
		return "must not end with /"
	}
	for _, seg := range strings.Split(t[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "must not contain empty, '.' or '..' segments"
		}
	}
	for _, r := range reservedTargets {
		if nested(t, r) {
			return "must not equal or be under " + r
		}
	}
	return ""
}

func optionProblem(o string) string {
	if plainOptions[o] {
		return ""
	}
	k, v, ok := strings.Cut(o, "=")
	if !ok {
		return "option not allowed: " + o
	}
	if allowed, found := enumOptions[k]; found {
		if slices.Contains(allowed, v) {
			return ""
		}
		return fmt.Sprintf("%s must be one of %s", k, strings.Join(allowed, "|"))
	}
	if r, found := intOptions[k]; found {
		n, err := strconv.Atoi(v)
		if !posIntRE.MatchString(v) || err != nil || n < r.lo || n > r.hi {
			return fmt.Sprintf("%s must be an integer %d..%d", k, r.lo, r.hi)
		}
		return ""
	}
	return "option not allowed: " + k
}

// nested reports whether a equals b or lies beneath it.
func nested(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func validateHook(p string, h Hook) []FieldError {
	var errs []FieldError
	add := func(path, msg string) {
		errs = append(errs, FieldError{Path: p + "." + path, Code: CodeHookInvalid, Message: msg})
	}
	if !nameRE.MatchString(h.Name) {
		add("name", "must match ^[a-z0-9][a-z0-9-]{0,62}$")
	}
	if h.Phase != PhaseProlog && h.Phase != PhaseEpilog {
		add("phase", "must be prolog or epilog")
	}
	if h.Order < 0 || h.Order > 99 {
		add("order", "must be 0..99")
	}
	switch {
	case len(h.Script) > MaxHookScriptBytes:
		add("script", "at most 64 KiB")
	case strings.IndexByte(h.Script, 0) >= 0:
		add("script", "must not contain NUL bytes")
	case !strings.HasPrefix(h.Script, "#!/bin/bash\n") && !strings.HasPrefix(h.Script, "#!/bin/sh\n"):
		add("script", "must start with #!/bin/bash or #!/bin/sh on its own line")
	case strings.Contains(h.Script, "\r"):
		add("script", "must use LF line endings")
	default:
		lang := workflowspec.LanguageBash
		if strings.HasPrefix(h.Script, "#!/bin/sh") {
			lang = workflowspec.LanguageSh
		}
		if _, err := shsyntax.ParseForScan([]byte(h.Script), lang); err != nil {
			add("script", "syntax error: "+err.Error())
		}
	}
	return errs
}

// ContentSHA256 is the hash of the canonical normalized configuration.
func ContentSHA256(c Config) string {
	b, _ := json.Marshal(Normalize(c))
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
