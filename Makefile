# Makefile for chess server and client
#
# Portable between GNU make (4.0+, Linux) and bmake (FreeBSD's make): no
# $(shell), order-only prerequisites, or GNU conditionals. Shell assignments
# use `!=`, which both implementations support.

# Variables
BINARY_DIR := bin
SERVER_BINARY := $(BINARY_DIR)/chess-server
CLIENT_BINARY := $(BINARY_DIR)/chess-client-cli
SERVER_SOURCE := ./cmd/chess-server
CLIENT_SOURCE := ./cmd/chess-client-cli
# FreeBSD packages each Go release under its own name: go.mod's 1.27 is go127.
GO_DEFAULT != uname -s | grep -q FreeBSD && sed -n 's/^go \([0-9]*\)\.\([0-9]*\).*/go\1\2/p' go.mod 2>/dev/null | grep . || echo go
GO ?= $(GO_DEFAULT)
GOFLAGS := -trimpath
LDFLAGS := -s -w
# Both binaries are pure Go (pgx has no C dependency), so builds are static and
# cross-compile without a C toolchain.
CGO := CGO_ENABLED=0

# PostgreSQL connection for run/db targets. Keyword/value or URL form; the
# default uses the local Unix socket and peer authentication.
CHESS_DSN ?= dbname=chess

# WASM build variables
WASM_DIR := web/chess-client-wasm
WASM_BINARY := $(WASM_DIR)/chess-client.wasm
WASM_EXEC_JS := $(WASM_DIR)/wasm_exec.js
WASM_LIB_DIR := $(WASM_DIR)/lib

# xterm.js versions (5.5.0 compatible)
XTERM_VERSION := 5.5.0
XTERM_FIT_VERSION := 0.10.0
XTERM_WEBGL_VERSION := 0.18.0
XTERM_LINKS_VERSION := 0.11.0
XTERM_UNICODE_VERSION := 0.8.0
# curl on Linux; FreeBSD base ships fetch(1)
DOWNLOAD != if command -v curl >/dev/null 2>&1; then echo 'curl -sSfLO'; else echo 'fetch -q'; fi

# Default target: `make` prints help (.MAIN for bmake, .DEFAULT_GOAL for GNU make)
.MAIN: help
.DEFAULT_GOAL := help

.PHONY: all
all: build

# Build both binaries
.PHONY: build
build: server client

# Build server only. Phony: the Go build cache decides what is stale, so a
# changed source file is never masked by an existing binary.
.PHONY: server
server:
	@mkdir -p $(BINARY_DIR)
	$(CGO) $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(SERVER_BINARY) $(SERVER_SOURCE)
	@echo "Built server: $(SERVER_BINARY)"

# Build client only
.PHONY: client
client:
	@mkdir -p $(BINARY_DIR)
	$(CGO) $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(CLIENT_BINARY) $(CLIENT_SOURCE)
	@echo "Built client: $(CLIENT_BINARY)"

# Build WASM client — CGO incompatible with js/wasm target
.PHONY: wasm
wasm:
	@mkdir -p $(WASM_DIR)
	@echo "Building WASM client..."
	$(CGO) GOOS=js GOARCH=wasm $(GO) build $(GOFLAGS) \
		-ldflags "$(LDFLAGS)" \
		-o $(WASM_BINARY) $(CLIENT_SOURCE)
	@cp "$$($(GO) env GOROOT)/lib/wasm/wasm_exec.js" $(WASM_DIR)/
	@echo "Built WASM client: $(WASM_BINARY)"
	@echo "Size: $$(du -h $(WASM_BINARY) | cut -f1)"

