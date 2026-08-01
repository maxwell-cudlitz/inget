# Task runner for inget. Binaries build to bin/ with version metadata injected at link
# time. Every target is safe to run offline except `tidy`.

BIN_DIR   := bin
MODULE    := github.com/maxwellcudlitz/inget
STAMP_PKG := $(MODULE)/internal/cli

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(STAMP_PKG).Version=$(VERSION) \
	-X $(STAMP_PKG).Commit=$(COMMIT) \
	-X $(STAMP_PKG).Date=$(DATE)

GOLANGCI_VERSION := v2.12.2

.PHONY: all build test cover lint lint-install fmt tidy run clean

all: build test lint

## build: compile every command in cmd/ into bin/
build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/ ./cmd/...

## test: run the unit test suite with the race detector
test:
	go test -race ./...

## cover: run tests and report per-package coverage
cover:
	go test -race -cover ./...

## lint: vet plus golangci-lint when it is installed
lint:
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed; skipping (run 'make lint-install'). CI enforces it." >&2; \
	fi

## lint-install: install the pinned golangci-lint into GOPATH/bin
lint-install:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

## fmt: rewrite sources with gofmt
fmt:
	gofmt -l -w .

## tidy: reconcile go.mod and go.sum (requires network)
tidy:
	go mod tidy

## run: build then invoke bin/inget (pass args with ARGS="...")
run: build
	./$(BIN_DIR)/inget $(ARGS)

## clean: remove build output
clean:
	rm -rf $(BIN_DIR)
