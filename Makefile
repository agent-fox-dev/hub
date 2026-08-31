.PHONY: check test lint build build-container run-container web-dev web-build web-lint

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
	cp bin/afc ${DEVEL}/tools/afc

<<<<<<< Updated upstream
# Build the af-hub container locally 
buildc: build
	-cd ../apikit && git push origin main
	-git push origin main
=======
# Build the af-hub container locally.
# Uses sibling ../apikit via additional build context (go.mod replace).
build-container: build
>>>>>>> Stashed changes
	podman build \
		--build-context apikit=../apikit \
		--build-arg VERSION=$(VERSION) \
		--build-arg BUILD=$(COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(IMAGE):$(IMAGE_TAG) \
		-f $(CONTAINERFILE) .

# Clean build artifacts
clean:
	rm -rf bin/af-hub bin/afc
	podman rmi $(IMAGE):$(IMAGE_TAG)

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
		-p $(PORT):8080 \
		-e ADMIN_TOKEN=$$(cat bin/config/token) \
		-v $(CURDIR)/bin/config:/config \
		-v $(CURDIR)/bin/data:/data \
		$(IMAGE):$(IMAGE_TAG)

# Start the Vite dev server with hot reload
web-dev:
	cd web && npm run dev

# Run a Vite production build
web-build:
	cd web && npm run build

# Run ESLint and TypeScript type checking
web-lint:
	cd web && npm run lint