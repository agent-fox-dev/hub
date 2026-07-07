.PHONY: build test lint check

build:
	go build -o bin/af-hub ./cmd/af-hub
	go build -o bin/afc ./cmd/afc

test:
	go test ./... -count=1

lint:
	go vet ./...

check: lint test
