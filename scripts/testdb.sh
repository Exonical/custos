#!/usr/bin/env bash
# Manage the throwaway test PostgreSQL (deploy/compose/compose.test.yaml).
#
#   bash scripts/testdb.sh up    # start the stack and wait for health
#   bash scripts/testdb.sh down  # stop it and remove volumes
#   bash scripts/testdb.sh url   # print the test DSN
#
# Usage:
#   bash scripts/testdb.sh up
#   export CUSTOS_TEST_DATABASE_URL="$(bash scripts/testdb.sh url)"
#   go test ./...
set -euo pipefail

cd "$(dirname "$0")/.."

PW="${CUSTOS_TEST_DB_PASSWORD:-custos-test}"
DSN="postgres://postgres:${PW}@127.0.0.1:5433/postgres?sslmode=disable"

case "${1:-}" in
up)
	podman compose -f deploy/compose/compose.test.yaml up -d --wait
	;;
down)
	podman compose -f deploy/compose/compose.test.yaml down -v
	;;
url)
	echo "${DSN}"
	;;
*)
	echo "usage: $0 up|down|url" >&2
	exit 2
	;;
esac
