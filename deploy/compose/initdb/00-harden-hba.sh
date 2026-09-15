#!/usr/bin/env bash
# The stock entrypoint's bootstrap psql needs local trust while initdb.d
# runs, so initdb starts with trust for local and we rewrite pg_hba.conf
# here — the real server reads the file fresh on its first start.
set -euo pipefail
sed -i 's/\btrust\b/scram-sha-256/g; s/\bmd5\b/scram-sha-256/g; s/\bpassword\b/scram-sha-256/g' \
	"$PGDATA/pg_hba.conf"
