.DEFAULT_GOAL := help

DOC_DIR ?= docs/generated

VERSION    := $(shell git describe --tags 2>/dev/null || echo "0.1.0")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -ldflags "\
  -X github.com/txsvc/apikit.Version=$(VERSION) \
  -X github.com/txsvc/apikit.Commit=$(COMMIT) \
  -X github.com/txsvc/apikit.BuildTime=$(BUILD_TIME) \
  -X github.com/txsvc/apikit/internal/cli.Version=$(VERSION) \
  -X github.com/txsvc/apikit/internal/cli.Build=$(COMMIT) \
  -X github.com/txsvc/apikit.TokenPrefix=af \
  -X github.com/txsvc/apikit/internal/cli.TokenPrefix=af"

CONTAINER_REGISTRY ?= quay.io/agentfox

HUB_IMAGE ?= hub
HUB_IMAGE_TAG ?= $(VERSION)
HUB_PORT ?= 8080

SANDBOX_IMAGE ?= sandbox
SANDBOX_IMAGE_TAG ?= $(VERSION)

AGENTS_IMAGE ?= agents
AGENTS_IMAGE_TAG ?= $(VERSION)

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*##"}; {printf "  %-16s %s\n", $$1, $$2}'

# afc has no cgo dependencies and stays a static binary; hub links go-duckdb
# (internal/audit) and requires cgo.
.PHONY: build
build: ## Install afc and build bin/hub
	CGO_ENABLED=0 go install $(LDFLAGS) ./cmd/afc
	CGO_ENABLED=1 go build $(LDFLAGS) -o bin/hub ./cmd/af-hub

.PHONY: test
test: ## Run all Go tests
	go test ./... -count=1

.PHONY: coverage
coverage: ## Run all tests with a coverage report
	go test ./... -coverprofile=coverage.txt -covermode=atomic
	go tool cover -func=coverage.txt

.PHONY: fmt
fmt: ## gofmt all source files in place
	gofmt -l -w .

.PHONY: vet
vet: ## go vet ./...
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint if installed, otherwise skip
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed; skipping (see https://golangci-lint.run)"; \
	fi

.PHONY: check
check: fmt vet lint test ## Run fmt, vet, lint and test - use before committing

.PHONY: doc
doc: ## Generate Go package reference (go doc -all) as text files under docs/generated
	@rm -rf $(DOC_DIR)
	@mkdir -p $(DOC_DIR)
	@mod=$$(go list -m); \
	for p in $$(go list -f '{{if .GoFiles}}{{.ImportPath}}{{end}}' ./... | grep -v -e /internal/ -e /examples/); do \
		rel=$${p#$$mod}; rel=$${rel#/}; out="$(CURDIR)/$(DOC_DIR)/$${rel:-$$(basename $$mod)}.txt"; \
		mkdir -p "$$(dirname "$$out")" && go doc -all "$$p" > "$$out" || exit 1; \
	done
	@echo "package docs written to $(DOC_DIR)"

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy

.PHONY: clean
clean: ## Remove build output and the coverage profile
	rm -rf build coverage.txt bin/hub bin/af-hub bin/afc
	-rm -f af-hub afc

# ---------------------------------------------------------------------------
# Repository-specific targets
# ---------------------------------------------------------------------------

.PHONY: build-containers
build-containers: build-hub-container build-sandbox-container build-agents-container ## Build all container images

# Uses siblings ../apikit, ../agentkit-go and ../aflib via additional build
# contexts (go.mod replaces).
.PHONY: build-hub-container
build-hub-container: build ## Build the hub container locally
	podman build \
		--build-context apikit=../apikit \
		--build-context agentkit=../agentkit-go \
		--build-context aflib=../aflib \
		--build-arg VERSION=$(VERSION) \
		--build-arg BUILD=$(COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(CONTAINER_REGISTRY)/$(HUB_IMAGE):$(HUB_IMAGE_TAG) \
		-t $(CONTAINER_REGISTRY)/$(HUB_IMAGE):latest \
		-f containers/hub/Containerfile .

.PHONY: build-sandbox-container
build-sandbox-container: ## Build the sandbox container locally
	podman build \
		-t $(CONTAINER_REGISTRY)/$(SANDBOX_IMAGE):$(SANDBOX_IMAGE_TAG) \
		-t $(CONTAINER_REGISTRY)/$(SANDBOX_IMAGE):latest \
		-f containers/sandbox/Containerfile .

.PHONY: build-agents-container
build-agents-container: build-sandbox-container ## Build the agents container locally
	podman build \
		-t $(CONTAINER_REGISTRY)/$(AGENTS_IMAGE):$(AGENTS_IMAGE_TAG) \
		-t $(CONTAINER_REGISTRY)/$(AGENTS_IMAGE):latest \
		-f containers/agents/Containerfile .

.PHONY: hub-reset
hub-reset: ## Clear all data and config under bin/
	rm -rf bin/data bin/config
	mkdir -p bin/data bin/config
	cp bin/config.toml bin/config/config.toml
	XDG_CONFIG_HOME=$(CURDIR)/bin/config \
	XDG_DATA_HOME=$(CURDIR)/bin/data \
	./bin/hub --admin-email=hello@micku.me

.PHONY: hub-run
hub-run: ## Run bin/hub using the config/data created by hub-reset
	-mv bin/config/admin_token bin/config/token
	XDG_CONFIG_HOME=$(CURDIR)/bin/config \
	XDG_DATA_HOME=$(CURDIR)/bin/data \
	ADMIN_TOKEN=$$(cat bin/config/token) \
	./bin/hub

.PHONY: hub-runc
hub-runc: ## Run the hub container with bin/ mounted for config and data
	-mv bin/config/admin_token bin/config/token
	podman run --rm -it \
		-p $(HUB_PORT):8080 \
		-e ADMIN_TOKEN=$$(cat bin/config/token) \
		-v $(CURDIR)/bin/config:/config/af-hub \
		-v $(CURDIR)/bin/data:/data/af-hub \
		$(HUB_IMAGE):$(HUB_IMAGE_TAG)
