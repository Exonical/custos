# Manage the throwaway test PostgreSQL (deploy/compose/compose.test.yaml).
#
#   .\scripts\testdb.ps1 up    # start the stack and wait for health
#   .\scripts\testdb.ps1 down  # stop it and remove volumes
#   .\scripts\testdb.ps1 url   # print the test DSN
#
# Usage:
#   .\scripts\testdb.ps1 up
#   $env:CUSTOS_TEST_DATABASE_URL = (bash scripts/testdb.sh url)
#   go test ./...
param(
    [Parameter(Mandatory = $true, Position = 0)]
    [ValidateSet('up', 'down', 'url')]
    [string]$Command
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

$pw = if ($env:CUSTOS_TEST_DB_PASSWORD) { $env:CUSTOS_TEST_DB_PASSWORD } else { 'custos-test' }
$dsn = "postgres://postgres:${pw}@127.0.0.1:5433/postgres?sslmode=disable"

switch ($Command) {
    'up'   { podman compose -f deploy/compose/compose.test.yaml up -d --wait }
    'down' { podman compose -f deploy/compose/compose.test.yaml down -v }
    'url'  { Write-Output $dsn }
}
