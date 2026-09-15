.PHONY: build test lint vet vuln fmt-check migrate openapi openapi-check docs-config docs-config-check

VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/custos ./cmd/custos

test:
	go test -race ./...

lint:
	golangci-lint run ./...

vet:
	go vet ./...

vuln:
	govulncheck ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; exit 1)

migrate:
	go run ./cmd/custos migrate up

openapi:
	go generate ./pkg/api/v1

openapi-check: openapi
	@git diff --exit-code -- pkg/api || (echo "pkg/api is stale; run make openapi"; exit 1)

docs-config:
	go run ./tools/docsconfig

docs-config-check: docs-config
	@git diff --exit-code -- docs/configuration.md || (echo "docs/configuration.md is stale; run make docs-config"; exit 1)
