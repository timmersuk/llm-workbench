GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
BUILD_ID ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BIN := bin/$(GOOS)/$(GOARCH)/workbench

.PHONY: all
all: frontend build

.PHONY: frontend
frontend:
	corepack enable
	cd frontend && pnpm install --frozen-lockfile && pnpm run build

.PHONY: build
build: build-go-local

.PHONY: build-go-local
build-go-local:
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "-s -w -X main.BuildID=$(BUILD_ID)" -o $(BIN) ./cmd/server

.PHONY: frontend-test
frontend-test:
	corepack enable
	cd frontend && pnpm install --frozen-lockfile && pnpm run test

.PHONY: test
test: frontend-test
	go test ./...

.PHONY: clean
clean:
	rm -rf bin internal/web/dist/* frontend/node_modules
