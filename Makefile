# gotwamp Makefile. Run `make help` for the list of targets.

GO      ?= go
GOBIN   ?= $(shell $(GO) env GOPATH)/bin
BIN_DIR ?= bin
# RACE is the race detector flag of the test targets. CI empties it on a
# runner whose kernel the race runtime cannot run on, and says so.
RACE    ?= -race
export PATH := $(GOBIN):$(PATH)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

COMMANDS := gotwampd gotwamp
GEN_DIR  := internal/api/gen
# Go sources checked by gofumpt (generated code is excluded).
GO_FILES = $(shell find . -name '*.go' -not -path './.git/*' -not -path './$(GEN_DIR)/*')

# Tool versions installed by `make tools`.
BUF_VERSION                ?= v1.73.0
PROTOC_GEN_GO_VERSION      ?= v1.36.12
PROTOC_GEN_GO_GRPC_VERSION ?= v1.6.2
GOFUMPT_VERSION            ?= v0.12.0
GOLANGCI_LINT_VERSION      ?= v2.14.0
GOVULNCHECK_VERSION        ?= v1.8.0
DECLSCOPE_VERSION          ?= v0.14.0

# The docker compose staging (deploy/staging, docs/ops/staging.md).
STAGING      := docker compose -f deploy/staging/docker-compose.yml
STAGING_PING := $(STAGING) exec -T sender gotwamp ping -q -c 50 -i 20ms

.DEFAULT_GOAL := build

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build all packages and the binaries into bin/
	CGO_ENABLED=0 $(GO) build ./...
	@mkdir -p $(BIN_DIR)
	$(foreach c,$(COMMANDS),CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(c) ./cmd/$(c) &&) true

.PHONY: test
test: test-library ## Run the tests with the race detector
	CGO_ENABLED=1 $(GO) test $(RACE) ./...

.PHONY: test-library
test-library: ## Test embedding from an independent Go module
	cd test/library && CGO_ENABLED=1 $(GO) test $(RACE) ./...

# Soak test (test/soak): SOAK_SESSIONS sessions at SOAK_PPS packets per second
# for SOAK_DURATION. The 24-hour M5 run: make soak SOAK_SESSIONS=100 SOAK_DURATION=24h
SOAK_SESSIONS ?= 20
SOAK_PPS      ?= 10
SOAK_DURATION ?= 60s
SOAK_ARGS     ?=

.PHONY: soak
soak: ## Run the soak test: sessions, exporters and a loopback reflector in one process, checked for leaks
	CGO_ENABLED=0 $(GO) test -tags soak -count=1 -timeout 0 -v -run TestSoak ./test/soak/ \
		-args -soak.sessions=$(SOAK_SESSIONS) -soak.pps=$(SOAK_PPS) -soak.duration=$(SOAK_DURATION) $(SOAK_ARGS)

.PHONY: test-component
test-component: ## Run the netns + netem component tests (needs sudo)
	CGO_ENABLED=1 $(GO) test $(RACE) -tags integration -exec sudo -count=1 ./test/component/...

# perfSONAR testpoint for make interop (test/interop/perfsonar/compose.yaml).
INTEROP_COMPOSE := docker compose -f test/interop/perfsonar/compose.yaml

.PHONY: interop
interop: ## Run the perfSONAR interop tests in Docker (INTEROP_CLI=1: also the CLI script; INTEROP_KEEP=1: keep the container)
	$(INTEROP_COMPOSE) up -d --wait
	@rc=0; \
	CGO_ENABLED=1 $(GO) test $(RACE) -tags interop -count=1 -v ./test/interop/... || rc=$$?; \
	if [ -n "$(INTEROP_CLI)" ]; then test/interop/perfsonar/run.sh || rc=$$?; fi; \
	if [ -z "$(INTEROP_KEEP)" ]; then $(INTEROP_COMPOSE) down; fi; \
	exit $$rc

.PHONY: cover
cover: test-library ## Run the tests with coverage and print the per-function summary
	CGO_ENABLED=1 $(GO) test $(RACE) -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -n 1

