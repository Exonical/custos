package nodehooks_test

import (
	"archive/tar"
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Exonical/custos/internal/nodehooks"
)

// Behaviour tests for the generated node scripts. They run the rendered
// bundle inside a debian container with stub mount tooling that keeps a
// fake mount table, so no privileges or real NFS are needed. Guarded by
// CUSTOS_TEST_NODEHOOKS_PODMAN=1 (needs podman and network for the image).

const podmanImage = "docker.io/library/debian:stable-slim"

const stubCommon = `#!/bin/bash
T=${FAKE_MOUNTS:-/fake/mounts}
`

var stubScripts = map[string]string{
	"mount": stubCommon + `echo "mount[${FAKE_MOUNTS:+ns}${FAKE_MOUNTS:-host}] $*" >>/fake/mount.log
while [ $# -gt 0 ]; do
	case $1 in -t | -o) shift 2 ;; *) break ;; esac
done
source=$1
target=$2
key=${target//\//_}
if [ -e "/fake/fail-mount$key" ]; then
	echo "mount: $target: simulated failure" >&2
	exit 32
fi
printf '%s\t%s\n' "$target" "$source" >>"$T"
`,
	"umount": stubCommon + `echo "umount[${FAKE_MOUNTS:-host}] $*" >>/fake/umount.log
if [ "$1" = -l ]; then shift; fi
target=$1
if [ -e /fake/fail-umount ]; then exit 32; fi
awk -F'\t' -v t="$target" '$1!=t' "$T" >"$T.new" && mv "$T.new" "$T"
`,
	"mountpoint": stubCommon + `awk -F'\t' -v t="$2" '$1==t{f=1} END{exit f?0:1}' "$T"
`,
	"findmnt": stubCommon + `awk -F'\t' -v t="$5" '$1==t{print $2; f=1} END{exit f?0:1}' "$T"
`,
	"nsenter": `#!/bin/bash
ns=${1#--mount=}
shift 2
export FAKE_MOUNTS="/fake/ns${ns//\//_}"
touch "$FAKE_MOUNTS"
exec "$@"
`,
	"logger": `#!/bin/bash
if [ "$1" = -t ]; then shift 2; fi
if [ "$1" = -- ]; then shift; fi
printf '%s\n' "$*" >>/fake/syslog
`,
	"timeout": `#!/bin/bash
shift
exec "$@"
`,
}

const casePreamble = `set -u
for c in mount umount mountpoint findmnt nsenter logger timeout; do
	dest=$(command -v "$c" || true)
	[ -n "$dest" ] || dest=/usr/bin/$c
	cp -f "/stubs/$c" "$dest"
	chmod 755 "$dest"
done
mkdir -p /fake
: >/fake/mounts
: >/fake/syslog
: >/fake/mount.log
: >/fake/umount.log
PRO=/etc/custos/node/prolog.d/900-custos-mounts
EPI=/etc/custos/node/epilog.d/100-custos-unmount
NSC=/etc/custos/node/ns-clone.sh
NSE=/etc/custos/node/ns-epilog.sh

fail() {
	echo "ASSERT FAILED: $*"
	echo "--- host mounts ---"; cat /fake/mounts
	echo "--- syslog ---"; cat /fake/syslog
	echo "--- mount log ---"; cat /fake/mount.log
	exit 1
}
pro() {
	if [ -n "${2-}" ]; then
		env -i SLURM_JOB_ID="$1" SLURM_JOB_ACCOUNT="$2" bash "$PRO"
	else
		env -i SLURM_JOB_ID="$1" bash "$PRO"
	fi
}
epi() { env -i SLURM_JOB_ID="$1" bash "$EPI"; }
# ns_clone <ns> <jobid-or-empty> <account-or-empty>
nsrun() {
	local script=$1 ns=$2 job=${3-} acct=${4-}
	local -a e=(SLURM_NS="$ns")
	[ -n "$job" ] && e+=(SLURM_JOB_ID="$job")
	[ -n "$acct" ] && e+=(SLURM_JOB_ACCOUNT="$acct")
	env -i "${e[@]}" bash "$script"
}
rc_of() { "$@" >/dev/null 2>&1 && echo 0 || echo $?; }
expect_rc() {
	local want=$1
	shift
	local got
	got=$(rc_of "$@")
	[ "$got" = "$want" ] || fail "exit $got, want $want: $*"
}
mounted_from() { awk -F'\t' -v t="$1" -v s="$2" '$1==t && $2==s{f=1} END{exit f?0:1}' "${3:-/fake/mounts}"; }
is_mounted() { awk -F'\t' -v t="$1" '$1==t{f=1} END{exit f?0:1}' "${2:-/fake/mounts}"; }
expect_mounted() { mounted_from "$@" || fail "$1 not mounted from $2"; }
expect_not_mounted() { ! is_mounted "$@" || fail "$1 is mounted"; }
mount_count() { grep -c -E "^mount\[host\] .* $1\$" /fake/mount.log || true; }
syslog_has() { grep -q -- "$1" /fake/syslog || fail "syslog lacks: $1"; }
`

