SHELL := /bin/sh

.PHONY: all fmt lint test ci

all: fmt lint test

fmt:
	@command -v gofumpt >/dev/null 2>&1 || { echo "gofumpt not found. Install: go install mvdan.cc/gofumpt@latest"; exit 1; }
	gofumpt -l -w .

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not found. Install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8"; exit 1; }
	golangci-lint run --timeout=5m

test:
	go test ./... -race -covermode=atomic

ci: lint test