.PHONY: lint
lint: lint-fmt lint-vet lint-golangci lint-declscope lint-proto ## Run every linter

.PHONY: lint-fmt
lint-fmt: ## Check gofumpt formatting
	@out="$$(gofumpt -l $(GO_FILES))"; \
	if [ -n "$$out" ]; then echo "gofumpt: files need formatting:"; echo "$$out"; exit 1; fi

.PHONY: lint-vet
lint-vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint-golangci
lint-golangci: ## Run golangci-lint
	golangci-lint run ./...

.PHONY: lint-declscope
lint-declscope: ## Run declscope shrink, then the declscope analyzer
	declscope shrink ./...
	declscope ./...

.PHONY: lint-proto
lint-proto: ## Run buf lint and check buf formatting
	buf lint
	buf format --diff --exit-code

.PHONY: proto
proto: ## Generate Go code from api/proto into internal/api/gen
	buf generate

.PHONY: proto-breaking
proto-breaking: ## Check the API for breaking changes against main (AGAINST=<buf input>)
	buf breaking --against '$(or $(AGAINST),.git#branch=main)'

.PHONY: vuln
vuln: ## Run govulncheck
	govulncheck ./...

.PHONY: fmt
fmt: ## Format Go and proto sources
	gofumpt -w $(GO_FILES)
	buf format --write

.PHONY: docker
docker: ## Build the container image from deploy/docker/Dockerfile (IMAGE=name:tag)
	docker build -f deploy/docker/Dockerfile --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-t $(or $(IMAGE),gotwamp:$(VERSION)) .

.PHONY: staging-up
staging-up: ## Start the docker compose staging (sender + 3 reflectors; PROFILES="observability perfsonar" adds those)
	$(STAGING) build --pull
	$(STAGING) $(foreach p,$(PROFILES),--profile $(p)) up -d --wait

.PHONY: staging-down
staging-down: ## Stop the staging and remove its containers, network and volumes
	$(STAGING) --profile observability --profile perfsonar down -v --remove-orphans

.PHONY: staging-status
staging-status: ## Show the staging containers and the sender's gotwampd status and sessions
	$(STAGING) ps
	$(STAGING) exec -T sender gotwamp status
	$(STAGING) exec -T sender gotwamp session list

.PHONY: staging-ping
staging-ping: ## Ping every staging reflector from the sender: Full and Light, IPv4 and IPv6, encrypted, STAMP
	$(STAGING_PING) reflector1 -4
	$(STAGING_PING) reflector1 -6 --light
	$(STAGING_PING) reflector2 -4 --security encrypted --key-id staging
	$(STAGING_PING) reflector2 -6 --light
	$(STAGING_PING) reflector3 -6 --isc --symmetrical --reflect-octets 0xbeef --padding-to-reflect 16 -s 64
	$(STAGING_PING) reflector3 -4 --light --stamp --ssid 7

.PHONY: staging-netem
staging-netem: ## Impair a staging container: make staging-netem ARGS="reflector2 delay 10ms 2ms" (see deploy/staging/netem.sh)
	deploy/staging/netem.sh $(or $(ARGS),all show)

.PHONY: staging-scenarios
staging-scenarios: ## Run the netem scenarios against the running staging (needs jq)
	deploy/staging/scenarios/all.sh

.PHONY: release-snapshot
release-snapshot: ## Build the release artifacts into dist/ with goreleaser, without publishing (skipped without goreleaser)
	@if command -v goreleaser >/dev/null 2>&1; then \
		goreleaser release --snapshot --clean; \
	else \
		echo "goreleaser not found: skipping release-snapshot (https://goreleaser.com/install/)"; \
	fi

.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -rf $(BIN_DIR) dist coverage.out coverage.html

.PHONY: tools
tools: ## Install the development tools into GOBIN
	$(GO) install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	$(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	$(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
	$(GO) install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	# declscope v0.14.0 declares go 1.27.0, so building it needs a Go 1.27
	# toolchain; let go fetch one for this install only (go.mod is unaffected).
	GOTOOLCHAIN=auto $(GO) install github.com/mpyw/declscope/cmd/declscope@$(DECLSCOPE_VERSION)
