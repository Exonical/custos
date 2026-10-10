#!/usr/bin/env bash
# Smoke-installs the Helm chart (deploy/helm/custos) into a throwaway kind
# cluster running on Podman. Local / nightly only (like scripts/e2e.sh); never
# on pull requests. Linux (or a Linux VM such as the Podman machine) with a
# ROOTFUL Podman is required; kind's Podman provider cannot run rootless here.
#
#   bash scripts/helm-kind.sh            # run, then delete the cluster
#   KEEP=1 bash scripts/helm-kind.sh     # keep the cluster (kubectl --context kind-custos-helm)
#
# What it does: builds (or reuses) custos:local, custos-web:local and
# custos-validator:local, creates a kind cluster, installs the pinned
# CloudNativePG operator and Gateway API standard CRDs, deploys a Keycloak
# test fixture (HTTPS, e2e realm, throwaway CA) and installs the chart with the
# bundled PostgreSQL, web, Gateway API routes (CRDs only, no controller) and
# NetworkPolicies, so Custos runs with dev_mode: false. Then it asserts health,
# an authenticated API call, the web frontend, the validator sidecars, a
# `helm upgrade` that creates the next migrate Job and rolls pods, and a clean
# `helm uninstall`.
#
# Env: KEEP=1 keep the cluster; HELM_KIND_BUILD=1 force image rebuilds
# (default: build only missing images); HELM_KIND_LOGDIR log directory;
# HELM_KIND_MIN_MEM_MB minimum MemAvailable (default 4096); PODMAN podman binary.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO=$PWD

CLUSTER=custos-helm
NS=custos
REL=custos
CTX=kind-$CLUSTER
KEEP=${KEEP:-0}
PODMAN=${PODMAN:-podman}
MIN_MEM_MB=${HELM_KIND_MIN_MEM_MB:-4096}
STAMP=$(date +%Y%m%d-%H%M%S)
LOGDIR=${HELM_KIND_LOGDIR:-${TMPDIR:-/tmp}/custos-helm-kind-$STAMP}
TOOLS=${HELM_CHECK_TOOLS:-${XDG_CACHE_HOME:-$HOME/.cache}/custos-helm-tools}
WORK=$(mktemp -d)
mkdir -p "$LOGDIR" "$TOOLS"
export KIND_EXPERIMENTAL_PROVIDER=podman

# Pinned versions (all published at least 7 days before pinning).
KIND_VERSION=v0.33.0                 # 2026-08-26
KIND_LINUX_AMD64_SHA256=aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d
NODE_IMAGE=kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0
KUBECTL_VERSION=v1.35.8
KUBECTL_LINUX_AMD64_SHA256=874d5e72dbb819f43cff16bcd1e4f8bac5b7f2361fe1e55049b0a6c676fb0cbf
HELM_VERSION=v4.3.0                  # 2026-09-09
HELM_LINUX_AMD64_SHA256=86584a54def73570558f66f5111cc53dfed56689637ae32c1201205d494f54fb
GATEWAY_API_VERSION=v1.6.2           # 2026-09-03 (standard channel)
GATEWAY_API_SHA256=faede450fa178126aba41337737b97d351ebe87d93c910237ce1e072d1ca40d9
CNPG_OPERATOR_CHART=0.29.1           # operator 1.30.1, chart published 2026-09-23
KEYCLOAK_IMAGE=quay.io/keycloak/keycloak:26.7

LOG=$LOGDIR/helm-kind.log
exec > >(tee -a "$LOG") 2>&1

