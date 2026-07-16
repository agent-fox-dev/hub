.PHONY: check test lint build build-container run-container web-dev web-build web-lint

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

# Web UI targets
web-dev:
	cd web && npm run dev

web-build:
	cd web && npm run build

web-lint:
	cd web && npm run lint
