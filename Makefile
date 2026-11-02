# CredOrbit build helpers. Requires Go (see go.mod) and POSIX make.

BINARY   := credorbit
PKG      := github.com/credorbit/credorbit
MAIN     := ./cmd/credorbit
DIST     := dist

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS  := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(COMMIT) \
	-X $(PKG)/internal/version.Date=$(DATE)
GOFLAGS_BUILD := -trimpath -ldflags "$(LDFLAGS)"

GOBIN    ?= $(shell go env GOBIN)
INSTALL_DIR := $(if $(GOBIN),$(GOBIN),$(HOME)/bin)

PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

.PHONY: all build install test race cover lint fmt vet cross completions snapshot release-check clean help

all: lint test build

help: ## Show targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

build: ## Build ./credorbit for this platform
	CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -o $(BINARY)$(shell go env GOEXE) $(MAIN)

install: build ## Install to $GOBIN or ~/bin
	mkdir -p "$(INSTALL_DIR)"
	install -m 0755 $(BINARY)$(shell go env GOEXE) "$(INSTALL_DIR)/"
	@echo "installed $(INSTALL_DIR)/$(BINARY)"

test: ## Run unit tests
	go test ./...

race: ## Run tests with the race detector and coverage
	go test -race -coverprofile=coverage.out -covermode=atomic ./...

cover: race ## Show coverage summary
	go tool cover -func=coverage.out | tail -n 1

fmt: ## Format sources
	gofmt -w .

vet:
	go vet ./...

lint: vet ## go vet + gofmt check (+ golangci-lint if installed)
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed; skipped"; fi

cross: ## Build all release platforms into dist/
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
		out="$(DIST)/$(BINARY)_$${os}_$${arch}$$ext"; \
		echo "build $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS_BUILD) -o "$$out" $(MAIN) || exit 1; \
	done

completions: ## Generate shell completions into completions/
	@mkdir -p completions
	go run $(MAIN) completion bash > completions/$(BINARY).bash
	go run $(MAIN) completion zsh > completions/$(BINARY).zsh
	go run $(MAIN) completion fish > completions/$(BINARY).fish
	go run $(MAIN) completion powershell > completions/$(BINARY).ps1

snapshot: ## Local GoReleaser snapshot build (no publishing)
	@if command -v goreleaser >/dev/null 2>&1; then goreleaser release --snapshot --clean; \
	else echo "goreleaser not installed; use 'make cross' instead"; exit 1; fi

release-check: ## Validate .goreleaser.yaml
	@if command -v goreleaser >/dev/null 2>&1; then goreleaser check; \
	else echo "goreleaser not installed; skipped"; fi

clean: ## Remove build outputs
	rm -rf $(DIST) completions coverage.out $(BINARY) $(BINARY).exe
