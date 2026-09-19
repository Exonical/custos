#!/usr/bin/env bash
# Generates the e2e stack's secrets into deploy/e2e/.secrets/
# (gitignored): a private CA, server certs for nginx (keycloak.e2e,
# slurmrestd.e2e) and the Custos API (localhost/127.0.0.1), the Slurm
# auth keys (slurm.key, jwt_hs256.key), the slurmdbd password/config
# and a placeholder for the minted JWT. Safe to re-run: existing files
# are kept. Requires openssl (Git Bash on Windows has it).
set -euo pipefail
cd "$(dirname "$0")"
d=.secrets
mkdir -p "$d" "$d/slurm"
chmod 700 "$d" 2>/dev/null || true

gen() { openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\r\n'; }

# --- private CA (certifies nginx + Custos API server certs) ---------
if [ ! -s "$d/e2e-ca.key" ]; then
	openssl ecparam -genkey -name prime256v1 -out "$d/e2e-ca.key"
	MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='/CN' \
		openssl req -x509 -new -nodes -days 3650 \
		-subj /CN=custos-e2e-ca \
		-key "$d/e2e-ca.key" -out "$d/e2e-ca.crt"
	chmod 600 "$d/e2e-ca.key"
fi

# cert() <out-name> <CN> <SAN>
cert() {
	local name=$1 cn=$2 san=$3
	if [ ! -s "$d/$name.crt" ]; then
		openssl ecparam -genkey -name prime256v1 -out "$d/$name.key"
		MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='/CN' \
			openssl req -new -nodes -subj "/CN=$cn" \
			-key "$d/$name.key" -out "$d/$name.csr"
		printf 'subjectAltName=%s\n' "$san" > "$d/$name.ext"
		openssl x509 -req -days 365 -in "$d/$name.csr" \
			-CA "$d/e2e-ca.crt" -CAkey "$d/e2e-ca.key" -CAcreateserial \
			-extfile "$d/$name.ext" -out "$d/$name.crt" 2>/dev/null
		rm -f "$d/$name.csr" "$d/$name.ext"
		chmod 600 "$d/$name.key"
		chmod 644 "$d/$name.crt"
	fi
}

# nginx terminates TLS for slurmrestd and Keycloak on the compose
# network; the Custos API does its own TLS with api.crt.
cert slurmrestd slurmrestd.e2e "DNS:slurmrestd.e2e"
cert keycloak keycloak.e2e "DNS:keycloak.e2e"
cert api custos.e2e "DNS:custos.e2e,DNS:localhost,IP:127.0.0.1"

# --- Slurm auth keys -------------------------------------------------
# auth/slurm shared secret (any file; slurm.key format is opaque).
if [ ! -s "$d/slurm/slurm.key" ]; then
	openssl rand -out "$d/slurm/slurm.key" 1024
fi
# auth/jwt HS256 key must be >=32 bytes ASCII.
if [ ! -s "$d/slurm/jwt_hs256.key" ]; then
	gen > "$d/slurm/jwt_hs256.key"
fi
# slurmdbd StoragePass (plain text in slurmdbd.conf, test-only).
if [ ! -s "$d/slurmdb-password" ]; then
	gen > "$d/slurmdb-password"
fi
# slurmdbd.conf is generated because it embeds the storage password.
cat > "$d/slurm/slurmdbd.conf" <<EOF
AuthType=auth/slurm
AuthAltTypes=auth/jwt
AuthAltParameters=jwt_key=/etc/slurm/jwt_hs256.key
DbdHost=slurmdbd
DbdPort=6819
PidFile=/run/slurmdbd/dbd.pid
SlurmUser=slurm
StorageType=accounting_storage/mysql
StorageHost=mariadb
StoragePort=3306
StorageUser=slurm
StoragePass=$(cat "$d/slurmdb-password")
StorageLoc=slurm_acct_db
EOF
# The JWT is minted by e2e.sh after slurmctld is up.
mkdir -p "$d/slurm"
touch "$d/slurm/token"
chmod 600 "$d/slurm/token"

# Keycloak confidential-client secret (test-only, documented).
if [ ! -s "$d/keycloak-client-secret" ]; then
	printf 'e2e-client-secret' > "$d/keycloak-client-secret"
	chmod 600 "$d/keycloak-client-secret"
fi
# Keycloak bootstrap admin (test-only, used only for emergency console
# access — tests never log in as admin).
if [ ! -s "$d/keycloak-admin-password" ]; then
	gen > "$d/keycloak-admin-password"
	chmod 600 "$d/keycloak-admin-password"
fi

echo "e2e secrets ready in $d/"
