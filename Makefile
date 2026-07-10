.PHONY: check test lint build clean web-dev web-build web-lint

VERSION ?= 0.1.0

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
