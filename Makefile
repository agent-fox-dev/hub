.PHONY: check test lint build build-container run-container web-dev web-build web-lint

VERSION := $(shell git describe --tags 2>/dev/null || echo "0.1.0")
BUILD   := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")

LDFLAGS := -ldflags "\
  -X github.com/txsvc/apikit.Version=$(VERSION) \
  -X github.com/txsvc/apikit.Build=$(BUILD) \
  -X github.com/txsvc/apikit/internal/cli.Version=$(VERSION) \
  -X github.com/txsvc/apikit/internal/cli.Build=$(BUILD) \
  -X github.com/txsvc/apikit.TokenPrefix=af \
  -X github.com/txsvc/apikit/internal/cli.TokenPrefix=af"

CONTAINER_NAME ?= hub
CONTAINERFILE := containers/$(CONTAINER_NAME)/Containerfile

IMAGE ?= af-hub
IMAGE_TAG ?= $(VERSION)
PORT ?= 8080

# Run lint + all tests
check: lint test

# Run all tests
test:
	go test ./... -count=1

# Run linter
lint:
	go vet ./...

# Build all packages
build:
	go build $(LDFLAGS) -o bin/afc ./cmd/afc
	go build $(LDFLAGS) -o bin/hub ./cmd/af-hub

# Build the af-hub container locally (same context/file as CI)
build-container:
	podman build \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(IMAGE_TAG) \
		-f $(CONTAINERFILE) .

# Clear all data and config
hub-reset:
	rm -rf bin/data bin/config
	mkdir -p bin/data/af-hub bin/config/af-hub
	podman run --rm -it \
		-p $(PORT):8080 \
		-v $(CURDIR)/bin/config:/config \
		-v $(CURDIR)/bin/data:/data \
		$(IMAGE):$(IMAGE_TAG)
	cp bin/config.toml bin/config/af-hub/config.toml

# Run the af-hub container with bin/ mounted for config and data
hub-run:
	podman run --rm -it \
		-p $(PORT):8080 \
		-e AF_HUB_ADMIN_TOKEN=$$(cat bin/config/af-hub/admin_token) \
		-v $(CURDIR)/bin/config:/config \
		-v $(CURDIR)/bin/data:/data \
		$(IMAGE):$(IMAGE_TAG)

# Clean build artifacts
clean:
	rm -rf bin/af-hub bin/afc
	podman rmi $(IMAGE):$(IMAGE_TAG)

run-reset:
	rm -rf bin/data
	mkdir -p bin/data
	cd bin && ./hub --admin-email=hello@micku.me

run:
	-mv bin/admin_token bin/token
	cd bin && ADMIN_TOKEN=$$(cat token) ./hub

# Start the Vite dev server with hot reload
web-dev:
	cd web && npm run dev

# Run a Vite production build
web-build:
	cd web && npm run build

# Run ESLint and TypeScript type checking
web-lint:
	cd web && npm run lint