GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
BUILD_ID ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
EXE :=
ifeq ($(GOOS),windows)
EXE := .exe
endif
BIN := bin/$(GOOS)/$(GOARCH)/workbench$(EXE)
TRAY_BIN := bin/$(GOOS)/$(GOARCH)/workbench-tray$(EXE)

.PHONY: all
all: frontend build

.PHONY: frontend
frontend:
	command -v pnpm >/dev/null 2>&1 || corepack enable
	cd frontend && pnpm install --frozen-lockfile && pnpm run build

.PHONY: build
build: build-go-local

.PHONY: build-go-local
build-go-local:
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "-s -w -X main.BuildID=$(BUILD_ID)" -o $(BIN) ./cmd/server
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "-s -w -X main.BuildID=$(BUILD_ID)" -o $(TRAY_BIN) ./cmd/tray

.PHONY: frontend-test
frontend-test:
	command -v pnpm >/dev/null 2>&1 || corepack enable
	cd frontend && pnpm install --frozen-lockfile && pnpm run test

.PHONY: test
test: frontend-test
	go test ./...

.PHONY: clean
clean:
	rm -rf bin internal/web/dist/* frontend/node_modules
