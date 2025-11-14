# Makefile for chess server and client

# Variables
BINARY_DIR := bin
SERVER_BINARY := $(BINARY_DIR)/chess-server
CLIENT_BINARY := $(BINARY_DIR)/chess-client-cli
SERVER_SOURCE := ./cmd/chess-server
CLIENT_SOURCE := ./cmd/chess-client-cli
GO := go
GOROOT := $(shell go env GOROOT)
GOFLAGS := -trimpath
LDFLAGS := -s -w

# Build info
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
VERSION := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "dev")

# WASM build variables
WASM_DIR := web/chess-client-wasm
WASM_BINARY := $(WASM_DIR)/chess-client.wasm
WASM_EXEC_JS := $(WASM_DIR)/wasm_exec.js
WASM_EXEC_SRC := $(GOROOT)/lib/wasm/wasm_exec.js

# Default target
.PHONY: all
all: build

# Build both binaries
.PHONY: build
build: server client

# Build server only
.PHONY: server
server: $(SERVER_BINARY)

$(SERVER_BINARY): $(BINARY_DIR)
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(SERVER_BINARY) $(SERVER_SOURCE)
	@echo "Built server: $(SERVER_BINARY)"

# Build client only
.PHONY: client
client: $(CLIENT_BINARY)

$(CLIENT_BINARY): $(BINARY_DIR)
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(CLIENT_BINARY) $(CLIENT_SOURCE)
	@echo "Built client: $(CLIENT_BINARY)"

# Create bin directory
$(BINARY_DIR):
	@mkdir -p $(BINARY_DIR)

# Build WASM client
.PHONY: wasm
wasm: $(WASM_DIR)
	@echo "Building WASM client..."
	GOOS=js GOARCH=wasm $(GO) build $(GOFLAGS) \
		-ldflags "$(LDFLAGS)" \
		-o $(WASM_BINARY) $(CLIENT_SOURCE)
	@cp "$(WASM_EXEC_SRC)" $(WASM_DIR)/
	@echo "Built WASM client: $(WASM_BINARY)"
	@echo "Size: $$(du -h $(WASM_BINARY) | cut -f1)"

# Download xterm.js dependencies
.PHONY: wasm-deps
wasm-deps: $(WASM_DIR)
	@echo "Downloading xterm.js 5.5.0..."
	@mkdir -p $(WASM_DIR)/lib
	@cd $(WASM_DIR)/lib && \
		curl -sO https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/lib/xterm.min.js && \
		curl -sO https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/css/xterm.css
	@echo "xterm.js 5.5.0 downloaded to $(WASM_DIR)/lib/"

# Build WASM with dependencies
.PHONY: wasm-full
wasm-full: wasm-deps wasm

# Serve WASM client for testing
.PHONY: wasm-serve
wasm-serve: wasm
	@echo "Starting WASM server on http://localhost:8081"
	@echo "Open http://localhost:8081 in your browser"
	cd $(WASM_DIR) && python3 -m http.server 8081 --bind 127.0.0.1

# Clean WASM build
.PHONY: wasm-clean
wasm-clean:
	rm -f $(WASM_BINARY) $(WASM_EXEC_JS)
	rm -rf $(WASM_DIR)/lib

# Create WASM directory
$(WASM_DIR):
	@mkdir -p $(WASM_DIR)

# Run server with default settings
.PHONY: run-server
run-server: server
	$(SERVER_BINARY) -api-port 8080 -dev -storage-path db/chess.db

# Run server with web UI
.PHONY: run-server-web
run-server-web: server
	$(SERVER_BINARY) -api-port 8080 -dev -storage-path db/chess.db -serve -web-port 9090

# Run client
.PHONY: run-client
run-client: client
	$(CLIENT_BINARY)

# Run tests (start server and run test scripts)
.PHONY: test
test: server
	test/run-test-server.sh

# Run individual test suites
.PHONY: test-api
test-api:
	test/test-api.sh

.PHONY: test-db
test-db:
	test/test-db.sh

.PHONY: test-longpoll
test-longpoll:
	test/test-longpoll.sh

# Database operations
.PHONY: db-init
db-init: server
	$(SERVER_BINARY) db init -path db/chess.db

.PHONY: db-clean
db-clean:
	# ☣ DESTRUCTIVE: Removes database
	rm -f db/chess.db db/chess.db-*

# Development build (with race detector)
.PHONY: dev
dev:
	$(GO) build -race -o $(SERVER_BINARY) $(SERVER_SOURCE)
	$(GO) build -race -o $(CLIENT_BINARY) $(CLIENT_SOURCE)
	@echo "Built with race detector enabled"

# Clean build artifacts
.PHONY: clean
clean:
	rm -f $(SERVER_BINARY) $(CLIENT_BINARY)
	rm -rf $(BINARY_DIR)
	@echo "Cleaned build artifacts"

# Install dependencies
.PHONY: deps
deps:
	$(GO) mod download
	$(GO) mod verify

# Update dependencies
.PHONY: deps-update
deps-update:
	$(GO) get -u ./...
	$(GO) mod tidy

# Format code
.PHONY: fmt
fmt:
	$(GO) fmt ./...

# Run linter
.PHONY: lint
lint:
	golangci-lint run ./...

# Show help
.PHONY: help
help:
	@echo "Chess Build System"
	@echo ""
	@echo "Build targets:"
	@echo "  make build        Build both server and client"
	@echo "  make server       Build server only"
	@echo "  make client       Build client only"
	@echo "  make wasm         Build WASM client"
	@echo "  make wasm-full    Build WASM with dependencies"
	@echo "  make dev          Build with race detector"
	@echo ""
	@echo "Run targets:"
	@echo "  make run-server     Run server (port 8080, dev mode)"
	@echo "  make run-server-web Run server with web UI (ports 8080/9090)"
	@echo "  make run-client     Run client"
	@echo "  make wasm-serve     Serve WASM client (port 8081)"
	@echo ""
	@echo "Test targets:"
	@echo "  make test         Run all tests"
	@echo "  make test-api     Run API tests"
	@echo "  make test-db      Run database tests"
	@echo "  make test-longpoll Run long-poll tests"
	@echo ""
	@echo "Database targets:"
	@echo "  make db-init      Initialize database"
	@echo "  make db-clean     Remove database (destructive)"
	@echo ""
	@echo "WASM targets:"
	@echo "  make wasm-deps    Download xterm.js dependencies"
	@echo "  make wasm-clean   Clean WASM build files"
	@echo ""
	@echo "Maintenance:"
	@echo "  make clean        Remove build artifacts"
	@echo "  make deps         Download dependencies"
	@echo "  make deps-update  Update dependencies"
	@echo "  make fmt          Format code"
	@echo "  make lint         Run linter"

# Default make without arguments shows help
.DEFAULT_GOAL := help