func podmanInput(mode string) nodehooks.RenderInput {
	cfg := nodehooks.Normalize(nodehooks.Config{
		IsolationMode: mode, MountTimeoutSeconds: 30,
		SharedMounts: []nodehooks.Mount{{Name: "apps", FSType: "nfs4",
			Source: "server:/hpc/apps", Target: "/apps", Options: []string{"ro", "nfsvers=4.2"}}},
		TenantMounts: []nodehooks.TenantMount{
			{Tenant: tenantA, Name: "flight-data", FSType: "nfs4", Source: "server:/tenantA/flight_data", Target: "/mnt/data"},
			{Tenant: tenantA, Name: "scratch", FSType: "nfs4", Source: "server:/tenantA/scratch", Target: "/scratch"},
			{Tenant: tenantB, Name: "flight-data", FSType: "nfs4", Source: "server:/tenantB/flight_data", Target: "/mnt/data"},
		},
	})
	return nodehooks.RenderInput{
		Cluster: "e2e", Revision: 1, Config: cfg,
		TenantSlugs: map[string]string{tenantA: "tenant-a", tenantB: "tenant-b"},
		Bindings: []nodehooks.Binding{
			{TenantID: tenantA, Account: "acct-a"},
			{TenantID: tenantB, Account: "acct-b"},
			{TenantID: tenantA, Account: "shared-acct"},
			{TenantID: tenantB, Account: "shared-acct"},
		},
	}
}

