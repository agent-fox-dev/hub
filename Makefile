.PHONY: check test lint build build-container run-container clean web-dev web-build web-lint

VERSION ?= 0.1.0

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
	go build -ldflags "-X main.version=$(VERSION)" -o bin/afc ./cmd/afc
	go build -ldflags "-X main.version=$(VERSION)" -o bin/af-hub ./cmd/af-hub

# Build the af-hub container locally (same context/file as CI)
build-container:
	podman build \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(IMAGE_TAG) \
		-f $(CONTAINERFILE) .

# Run the af-hub container with bin/ mounted for config and data
run-container:
	mkdir -p bin/data bin/config/af-hub
	podman run --rm -it \
		-p $(PORT):8080 \
		-v $(CURDIR)/bin/config:/config \
		-v $(CURDIR)/bin/data:/data \
		$(IMAGE):$(IMAGE_TAG)

# Clean build artifacts
clean:
	rm -rf bin/

# Web UI targets
web-dev:
	cd web && npm run dev

web-build:
	cd web && npm run build

web-lint:
	cd web && npm run lint
