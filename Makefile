.PHONY: check test lint build clean

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

# Clean build artifacts
clean:
	rm -rf bin/