func tarStream(t *testing.T, files []nodehooks.File, extra map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	write := func(name string, mode int64, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode,
			Size: int64(len(data)), ModTime: time.Unix(0, 0)}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		if strings.HasPrefix(f.Name, "etc/custos/node/") {
			write(f.Name, f.Mode, f.Data)
		}
	}
	for name, body := range stubScripts {
		write("stubs/"+name, 0o755, []byte(body))
	}
	for name, body := range extra {
		write(name, 0o755, []byte(body))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// runPodmanCase renders the bundle for mode and runs script in a container.
func runPodmanCase(t *testing.T, mode, script string) {
	t.Helper()
	if os.Getenv("CUSTOS_TEST_NODEHOOKS_PODMAN") != "1" {
		t.Skip("set CUSTOS_TEST_NODEHOOKS_PODMAN=1 (needs podman) to run script behaviour tests")
	}
	podman, err := exec.LookPath("podman")
	if err != nil {
		t.Skip("podman not on PATH")
	}
	in := podmanInput(mode)
	if errs := nodehooks.Validate(in.Config); len(errs) != 0 {
		t.Fatalf("fixture invalid: %+v", errs)
	}
	r, err := nodehooks.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	stream := tarStream(t, r.Files, map[string]string{
		"case/run.sh": casePreamble + "\n" + script + "\necho CASE-OK\n",
	})
	cmd := exec.Command(podman, "run", "--rm", "-i", "--cgroups=disabled", podmanImage,
		"bash", "-c", "tar -xf - -C / && bash /case/run.sh")
	cmd.Stdin = bytes.NewReader(stream)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "CASE-OK") {
		t.Fatalf("case failed: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestNodeScriptsPodman(t *testing.T) {
	t.Run("unknown account gets shared mounts only", func(t *testing.T) {
		runPodmanCase(t, "tenant_exclusive", `
expect_rc 0 pro 1 unknown-acct
expect_mounted /apps server:/hpc/apps
expect_not_mounted /mnt/data
expect_not_mounted /scratch
[ "$(cat /run/custos/jobs/1/account)" = unknown-acct ] || fail "account file"
expect_rc 0 pro 2 unknown-acct
[ "$(mount_count /apps)" = 1 ] || fail "shared mount repeated: $(mount_count /apps)"
expect_rc 0 pro 3
[ "$(cat /run/custos/jobs/3/account)" = - ] || fail "job without account must record -"
[ ! -d /run/custos/active ] || [ -z "$(ls -A /run/custos/active)" ] || fail "unknown accounts must not take tenant markers"
expect_rc 0 epi 1
expect_mounted /apps server:/hpc/apps
[ ! -e /run/custos/jobs/1 ] || fail "job dir not removed"
`)
	})

	t.Run("shared target mounted from another source fails closed", func(t *testing.T) {
		runPodmanCase(t, "tenant_exclusive", `
printf '/apps\tevil:/x\n' >>/fake/mounts
expect_rc 1 pro 1 acct-a
syslog_has ISOLATION_VIOLATION
expect_not_mounted /mnt/data
`)
	})

	t.Run("tenant_exclusive same tenant refcount", func(t *testing.T) {
		runPodmanCase(t, "tenant_exclusive", `
expect_rc 0 pro 1 acct-a
expect_mounted /mnt/data server:/tenantA/flight_data
expect_mounted /scratch server:/tenantA/scratch
[ "$(cat /run/custos/jobs/1/tenant)" = tenant-a ] || fail "tenant file"
[ -e /run/custos/active/tenant-a/1 ] || fail "marker 1"
expect_rc 0 pro 2 acct-a
[ "$(mount_count /mnt/data)" = 1 ] || fail "tenant mount repeated"
[ -e /run/custos/active/tenant-a/2 ] || fail "marker 2"
expect_rc 0 epi 1
expect_mounted /mnt/data server:/tenantA/flight_data
expect_mounted /scratch server:/tenantA/scratch
expect_rc 0 epi 2
expect_not_mounted /mnt/data
expect_not_mounted /scratch
expect_mounted /apps server:/hpc/apps
[ ! -e /run/custos/active/tenant-a ] || fail "active dir should be gone"
`)
	})

	t.Run("tenant_exclusive second tenant is refused", func(t *testing.T) {
		runPodmanCase(t, "tenant_exclusive", `
expect_rc 0 pro 1 acct-a
expect_rc 1 pro 2 acct-b
syslog_has ISOLATION_VIOLATION
expect_mounted /mnt/data server:/tenantA/flight_data
[ ! -e /run/custos/jobs/2 ] || fail "refused job dir must be removed"
expect_rc 0 epi 1
expect_not_mounted /mnt/data
expect_rc 0 pro 3 acct-b
expect_mounted /mnt/data server:/tenantB/flight_data
expect_not_mounted /scratch
`)
	})

	t.Run("tenant mount from another tenant source is a violation", func(t *testing.T) {
		runPodmanCase(t, "tenant_exclusive", `
printf '/mnt/data\tserver:/tenantB/flight_data\n' >>/fake/mounts
expect_rc 1 pro 1 acct-a
syslog_has ISOLATION_VIOLATION
[ ! -e /run/custos/active/tenant-a/1 ] || fail "marker must be removed"
expect_mounted /mnt/data server:/tenantB/flight_data
`)
	})

	t.Run("node_exclusive refuses any second job", func(t *testing.T) {
		runPodmanCase(t, "node_exclusive", `
expect_rc 0 pro 1 acct-a
expect_rc 1 pro 2 acct-a
syslog_has ISOLATION_VIOLATION
expect_rc 0 epi 1
expect_not_mounted /mnt/data
expect_rc 0 pro 3 acct-a
expect_mounted /mnt/data server:/tenantA/flight_data
`)
	})

	t.Run("unmount that keeps failing fails the epilog", func(t *testing.T) {
		runPodmanCase(t, "tenant_exclusive", `
expect_rc 0 pro 1 acct-a
touch /fake/fail-umount
expect_rc 1 epi 1
syslog_has "still mounted"
grep -q '^umount\[host\] -l ' /fake/umount.log || fail "lazy fallback not attempted"
is_mounted /mnt/data || fail "stub should keep the mount"
`)
	})

	t.Run("mid-prolog mount failure cleans up", func(t *testing.T) {
		runPodmanCase(t, "tenant_exclusive", `
touch /fake/fail-mount_scratch
expect_rc 1 pro 1 acct-a
syslog_has "tenant mount scratch failed"
expect_not_mounted /mnt/data
expect_not_mounted /scratch
[ ! -e /run/custos/active/tenant-a/1 ] || fail "marker must be removed"
[ ! -e /run/custos/jobs/1 ] || fail "job dir must be removed"
expect_mounted /apps server:/hpc/apps

# With another job of the tenant active, the failed attempt keeps its mounts.
rm /fake/fail-mount_scratch
expect_rc 0 pro 2 acct-a
touch /fake/fail-mount_scratch
umount /scratch
expect_rc 1 pro 3 acct-a
expect_mounted /mnt/data server:/tenantA/flight_data
[ -e /run/custos/active/tenant-a/2 ] || fail "other job marker must survive"
[ ! -e /run/custos/active/tenant-a/3 ] || fail "failed job marker must be removed"
`)
	})

	t.Run("namespace mode", func(t *testing.T) {
		runPodmanCase(t, "namespace", `
expect_rc 0 pro 1 acct-a
expect_not_mounted /mnt/data
expect_not_mounted /scratch
expect_mounted /apps server:/hpc/apps
[ "$(cat /run/custos/jobs/1/account)" = acct-a ] || fail "account file"
[ ! -d /run/custos/active ] || [ -z "$(ls -A /run/custos/active)" ] || fail "namespace mode takes no markers"

# 1. account from SLURM_JOB_ACCOUNT
expect_rc 0 nsrun "$NSC" /run/ns/a 1 acct-a
NS=/fake/ns_run_ns_a
expect_mounted /mnt/data server:/tenantA/flight_data "$NS"
expect_mounted /scratch server:/tenantA/scratch "$NS"
expect_not_mounted /mnt/data
# 2. account from the prolog's file via SLURM_JOB_ID only
expect_rc 0 nsrun "$NSC" /run/ns/b 1
expect_mounted /mnt/data server:/tenantA/flight_data /fake/ns_run_ns_b
# 3. neither resolves: fail closed
expect_rc 1 nsrun "$NSC" /run/ns/c 99
syslog_has "failing closed"
expect_rc 1 nsrun "$NSC" /run/ns/c
[ ! -s /fake/ns_run_ns_c ] || fail "nothing may be mounted when the account is unknown"
# 4. unmounted account recorded as "-": nothing to do
expect_rc 0 pro 5
expect_rc 0 nsrun "$NSC" /run/ns/d 5
[ ! -s /fake/ns_run_ns_d ] || fail "no tenant mounts for non-Custos jobs"
# 5. relative SLURM_NS rejected
expect_rc 1 nsrun "$NSC" relative 1 acct-a
# ns-epilog unmounts inside the namespace and is idempotent
expect_rc 0 nsrun "$NSE" /run/ns/a 1 acct-a
expect_not_mounted /mnt/data "$NS"
expect_not_mounted /scratch "$NS"
expect_rc 0 nsrun "$NSE" /run/ns/a 1 acct-a
expect_rc 0 nsrun "$NSE" /run/ns/b 1
expect_not_mounted /mnt/data /fake/ns_run_ns_b
# host epilog only removes the job dir; shared mounts stay
expect_rc 0 epi 1
[ ! -e /run/custos/jobs/1 ] || fail "job dir not removed"
expect_mounted /apps server:/hpc/apps
`)
	})

	t.Run("account mapped to two tenants fails closed", func(t *testing.T) {
		runPodmanCase(t, "tenant_exclusive", `
expect_rc 1 pro 1 shared-acct
syslog_has ISOLATION_VIOLATION
expect_not_mounted /mnt/data
`)
		runPodmanCase(t, "namespace", `
expect_rc 1 pro 1 shared-acct
syslog_has ISOLATION_VIOLATION
expect_rc 1 nsrun "$NSC" /run/ns/x 1 shared-acct
`)
	})
}