# Download xterm.js and all addons
.PHONY: wasm-deps
wasm-deps:
	@echo "Downloading xterm.js $(XTERM_VERSION) and addons..."
	@mkdir -p $(WASM_LIB_DIR)
	@cd $(WASM_LIB_DIR) && \
		$(DOWNLOAD) https://cdn.jsdelivr.net/npm/@xterm/xterm@$(XTERM_VERSION)/lib/xterm.min.js && \
		$(DOWNLOAD) https://cdn.jsdelivr.net/npm/@xterm/xterm@$(XTERM_VERSION)/css/xterm.min.css && \
		$(DOWNLOAD) https://cdn.jsdelivr.net/npm/@xterm/addon-fit@$(XTERM_FIT_VERSION)/lib/addon-fit.min.js && \
		$(DOWNLOAD) https://cdn.jsdelivr.net/npm/@xterm/addon-webgl@$(XTERM_WEBGL_VERSION)/lib/addon-webgl.min.js && \
		$(DOWNLOAD) https://cdn.jsdelivr.net/npm/@xterm/addon-web-links@$(XTERM_LINKS_VERSION)/lib/addon-web-links.min.js && \
		$(DOWNLOAD) https://cdn.jsdelivr.net/npm/@xterm/addon-unicode11@$(XTERM_UNICODE_VERSION)/lib/addon-unicode11.min.js
	@echo "Downloaded to $(WASM_LIB_DIR)/"
	@ls -la $(WASM_LIB_DIR)/

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

# Run server with default settings
.PHONY: run-server
run-server: server
	CHESS_DSN='$(CHESS_DSN)' $(SERVER_BINARY) -api-port 8080 -dev

# Run server with web UI
.PHONY: run-server-web
run-server-web: server
	CHESS_DSN='$(CHESS_DSN)' $(SERVER_BINARY) -api-port 8080 -dev -serve -web-port 9090

# Run client
.PHONY: run-client
run-client: client
	$(CLIENT_BINARY)

# Go unit and PostgreSQL integration tests. Database tests are skipped unless
# CHESS_TEST_DSN names a disposable database (see test/README.md).
.PHONY: test
test:
	$(GO) vet ./...
	$(GO) test -race -count=1 ./...

# Start a test server for the shell suites (requires CHESS_TEST_DSN)
.PHONY: test-server
test-server: server
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
	CHESS_DSN='$(CHESS_DSN)' $(SERVER_BINARY) db init

.PHONY: db-clean
# ☣ DESTRUCTIVE: drops every chess table in the DSN's search_path
db-clean: server
	CHESS_DSN='$(CHESS_DSN)' $(SERVER_BINARY) db delete -confirm

# Cross-compile a static server for FreeBSD/amd64 (no C toolchain required)
.PHONY: server-freebsd
server-freebsd:
	@mkdir -p $(BINARY_DIR)
	$(CGO) GOOS=freebsd GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" \
		-o $(SERVER_BINARY)-freebsd-amd64 $(SERVER_SOURCE)
	@echo "Built FreeBSD server: $(SERVER_BINARY)-freebsd-amd64"

# Development build with the race detector (the race runtime requires cgo)
.PHONY: dev
dev:
	@mkdir -p $(BINARY_DIR)
	CGO_ENABLED=1 $(GO) build -race -o $(SERVER_BINARY) $(SERVER_SOURCE)
	CGO_ENABLED=1 $(GO) build -race -o $(CLIENT_BINARY) $(CLIENT_SOURCE)
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
	@echo "  make server-freebsd Cross-compile static FreeBSD/amd64 server"
	@echo "  make dev          Build with race detector"
	@echo ""
	@echo "Run targets:"
	@echo "  make run-server     Run server (port 8080, dev mode)"
	@echo "  make run-server-web Run server with web UI (ports 8080/9090)"
	@echo "  make run-client     Run client"
	@echo "  make wasm-serve     Serve WASM client (port 8081)"
	@echo ""
	@echo "Test targets:"
	@echo "  make test         Run vet and Go tests (set CHESS_TEST_DSN for DB tests)"
	@echo "  make test-server  Start the shell-suite test server (needs CHESS_TEST_DSN)"
	@echo "  make test-api     Run API tests"
	@echo "  make test-db      Run database tests"
	@echo "  make test-longpoll Run long-poll tests"
	@echo ""
	@echo "Database targets (CHESS_DSN=$(CHESS_DSN)):"
	@echo "  make db-init      Create or migrate the schema"
	@echo "  make db-clean     Drop all chess tables (destructive)"
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