step() { echo; echo "== $*"; }
die() { echo "helm-kind: FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }

PIDS=()
cleanup() {
	local rc=$?
	for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
	if [ $rc -ne 0 ] && command -v kubectl >/dev/null 2>&1 && kubectl --context "$CTX" version >/dev/null 2>&1; then
		echo "helm-kind: failed (rc=$rc), collecting diagnostics into $LOGDIR"
		kubectl --context "$CTX" get pods,jobs,events -A -o wide >"$LOGDIR/diag-resources.txt" 2>&1 || true
		for ns in "$NS" cnpg-system; do
			for pod in $(kubectl --context "$CTX" -n "$ns" get pods -o name 2>/dev/null); do
				kubectl --context "$CTX" -n "$ns" logs "$pod" --all-containers --prefix --tail=200 \
					>"$LOGDIR/diag-${ns}-${pod#pod/}.log" 2>&1 || true
			done
		done
	fi
	if [ "$KEEP" = 1 ]; then
		echo "helm-kind: KEEP=1, cluster $CLUSTER left running (kubectl --context $CTX ...)"
	else
		kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
	fi
	rm -rf "$WORK"
	echo "helm-kind: logs in $LOGDIR"
	exit $rc
}
trap cleanup EXIT

fetch() { # fetch <url> <sha256> <dest>
	curl -fsSL --retry 3 -o "$3" "$1"
	echo "$2  $3" | sha256sum -c - >/dev/null || die "checksum mismatch for $1"
}

[ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ] || die "Linux x86_64 required (run inside the Podman machine on Windows/macOS)"

step "preflight"
"$PODMAN" info --format '{{.Host.Security.Rootless}}' | grep -qx false \
	|| die "rootful Podman required (kind's podman provider); this connection is rootless"
avail_mb=$(awk '/^MemAvailable:/ {print int($2/1024)}' /proc/meminfo)
echo "  MemAvailable ${avail_mb} MiB (need ${MIN_MEM_MB})"
[ "$avail_mb" -ge "$MIN_MEM_MB" ] || die "not enough free memory for kind (${avail_mb} MiB < ${MIN_MEM_MB} MiB); free memory, do not change the Podman machine here"
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then die "kind cluster $CLUSTER already exists; delete it first"; fi

step "tools"
if ! command -v kind >/dev/null 2>&1; then
	fetch "https://github.com/kubernetes-sigs/kind/releases/download/$KIND_VERSION/kind-linux-amd64" "$KIND_LINUX_AMD64_SHA256" "$TOOLS/kind"
	chmod +x "$TOOLS/kind"
fi
if ! command -v kubectl >/dev/null 2>&1; then
	fetch "https://dl.k8s.io/release/$KUBECTL_VERSION/bin/linux/amd64/kubectl" "$KUBECTL_LINUX_AMD64_SHA256" "$TOOLS/kubectl"
	chmod +x "$TOOLS/kubectl"
fi
if ! command -v helm >/dev/null 2>&1 || ! helm version --short | grep -q "^$HELM_VERSION"; then
	mkdir -p "$TOOLS/helm"
	fetch "https://get.helm.sh/helm-$HELM_VERSION-linux-amd64.tar.gz" "$HELM_LINUX_AMD64_SHA256" "$TOOLS/helm.tgz"
	tar -xzf "$TOOLS/helm.tgz" -C "$TOOLS/helm" --strip-components=1 linux-amd64/helm
fi
export PATH="$TOOLS:$TOOLS/helm:$PATH"
kind version
kubectl version --client
helm version --short

# openssl from the host, or from the golang image the Containerfile already uses.
ossl() {
	if command -v openssl >/dev/null 2>&1; then
		openssl "$@"
	else
		"$PODMAN" run --rm --cgroups=disabled -v "$WORK:$WORK" -w "$WORK" docker.io/library/golang:1.27 openssl "$@"
	fi
}

step "images"
build_image() { # build_image <ref> <podman build args...>
	local ref=$1
	shift
	if [ "${HELM_KIND_BUILD:-0}" = 1 ] || ! "$PODMAN" image exists "$ref"; then
		echo "  building $ref"
		"$PODMAN" build -t "$ref" "$@" "$REPO"
	else
		echo "  reusing $ref"
	fi
}
IMG_CUSTOS=docker.io/library/custos:local
IMG_WEB=docker.io/library/custos-web:local
IMG_VALIDATOR=docker.io/library/custos-validator:local
build_image "$IMG_CUSTOS" -f Containerfile
build_image "$IMG_WEB" --target runner -f web/Containerfile
build_image "$IMG_VALIDATOR" -f deploy/Containerfile.validator

step "kind cluster ($NODE_IMAGE)"
kind create cluster --name "$CLUSTER" --image "$NODE_IMAGE" --wait 180s
kubectl --context "$CTX" get nodes -o wide
for ref in "$IMG_CUSTOS" "$IMG_WEB" "$IMG_VALIDATOR"; do
	f=$WORK/$(basename "${ref%%:*}").tar
	"$PODMAN" save -o "$f" "$ref"
	kind load image-archive "$f" --name "$CLUSTER"
done
K="kubectl --context $CTX"
H="helm --kube-context $CTX"

step "operators and CRDs"
fetch "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API_VERSION/standard-install.yaml" "$GATEWAY_API_SHA256" "$WORK/gateway.yaml"
$K apply --server-side -f "$WORK/gateway.yaml" >/dev/null
$K wait --for=condition=Established crd/httproutes.gateway.networking.k8s.io crd/backendtlspolicies.gateway.networking.k8s.io --timeout=60s
helm repo add cnpg https://cloudnative-pg.github.io/charts --force-update >/dev/null
helm repo add openbao https://openbao.github.io/openbao-helm --force-update >/dev/null
$H install cnpg cnpg/cloudnative-pg --version "$CNPG_OPERATOR_CHART" -n cnpg-system --create-namespace --wait --timeout 5m

step "test CA and certificates"
cd "$WORK"
ossl ecparam -genkey -name prime256v1 -out ca.key
MSYS_NO_PATHCONV=1 ossl req -x509 -new -nodes -days 2 -subj /CN=custos-helm-ca -key ca.key -out ca.crt
cert() { # cert <name> <SAN list>
	ossl ecparam -genkey -name prime256v1 -out "$1.key"
	MSYS_NO_PATHCONV=1 ossl req -new -nodes -subj "/CN=$1" -key "$1.key" -out "$1.csr"
	printf 'subjectAltName=%s\n' "$2" >"$1.ext"
	ossl x509 -req -days 2 -in "$1.csr" -CA ca.crt -CAkey ca.key -CAcreateserial -extfile "$1.ext" -out "$1.crt" 2>/dev/null
	chmod 644 "$1.key"
}
cert api "DNS:$REL-api,DNS:$REL-api.$NS,DNS:$REL-api.$NS.svc,DNS:$REL-api.$NS.svc.cluster.local"
cert keycloak "DNS:keycloak,DNS:keycloak.$NS,DNS:keycloak.$NS.svc,DNS:keycloak.$NS.svc.cluster.local"
cd "$REPO"

step "fixtures (namespace, secrets, Keycloak)"
$K create namespace "$NS"
$K -n "$NS" create secret generic "$REL-api-tls" --type=kubernetes.io/tls \
	--from-file=tls.crt="$WORK/api.crt" --from-file=tls.key="$WORK/api.key" --from-file=ca.crt="$WORK/ca.crt"
$K -n "$NS" create secret generic test-ca --from-file=ca.crt="$WORK/ca.crt"
$K -n "$NS" create configmap "$REL-api-ca" --from-file=ca.crt="$WORK/ca.crt"
$K -n "$NS" create secret generic keycloak-tls --from-file=tls.crt="$WORK/keycloak.crt" --from-file=tls.key="$WORK/keycloak.key"
web_secret=$(head -c 24 /dev/urandom | base64 | tr -d '=+/\n')
printf '%s' "$web_secret" >"$WORK/web-client-secret"
head -c 32 /dev/urandom | base64 | tr -d '\n' >"$WORK/web-auth-secrets"
$K -n "$NS" create secret generic slurm-token --from-literal=token=dummy-slurmrestd-token
$K -n "$NS" create secret generic web-oidc --from-file=client-secret="$WORK/web-client-secret"
$K -n "$NS" create secret generic web-auth --from-file=auth-secrets="$WORK/web-auth-secrets"
sed "s/__CUSTOS_WEB_CLIENT_SECRET__/$web_secret/g" deploy/e2e/keycloak/realm-custos.json >"$WORK/realm-custos.json"
$K -n "$NS" create secret generic keycloak-realm --from-file=realm-custos.json="$WORK/realm-custos.json"

# Keycloak test fixture: HTTPS on 8443 with a fixed frontend URL so the issuer
# is https://keycloak.$NS.svc:8443/realms/custos for in-cluster and forwarded use.
$K -n "$NS" apply -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: keycloak
spec:
  selector: {app: keycloak}
  ports:
    - {name: https, port: 8443, targetPort: 8443}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: keycloak
spec:
  replicas: 1
  selector:
    matchLabels: {app: keycloak}
  template:
    metadata:
      labels: {app: keycloak}
    spec:
      containers:
        - name: keycloak
          image: $KEYCLOAK_IMAGE
          args: ["start-dev", "--import-realm", "--https-certificate-file=/tls/tls.crt", "--https-certificate-key-file=/tls/tls.key", "--https-port=8443"]
          env:
            - {name: KC_HOSTNAME, value: "https://keycloak.$NS.svc:8443"}
            - {name: KC_BOOTSTRAP_ADMIN_USERNAME, value: admin}
            - {name: KC_BOOTSTRAP_ADMIN_PASSWORD, value: helm-kind-admin}
          ports:
            - {containerPort: 8443}
          readinessProbe:
            httpGet: {path: /realms/custos, port: 8443, scheme: HTTPS}
            periodSeconds: 5
            failureThreshold: 60
          resources:
            requests: {cpu: 250m, memory: 768Mi}
          volumeMounts:
            - {name: tls, mountPath: /tls, readOnly: true}
            - {name: realm, mountPath: /opt/keycloak/data/import, readOnly: true}
      volumes:
        - name: tls
          secret: {secretName: keycloak-tls}
        - name: realm
          secret: {secretName: keycloak-realm}
EOF
$K -n "$NS" rollout status deploy/keycloak --timeout=6m
ISSUER=https://keycloak.$NS.svc:8443/realms/custos

cat >"$WORK/values.yaml" <<EOF
image: {repository: docker.io/library/custos, tag: local, pullPolicy: Never}
validator:
  image: {repository: docker.io/library/custos-validator, tag: local, pullPolicy: Never}
config:
  auth:
    oidc:
      issuer: $ISSUER
      client_id: custos-e2e
      audiences: [custos]
auth:
  oidc:
    caSecret: {name: test-ca, key: ca.crt}
api:
  tls:
    secretName: $REL-api-tls
postgresql:
  enabled: true
  cluster:
    instances: 1
    storage: {size: 1Gi}
web:
  image: {repository: docker.io/library/custos-web, tag: local, pullPolicy: Never}
  replicas: 1
  publicOrigin: https://127.0.0.1:3000
  client:
    id: custos-web
    secret: {name: web-oidc, key: client-secret}
  authSecrets: {name: web-auth, key: auth-secrets}
  idpCaSecret: {name: test-ca, key: ca.crt}
serve:
  replicas: 1
fileSecrets:
  - secretName: slurm-token
gateway:
  enabled: true
  parentRefs:
    - {name: not-deployed-gateway}
  web:
    hostnames: [custos.example.org]
  api:
    enabled: true
    hostnames: [api.custos.example.org]
    backendTLS:
      enabled: true
      caConfigMap: $REL-api-ca
networkPolicy:
  enabled: true
EOF

step "helm install (bundled PostgreSQL, web, gateway routes, network policies)"
helm dependency build deploy/helm/custos
$H install "$REL" deploy/helm/custos -n "$NS" -f "$WORK/values.yaml" --wait --timeout 10m
$H -n "$NS" status "$REL" | sed -n 1,6p

step "assertions"
$K -n "$NS" wait --for=condition=complete "job/$REL-migrate-r1" --timeout=60s >/dev/null
ok "migrate Job $REL-migrate-r1 complete"
$K -n "$NS" logs "job/$REL-migrate-r1" | grep -c 'migrat' >/dev/null && ok "migrate Job logged migration activity"
for d in serve worker web; do
	$K -n "$NS" rollout status "deploy/$REL-$d" --timeout=120s >/dev/null
	ok "deployment $REL-$d is Ready"
done
$K -n "$NS" get cluster.postgresql.cnpg.io "$REL-postgresql" -o jsonpath='{.status.phase}{"\n"}'
$K -n "$NS" get secret "$REL-postgresql-app" "$REL-postgresql-ca" >/dev/null && ok "CloudNativePG secrets $REL-postgresql-app / -ca exist"

for comp in serve worker; do
	pod=$($K -n "$NS" get pod -l "app.kubernetes.io/component=$comp,app.kubernetes.io/instance=$REL" -o jsonpath='{.items[0].metadata.name}')
	running=$($K -n "$NS" get pod "$pod" -o jsonpath='{.status.initContainerStatuses[?(@.name=="validator")].state.running.startedAt}')
	[ -n "$running" ] || die "validator sidecar not running in $comp pod $pod"
	waited=$($K -n "$NS" get pod "$pod" -o jsonpath='{.status.initContainerStatuses[?(@.name=="migrate-wait")].state.terminated.reason}')
	[ "$waited" = Completed ] || die "migrate-wait init container did not complete in $pod ($waited)"
	ok "$comp: validator sidecar running, migrate-wait Completed"
done

portfwd() { # portfwd <svc> <local> <remote>
	$K -n "$NS" port-forward "svc/$1" "$2:$3" >"$LOGDIR/portforward-$1.log" 2>&1 &
	PIDS+=($!)
	for _ in $(seq 1 30); do
		(exec 3<>"/dev/tcp/127.0.0.1/$2") 2>/dev/null && return 0
		sleep 1
	done
	die "port-forward to $1 did not come up"
}
portfwd "$REL-api" 18443 8443
code=$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$WORK/ca.crt" \
	--resolve "$REL-api.$NS.svc:18443:127.0.0.1" "https://$REL-api.$NS.svc:18443/health/ready")
[ "$code" = 200 ] || die "API /health/ready returned $code"
ok "API /health/ready 200 over HTTPS with the test CA"

portfwd keycloak 18444 8443
tok=$(curl -sS --cacert "$WORK/ca.crt" --resolve "keycloak.$NS.svc:18444:127.0.0.1" \
	-d grant_type=password -d client_id=custos-e2e -d client_secret=e2e-client-secret \
	-d username=alice -d password=alice-e2e-password \
	"https://keycloak.$NS.svc:18444/realms/custos/protocol/openid-connect/token" \
	| sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
[ -n "$tok" ] || die "no access token from Keycloak for alice"
ok "obtained an access token for alice (issuer $ISSUER)"
me=$(curl -sS -o "$WORK/me.json" -w '%{http_code}' --cacert "$WORK/ca.crt" \
	--resolve "$REL-api.$NS.svc:18443:127.0.0.1" -H "Authorization: Bearer $tok" \
	"https://$REL-api.$NS.svc:18443/api/v1/me")
[ "$me" = 200 ] || die "authenticated GET /api/v1/me returned $me: $(cat "$WORK/me.json")"
ok "authenticated GET /api/v1/me -> 200 ($(head -c 160 "$WORK/me.json"))"
anon=$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$WORK/ca.crt" \
	--resolve "$REL-api.$NS.svc:18443:127.0.0.1" "https://$REL-api.$NS.svc:18443/api/v1/me")
[ "$anon" = 401 ] || die "unauthenticated GET /api/v1/me returned $anon, want 401"
ok "unauthenticated GET /api/v1/me -> 401"

portfwd "$REL-web" 13000 3000
# The web proxy answers 421 unless Host matches web.publicOrigin.
code=$(curl -sS -o /dev/null -w '%{http_code}' -H "Host: 127.0.0.1:3000" http://127.0.0.1:13000/api/health)
[ "$code" = 200 ] || die "web /api/health returned $code"
ok "web /api/health 200"

# File-provider secrets: the dedicated root is configured, the internal credential
# directory is not a root, and the Secret is mounted read-only in serve and worker.
cfg=$($K -n "$NS" get configmap "$REL-config" -o jsonpath='{.data.config\.yaml}')
echo "$cfg" | grep -A1 'file_roots:' | grep -q '/etc/custos/file-secrets$' || die "file_roots is not /etc/custos/file-secrets"
if [ "$(echo "$cfg" | grep -A3 'file_roots:' | grep -c '/etc/custos/secrets')" != 0 ]; then die "internal credential directory is a file root"; fi
ok "file_roots is exactly /etc/custos/file-secrets (not /etc/custos/secrets)"
for comp in serve worker; do
	mounts=$($K -n "$NS" get pod -l "app.kubernetes.io/component=$comp,app.kubernetes.io/instance=$REL" \
		-o jsonpath='{range .items[0].spec.containers[0].volumeMounts[*]}{.mountPath}{" ro="}{.readOnly}{"\n"}{end}')
	echo "$mounts" | grep -qx '/etc/custos/file-secrets/slurm-token ro=true' || die "$comp lacks the read-only slurm-token mount: $mounts"
	mode=$($K -n "$NS" get pod -l "app.kubernetes.io/component=$comp,app.kubernetes.io/instance=$REL" \
		-o jsonpath='{.items[0].spec.volumes[?(@.name=="file-secret-slurm-token")].secret.defaultMode}')
	[ "$mode" = 288 ] || die "$comp slurm-token volume defaultMode is $mode, want 288 (0440)"
	ok "$comp: /etc/custos/file-secrets/slurm-token mounted read-only (mode 0440) and the pod is Ready"
done
for comp in web; do
	if $K -n "$NS" get pod -l "app.kubernetes.io/component=$comp,app.kubernetes.io/instance=$REL" -o yaml | grep -q file-secret; then die "$comp must not mount file secrets"; fi
done
ok "web does not mount file secrets"

$K -n "$NS" get httproute "$REL-web" "$REL-api" >/dev/null && ok "HTTPRoutes accepted by the API server"
$K -n "$NS" get backendtlspolicy "$REL-api" >/dev/null && ok "BackendTLSPolicy accepted by the API server"
$K -n "$NS" get networkpolicy "$REL-serve" "$REL-worker" "$REL-web" >/dev/null && ok "NetworkPolicies created"
if [ "$($K -n "$NS" get networkpolicy -o yaml | grep -c 8481)" != 0 ]; then die "a NetworkPolicy opens the validator port 8481"; fi
ok "no NetworkPolicy mentions 8481"

step "helm upgrade (config change)"
before=$($K -n "$NS" get pods -l "app.kubernetes.io/component=serve,app.kubernetes.io/instance=$REL" -o name | sort)
$H upgrade "$REL" deploy/helm/custos -n "$NS" -f "$WORK/values.yaml" --set config.log.level=debug --wait --timeout 10m
$K -n "$NS" wait --for=condition=complete "job/$REL-migrate-r2" --timeout=120s >/dev/null
ok "migrate Job $REL-migrate-r2 complete"
if $K -n "$NS" get "job/$REL-migrate-r1" >/dev/null 2>&1; then die "old migrate Job r1 was not pruned"; fi
ok "old migrate Job r1 pruned"
for d in serve worker web; do $K -n "$NS" rollout status "deploy/$REL-$d" --timeout=180s >/dev/null; done
after=$($K -n "$NS" get pods -l "app.kubernetes.io/component=serve,app.kubernetes.io/instance=$REL" -o name | sort)
[ "$before" != "$after" ] || die "serve pods did not roll after the config change"
ok "serve pods rolled ($(echo "$before" | tr '\n' ' ') -> $(echo "$after" | tr '\n' ' '))"

step "helm uninstall"
for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
PIDS=()
$H uninstall "$REL" -n "$NS" --wait --timeout 5m
sleep 5
left=$($K -n "$NS" get deploy,job,svc,cm,sa,pdb,networkpolicy,httproute,backendtlspolicy,cluster.postgresql.cnpg.io,pod \
	-l "app.kubernetes.io/instance=$REL" -o name 2>/dev/null || true)
[ -z "$left" ] || die "resources left after uninstall: $left"
ok "no chart resources left after uninstall"
echo "  remaining PVCs (kept by CloudNativePG policy):"
$K -n "$NS" get pvc -o name | sed 's/^/    /'

echo
echo "helm-kind: all assertions passed"
