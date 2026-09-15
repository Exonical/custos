#!/usr/bin/env bash
# Runs once at initdb (docker-entrypoint-initdb.d) as the superuser.
# Creates the split roles from ADR-013: custos_migrate owns the schema,
# custos_app is the least-privilege runtime role.
set -euo pipefail

mig_pw=$(cat /run/secrets/custos-migrate-password)
app_pw=$(cat /run/secrets/custos-app-password)

# Passwords go in as psql variables, never interpolated into SQL text.
# Local socket auth is still trust during initdb (00-harden-hba.sh
# flips pg_hba to scram before the real server starts).
psql -v ON_ERROR_STOP=1 -v mig_pw="$mig_pw" -v app_pw="$app_pw" \
	--username "$POSTGRES_USER" --dbname postgres <<'SQL'
CREATE ROLE custos_migrate LOGIN PASSWORD :'mig_pw';
CREATE ROLE custos_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT
  PASSWORD :'app_pw';
CREATE DATABASE custos OWNER custos_migrate;
SQL

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname custos \
	-c "REVOKE CREATE ON SCHEMA public FROM PUBLIC"
