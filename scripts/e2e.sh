#!/usr/bin/env bash
# End-to-end stack control: real Slurm 26.05 + Keycloak 26.7 + the
# hardened Custos runtime (see deploy/e2e/ and docs/e2e.md).
#
#   scripts/e2e.sh up      init secrets, compose up, bootstrap, print env
#   scripts/e2e.sh down    compose down -v
#   scripts/e2e.sh logs    compose logs (passes extra args, e.g. -f custos)
#
# CONTAINER_ENGINE picks the engine: podman (default) or docker.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO=$(pwd)

ENGINE=${CONTAINER_ENGINE:-podman}
CF=deploy/e2e/compose.yaml
SECRETS=deploy/e2e/.secrets
CA=$SECRETS/e2e-ca.crt
KEYCLOAK=https://keycloak.e2e:8443

die() { echo "e2e: $*" >&2; exit 1; }

compose() { "$ENGINE" compose -f "$CF" "$@"; }

# exec in a container by service name (first matching container).
cexec() {
	local svc=$1; shift
	local cid
	cid=$(compose ps -q "$svc" | head -1)
	[ -n "$cid" ] || die "no container for service $svc"
	"$ENGINE" exec "$cid" "$@"
}

cmd_up() {
	bash deploy/compose/init-secrets.sh
	bash deploy/e2e/init-secrets.sh
	# Rebuild application images so repeated local runs never reuse a stale
	# binary after migrations/config fields change.
	compose build migrate custos worker

	# The validator sidecars share the custos/worker network namespaces
	# (network_mode: service:*), so they must be recreated whenever their
	# parent is — a stale sidecar stays bound to a dead netns and
	# 127.0.0.1:8481 goes unreachable inside the new container. A plain
	# `up --force-recreate` fails: the engine refuses to remove a parent
	# while a dependent container still exists, and even `up -d` cannot
	# recreate custos/worker for a new image while the sidecars hold
	# their netns. Tear the four down in dependency order first (no-op
	# on a cold stack), then a single `up --wait` builds them fresh.
	compose rm -sf validator-api validator-worker >/dev/null 2>&1 || true
	compose rm -sf custos worker >/dev/null 2>&1 || true

	compose up -d --wait || {
		echo "e2e: stack failed to become healthy; recent logs:" >&2
		compose logs --tail=40 >&2 || true
		exit 1
	}

	echo "e2e: waiting for the shellcheck sidecars..."
	probe8481() { # probe8481 <service> — from a throwaway container in the parent netns
		local cid
		cid=$(compose ps -q "$1" | head -1)
		[ -n "$cid" ] || return 1
		"$ENGINE" run --rm --network "container:$cid" \
			docker.io/curlimages/curl:latest \
			-sf -m 5 http://127.0.0.1:8481/healthz >/dev/null 2>&1
	}
	for i in $(seq 1 30); do
		probe8481 custos && probe8481 worker && break
		sleep 2
		[ "$i" -lt 30 ] || die "validator sidecars unreachable at 127.0.0.1:8481"
	done

	echo "e2e: waiting for slurmctld to accept RPCs..."
	for i in $(seq 1 60); do
		cexec slurmctld scontrol ping >/dev/null 2>&1 && break
		sleep 2
		[ "$i" -lt 60 ] || die "slurmctld never came up"
	done

	echo "e2e: waiting for node c1 to register..."
	for i in $(seq 1 60); do
		cexec slurmctld sinfo -h -o '%T' 2>/dev/null | grep -qE 'idle|mixed|alloc' && break
		sleep 2
		[ "$i" -lt 60 ] || die "node c1 never registered"
	done

	echo "e2e: bootstrapping Slurm user 'custos'..."
	for svc in slurmctld slurmd slurmrestd slurmdbd; do
		cexec "$svc" sh -c 'id custos >/dev/null 2>&1 || useradd -m -u 2000 custos'
	done

	echo "e2e: waiting for slurmdbd to accept accounting writes..."
	for i in $(seq 1 60); do
		cexec slurmctld sacctmgr show cluster 2>/dev/null | grep -q e2e && break
		sleep 2
		[ "$i" -lt 60 ] || die "slurmdbd never came up"
	done

	echo "e2e: bootstrapping accounting (account e2e-acct, qos normal+high, user custos)..."
	have() { # have <entity> <field> <name>
		cexec slurmctld sacctmgr -n -P list "$1" format="$2" 2>/dev/null \
			| grep -qx "$3"
	}
	have cluster cluster e2e || cexec slurmctld sacctmgr -i add cluster e2e
	have account account e2e-acct || cexec slurmctld sacctmgr -i add account e2e-acct
	have qos name normal || cexec slurmctld sacctmgr -i add qos normal
	have qos name high || cexec slurmctld sacctmgr -i add qos high
	cexec slurmctld sacctmgr -n -P list assoc format=cluster,account,user \
		| awk -F'|' '$1=="e2e"&&$2=="e2e-acct"&&$3=="custos"{f=1}END{exit !f}' \
		|| cexec slurmctld sacctmgr -i add user name=custos account=e2e-acct
	cexec slurmctld sacctmgr -n -P list account format=account \
		| grep -qx e2e-acct || die "e2e-acct missing after accounting bootstrap"

	# `sacctmgr add cluster` creates a stub with no control port; job
	# records are only written once slurmctld re-registers, which it
	# does on (re)connect. Restart it, then wait for the port to appear.
	echo "e2e: restarting slurmctld to register the cluster with slurmdbd..."
	compose restart slurmctld >/dev/null
	for i in $(seq 1 60); do
		cexec slurmctld scontrol ping >/dev/null 2>&1 || {
			sleep 2; [ "$i" -lt 60 ] || die "slurmctld never came back"; continue
		}
		port=$(cexec slurmctld sacctmgr -n -P show cluster \
			format=controlport 2>/dev/null | head -1)
		[ "$port" = "6817" ] && break
		sleep 2
		[ "$i" -lt 60 ] || die "cluster e2e never registered with slurmdbd"
	done

	echo "e2e: minting Slurm JWT for 'custos'..."
	token=$(cexec slurmctld scontrol token username=custos lifespan=525600 \
		| grep -o 'SLURM_JWT=[^ ]*' | cut -d= -f2-)
	[ -n "$token" ] || die "failed to mint Slurm JWT"
	printf '%s' "$token" > "$SECRETS/slurm/token"
	chmod 600 "$SECRETS/slurm/token"

	if [ -s "$SECRETS/openbao-bootstrap-token" ]; then
		echo "e2e: storing Slurm credential in platform OpenBao..."
		MSYS_NO_PATHCONV=1 cexec openbao env BAO_ADDR=https://openbao:8200 \
			BAO_CACERT=/openbao/tls/openbao.crt \
			BAO_NAMESPACE=custos \
			BAO_TOKEN="$(cat "$SECRETS/openbao-bootstrap-token")" \
			bao kv put kv/clusters/e2e token="$token" >/dev/null
	fi

	echo "e2e: seeding the customer-managed OpenBao stand-in..."
	cexec openbao-byo env BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN=e2e-byo-token \
		bao namespace create customer >/dev/null 2>&1 || true
	cexec openbao-byo env BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN=e2e-byo-token \
		BAO_NAMESPACE=customer bao secrets enable -path=kv kv-v2 >/dev/null 2>&1 || true
	cexec openbao-byo env BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN=e2e-byo-token \
		BAO_NAMESPACE=customer bao kv put kv/e2e value=byo-e2e-value >/dev/null

	echo "e2e: waiting for Custos readiness..."
	for i in $(seq 1 60); do
		if curl -sf --ssl-no-revoke --cacert "$CA" https://127.0.0.1:8080/health/ready >/dev/null 2>&1; then
			break
		fi
		sleep 2
		[ "$i" -lt 60 ] || die "custos never became ready"
	done

	echo "e2e: granting platform-admin role to the 'platform-admin' user..."
	# Resolve the user's `sub` from an access token (password grant).
	resp=$(curl -sf --ssl-no-revoke --cacert "$CA" \
		--resolve keycloak.e2e:8443:127.0.0.1 \
		-d grant_type=password -d client_id=custos-e2e \
		-d client_secret="$(cat "$SECRETS/keycloak-client-secret")" \
		-d username=platform-admin -d password=platform-admin-e2e-password \
		-d scope=openid \
		"$KEYCLOAK/realms/custos/protocol/openid-connect/token")
	at=$(printf '%s' "$resp" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
	payload=$(printf '%s' "$at" | cut -d. -f2 | tr '_-' '/+')
	pad=$(( (4 - ${#payload} % 4) % 4 ))
	sub=$(printf '%s%.*s' "$payload" "$pad" '===' | base64 -d 2>/dev/null \
		| sed -n 's/.*"sub":"\([^"]*\)".*/\1/p')
	[ -n "$sub" ] || die "could not resolve platform-admin sub"
	MSYS_NO_PATHCONV=1 cexec custos /custos admin platform-role grant \
		--issuer "$KEYCLOAK/realms/custos" --subject "$sub" \
		--role platform-admin >/dev/null

	# Seed the tenant, claim rules and the admin's manual membership
	# here — before the suite mints any user token — so the first /me
	# reconciles IdP claim memberships immediately instead of waiting
	# out the 5-minute claims-sync freshness window mid-test.
	echo "e2e: seeding tenant acme + claim rules..."
	kcurl() { # kcurl <method> <path> [json]
		local m=$1 p=$2 d=${3:-}
		if [ -n "$d" ]; then
			curl -sf --ssl-no-revoke --cacert "$CA" -X "$m" \
				-H "Authorization: Bearer $at" \
				-H 'Content-Type: application/json' \
				-d "$d" "https://127.0.0.1:8080/api/v1$p"
		else
			curl -sf --ssl-no-revoke --cacert "$CA" -X "$m" \
				-H "Authorization: Bearer $at" \
				"https://127.0.0.1:8080/api/v1$p"
		fi
	}
	kcurl POST /tenants \
		'{"slug":"acme","name":"ACME E2E"}' >/dev/null 2>&1 || true
	kcurl POST /tenants/acme/claim-rules \
		'{"claim":"groups","match_value":"hpc-a","roles":["researcher"]}' \
		>/dev/null 2>&1 || true
	kcurl POST /tenants/acme/claim-rules \
		'{"claim":"groups","match_value":"hpc-admins","roles":["tenant-admin"]}' \
		>/dev/null 2>&1 || true
	admin_uid=$(kcurl GET /me | sed -n 's/.*"user_id":"\([^"]*\)".*/\1/p')
	kcurl POST /tenants/acme/members \
		"{\"user_id\":\"$admin_uid\",\"roles\":[\"tenant-admin\"]}" \
		>/dev/null 2>&1 || true

	echo "e2e: seeding Alice's tenant secret in OpenBao..."
	tenant_json=$(kcurl GET /tenants/acme)
	tenant_id=$(printf '%s' "$tenant_json" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
	alice_resp=$(curl -sf --ssl-no-revoke --cacert "$CA" \
		--resolve keycloak.e2e:8443:127.0.0.1 \
		-d grant_type=password -d client_id=custos-e2e \
		-d client_secret="$(cat "$SECRETS/keycloak-client-secret")" \
		-d username=alice -d password=alice-e2e-password -d scope=openid \
		"$KEYCLOAK/realms/custos/protocol/openid-connect/token")
	alice_at=$(printf '%s' "$alice_resp" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
	alice_me=$(curl -sf --ssl-no-revoke --cacert "$CA" \
		-H "Authorization: Bearer $alice_at" https://127.0.0.1:8080/api/v1/me)
	alice_id=$(printf '%s' "$alice_me" | sed -n 's/.*"user_id":"\([^"]*\)".*/\1/p')
	[ -n "$tenant_id" ] && [ -n "$alice_id" ] || die "tenant/alice id lookup failed"
	# Idempotent safety net for reruns where Alice was provisioned before
	# claim rules existed and the claims-sync freshness stamp is still live.
	kcurl POST /tenants/acme/members \
		"{\"user_id\":\"$alice_id\",\"roles\":[\"researcher\"]}" \
		>/dev/null 2>&1 || true
	if [ -s "$SECRETS/openbao-bootstrap-token" ]; then
		MSYS_NO_PATHCONV=1 cexec openbao env BAO_ADDR=https://openbao:8200 \
			BAO_CACERT=/openbao/tls/openbao.crt \
			BAO_NAMESPACE="custos/tenants/$tenant_id" \
			BAO_TOKEN="$(cat "$SECRETS/openbao-bootstrap-token")" \
			bao kv put "kv/users/$alice_id/hf-token" value=e2e-hf-token >/dev/null
		MSYS_NO_PATHCONV=1 cexec openbao env BAO_ADDR=https://openbao:8200 \
			BAO_CACERT=/openbao/tls/openbao.crt \
			BAO_NAMESPACE=custos \
			BAO_TOKEN="$(cat "$SECRETS/openbao-bootstrap-token")" \
			bao token revoke -self >/dev/null
		rm -f "$SECRETS/openbao-bootstrap-token"
	fi

	cat <<EOF

e2e stack is up.

Host resolution (required once, needs admin):
  add to C:\\Windows\\System32\\drivers\\etc\\hosts (or /etc/hosts):
    127.0.0.1 keycloak.e2e

Test environment:
  export CUSTOS_E2E=1
  export CUSTOS_E2E_API=https://127.0.0.1:8080
  export CUSTOS_E2E_CA=$REPO/$CA
  export CUSTOS_E2E_KEYCLOAK=$KEYCLOAK/realms/custos
  export CUSTOS_E2E_CLIENT_ID=custos-e2e
  export CUSTOS_E2E_CLIENT_SECRET=e2e-client-secret
  # test-only users (deploy/e2e/keycloak/realm-custos.json):
  #   alice / alice-e2e-password           (group hpc-a)
  #   bob / bob-e2e-password               (no group)
  #   platform-admin / platform-admin-e2e-password (group hpc-admins)

Then: go test ./test/e2e/... -count=1 -v
EOF
}

cmd_down() {
	compose down -v
}

cmd_logs() {
	compose logs "$@"
}

case "${1:-}" in
	up) cmd_up ;;
	down) cmd_down ;;
	logs) shift; cmd_logs "$@" ;;
	*) die "usage: $0 up|down|logs [args]" ;;
esac
