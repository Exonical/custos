#!/usr/bin/env bash
# Static checks for the Helm chart in deploy/helm/custos:
#   1. helm dependency build
#   2. helm lint --strict for every deploy/helm/custos/ci/*-values.yaml
#   3. helm template | kubeconform -strict (built-in schemas plus Gateway API,
#      cert-manager, CloudNativePG and Prometheus Operator CRD schemas)
#   4. config checksum sanity (the pod annotation rolls with the config)
#   5. helm unittest
#
# Tool versions are pinned below. Linux amd64 binaries are downloaded and
# checked against pinned SHA-256 sums when missing; elsewhere (or to use your
# own copies) set HELM_BIN / KUBECONFORM_BIN and install the helm-unittest
# plugin yourself. Requires network access for dependencies and schemas.
set -euo pipefail
cd "$(dirname "$0")/.."

CHART=deploy/helm/custos

HELM_VERSION=v4.3.0                  # published 2026-09-09
HELM_LINUX_AMD64_SHA256=86584a54def73570558f66f5111cc53dfed56689637ae32c1201205d494f54fb
KUBECONFORM_VERSION=v0.8.0           # published 2026-06-04
KUBECONFORM_LINUX_AMD64_SHA256=9bc2bffbf71f261128533edaf912153948b7ff238f9a531ae6d34466ec287883
HELM_UNITTEST_VERSION=v1.2.0         # published 2026-10-01
HELM_UNITTEST_LINUX_AMD64_SHA256=115c690234847d316f0a814beb9cceaf9c21bb407173179c0e82076bdce1efc0
# datreeio/CRDs-catalog commit of 2026-09-29 (schemas for the CRDs we render).
CRDS_CATALOG_REF=d373c2da9702bc9509a004db83e57263fe3bdfc1
KUBE_VERSION=${HELM_CHECK_KUBE_VERSION:-1.34.0}

TOOLS=${HELM_CHECK_TOOLS:-${XDG_CACHE_HOME:-$HOME/.cache}/custos-helm-tools}

die() { echo "helm-check: $*" >&2; exit 1; }
step() { echo; echo "== $*"; }

linux_amd64() { [ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ]; }

fetch() { # fetch <url> <sha256> <dest>
	curl -fsSL --retry 3 -o "$3" "$1"
	echo "$2  $3" | sha256sum -c - >/dev/null || die "checksum mismatch for $1"
}

HELM=${HELM_BIN:-}
if [ -z "$HELM" ] && command -v helm >/dev/null 2>&1; then HELM=$(command -v helm); fi
if [ -z "$HELM" ]; then
	linux_amd64 || die "helm not found; install helm $HELM_VERSION and set HELM_BIN"
	mkdir -p "$TOOLS/helm"
	fetch "https://get.helm.sh/helm-$HELM_VERSION-linux-amd64.tar.gz" "$HELM_LINUX_AMD64_SHA256" "$TOOLS/helm.tgz"
	tar -xzf "$TOOLS/helm.tgz" -C "$TOOLS/helm" --strip-components=1 linux-amd64/helm
	HELM=$TOOLS/helm/helm
fi
"$HELM" version --short | grep -q "^$HELM_VERSION" || die "helm $HELM_VERSION required, found $("$HELM" version --short)"

KUBECONFORM=${KUBECONFORM_BIN:-}
if [ -z "$KUBECONFORM" ] && command -v kubeconform >/dev/null 2>&1; then KUBECONFORM=$(command -v kubeconform); fi
if [ -z "$KUBECONFORM" ]; then
	linux_amd64 || die "kubeconform not found; install $KUBECONFORM_VERSION and set KUBECONFORM_BIN"
	mkdir -p "$TOOLS/kubeconform"
	fetch "https://github.com/yannh/kubeconform/releases/download/$KUBECONFORM_VERSION/kubeconform-linux-amd64.tar.gz" \
		"$KUBECONFORM_LINUX_AMD64_SHA256" "$TOOLS/kubeconform.tgz"
	tar -xzf "$TOOLS/kubeconform.tgz" -C "$TOOLS/kubeconform" kubeconform
	KUBECONFORM=$TOOLS/kubeconform/kubeconform
