#!/usr/bin/env bash
# Generates the local-development secrets for compose.yaml into
# deploy/compose/.secrets/ (gitignored). Safe to re-run: existing files
# are kept. Requires openssl (Git Bash on Windows has it).
set -euo pipefail
cd "$(dirname "$0")"
d=.secrets
mkdir -p "$d"
chmod 700 "$d" 2>/dev/null || true

# URL-safe base64 (compose URL files are parsed as postgres:// DSNs, so
# +, / and = must not appear in passwords).
gen() { openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\r\n'; }

for name in postgres-superuser-password custos-migrate-password custos-app-password; do
	if [ ! -s "$d/$name" ]; then
		gen > "$d/$name"
		chmod 600 "$d/$name"
	fi
done

if [ ! -s "$d/server.crt" ]; then
	# MSYS_NO_PATHCONV keeps Git Bash from rewriting /CN=... as a path.
	MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='/CN' \
		openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
		-nodes -days 365 -subj /CN=postgres \
		-addext subjectAltName=DNS:postgres \
		-keyout "$d/server.key" -out "$d/server.crt"
	chmod 600 "$d/server.key"
	chmod 644 "$d/server.crt"
fi

if [ ! -s "$d/openbao.crt" ]; then
	MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='/CN' \
		openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
		-nodes -days 365 -subj /CN=openbao \
		-addext subjectAltName=DNS:openbao \
		-keyout "$d/openbao.key" -out "$d/openbao.crt"
	chmod 600 "$d/openbao.key"
	chmod 644 "$d/openbao.crt"
fi

# Self-signed server cert doubles as the CA for verify-full.
printf 'postgres://custos_migrate:%s@postgres:5432/custos' \
	"$(cat "$d/custos-migrate-password")" > "$d/custos-migrate-url"
printf 'postgres://custos_app:%s@postgres:5432/custos' \
	"$(cat "$d/custos-app-password")" > "$d/custos-app-url"
chmod 600 "$d/custos-migrate-url" "$d/custos-app-url"

echo "secrets ready in $d/"
