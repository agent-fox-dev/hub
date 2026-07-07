.PHONY: build test lint check web-dev web-build

build:
	go build -o bin/af-hub ./cmd/af-hub
	go build -o bin/afc ./cmd/afc

test:
	go test ./... -count=1

lint:
	go vet ./...

check: lint test

web-dev:
	@if [ ! -d web/node_modules ]; then npm install --prefix web; fi
	npm run dev --prefix web

web-build:
	@if [ ! -d web/node_modules ]; then npm install --prefix web; fi
	npm run build --prefix web