fi
"$KUBECONFORM" -v | grep -q "$KUBECONFORM_VERSION" || die "kubeconform $KUBECONFORM_VERSION required"

if ! "$HELM" plugin list 2>/dev/null | grep -E '^unittest[[:space:]]' | grep -q "${HELM_UNITTEST_VERSION#v}"; then
	linux_amd64 || die "helm-unittest ${HELM_UNITTEST_VERSION} plugin required (helm plugin install https://github.com/helm-unittest/helm-unittest --version $HELM_UNITTEST_VERSION)"
	"$HELM" plugin uninstall unittest >/dev/null 2>&1 || true
	rm -rf "$("$HELM" env HELM_PLUGINS)/unittest"
	rm -rf "$TOOLS/unittest" && mkdir -p "$TOOLS/unittest"
	fetch "https://github.com/helm-unittest/helm-unittest/releases/download/$HELM_UNITTEST_VERSION/helm-unittest-linux-amd64-${HELM_UNITTEST_VERSION#v}.tgz" \
		"$HELM_UNITTEST_LINUX_AMD64_SHA256" "$TOOLS/unittest.tgz"
	tar -xzf "$TOOLS/unittest.tgz" -C "$TOOLS/unittest"
	plugin_dir=$(dirname "$(find "$TOOLS/unittest" -name plugin.yaml | head -1)")
	# Copy instead of `helm plugin install`: the archive is the verified release asset and needs no install hook.
	mkdir -p "$("$HELM" env HELM_PLUGINS)"
	cp -r "$plugin_dir" "$("$HELM" env HELM_PLUGINS)/unittest"
fi

step "helm dependency build"
"$HELM" repo add cnpg https://cloudnative-pg.github.io/charts --force-update >/dev/null
"$HELM" repo add openbao https://openbao.github.io/openbao-helm --force-update >/dev/null
"$HELM" dependency build "$CHART"

shopt -s nullglob
VALUES=("$CHART"/ci/*-values.yaml)
[ "${#VALUES[@]}" -gt 0 ] || die "no ci values files under $CHART/ci"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

for v in "${VALUES[@]}"; do
	name=$(basename "$v" -values.yaml)

	step "helm lint --strict ($name)"
	"$HELM" lint "$CHART" --strict -f "$v" --kube-version "$KUBE_VERSION" 2>&1 | sed 's/^/  /'

	step "kubeconform ($name)"
	"$HELM" template custos "$CHART" -n custos -f "$v" --kube-version "$KUBE_VERSION" >"$WORK/$name.yaml"
	"$KUBECONFORM" -strict -summary -kubernetes-version "$KUBE_VERSION" \
		-schema-location default \
		-schema-location "https://raw.githubusercontent.com/datreeio/CRDs-catalog/$CRDS_CATALOG_REF/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json" \
		"$WORK/$name.yaml"
done

step "config checksum rolls pods"
checksum() { "$HELM" template custos "$CHART" -n custos --kube-version "$KUBE_VERSION" \
	-f "$CHART/ci/external-minimal-values.yaml" "$@" -s templates/serve-deployment.yaml | awk '/checksum\/config:/ {print $2; exit}'; }
base=$(checksum)
again=$(checksum)
changed=$(checksum --set config.log.level=debug)
[ -n "$base" ] || die "no checksum/config annotation found"
[ "$base" = "$again" ] || die "checksum is not deterministic"
[ "$base" != "$changed" ] || die "checksum did not change with the config"
echo "  ok ($base -> $changed)"

step "helm unittest"
"$HELM" unittest "$CHART" --with-subchart=false

echo
echo "helm-check: all checks passed (helm $HELM_VERSION, kubeconform $KUBECONFORM_VERSION, helm-unittest $HELM_UNITTEST_VERSION)"
