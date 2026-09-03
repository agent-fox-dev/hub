.PHONY: check test lint build build-containers build-hub-container build-sandbox-container build-agents-container web-dev web-build web-lint

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

# Run lint + all tests
check: lint test

# Run all tests
test:
	go test ./... -count=1

# Run linter
lint:
	go vet ./...

# Build all packages
# afc has no cgo dependencies and stays a static binary; hub links go-duckdb
# (internal/audit) and requires cgo.
build:
	CGO_ENABLED=0 go install $(LDFLAGS) ./cmd/afc
	CGO_ENABLED=1 go build $(LDFLAGS) -o bin/hub ./cmd/af-hub

build-containers: build-hub-container build-sandbox-container build-agents-container

# Build the hub container locally.
# Uses sibling ../apikit via additional build context (go.mod replace).
build-hub-container: build
	podman build \
		--build-context apikit=../apikit \
		--build-arg VERSION=$(VERSION) \
		--build-arg BUILD=$(COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(CONTAINER_REGISTRY)/$(HUB_IMAGE):$(HUB_IMAGE_TAG) \
		-f containers/hub/Containerfile .

# Build the sandbox container locally.
build-sandbox-container:
	podman build \
		-t $(CONTAINER_REGISTRY)/$(SANDBOX_IMAGE):$(SANDBOX_IMAGE_TAG) \
		-f containers/sandbox/Containerfile .

# Build the agents container locally.
build-agents-container: build-sandbox-container
	podman build \
		-t $(CONTAINER_REGISTRY)/$(AGENTS_IMAGE):$(AGENTS_IMAGE_TAG) \
		-f containers/agents/Containerfile .

# Clean build artifacts
clean:
	-rm -rf bin/af-hub bin/afc
	-rm af-hub afc
	-podman rmi $(HUB_IMAGE):$(HUB_IMAGE_TAG)

# Clear all data and config
hub-reset:
	rm -rf bin/data bin/config
	mkdir -p bin/data bin/config
	cp bin/config.toml bin/config/config.toml
	XDG_CONFIG_HOME=$(CURDIR)/bin/config \
	XDG_DATA_HOME=$(CURDIR)/bin/data \
	./bin/hub --admin-email=hello@micku.me

# Run bin/hub directly using config/data created by hub-reset
hub-run:
	-mv bin/config/admin_token bin/config/token
	XDG_CONFIG_HOME=$(CURDIR)/bin/config \
	XDG_DATA_HOME=$(CURDIR)/bin/data \
	ADMIN_TOKEN=$$(cat bin/config/token) \
	./bin/hub

# Run the af-hub container with bin/ mounted for config and data
hub-runc:
	-mv bin/config/admin_token bin/config/token
	podman run --rm -it \
		-p $(HUB_PORT):8080 \
		-e ADMIN_TOKEN=$$(cat bin/config/token) \
		-v $(CURDIR)/bin/config:/config \
		-v $(CURDIR)/bin/data:/data \
		$(HUB_IMAGE):$(HUB_IMAGE_TAG)

# Start the Vite dev server with hot reload
web-dev:
	cd web && npm run dev

# Run a Vite production build
web-build:
	cd web && npm run build

# Run ESLint and TypeScript type checking
web-lint:
	cd web && npm run lint