# Makefile for Gemini CLI Persistent Memory Extension (agent-mem)
# ponytail: keep targets clean, simple, and utilize standard shell commands

.PHONY: all build rebuild install test clean self-check

all: build

# [1] Rebuild all components
build: clean
	@echo "Creating dist directory..."
	mkdir -p dist
	@echo "Compiling mcp server..."
	CGO_ENABLED=1 go build -o dist/server cmd/server/main.go
	@echo "Compiling session-start hook..."
	CGO_ENABLED=1 go build -o dist/session-start cmd/session-start/main.go
	@echo "Compiling session-end hook..."
	CGO_ENABLED=1 go build -o dist/session-end cmd/session-end/main.go
	@echo "Compiling codebase indexer..."
	CGO_ENABLED=1 go build -o dist/indexer cmd/indexer/main.go
	@echo "Compilation completed successfully!"

rebuild: build

# [2] Uninstall existing link, clean directories, and install/link the new extension non-interactively
install: build
	@echo "Uninstalling existing agent-mem extension if any..."
	-gemini extensions uninstall agent-mem 2>/dev/null || true
	-rm -rf ~/.gemini/extensions/agent-mem 2>/dev/null || true
	@echo "Installing and linking the compiled Go-based agent-mem extension..."
	gemini extensions link . --consent
	@echo "Extension 'agent-mem' linked and installed successfully!"

# Run tests and self-checks
test:
	@echo "Running package unit tests..."
	CGO_ENABLED=1 go test ./... -v

test-integration:
	@echo "Running end-to-end integration tests..."
	CGO_ENABLED=1 go test -tags=integration -v

test-all: test test-integration self-check

self-check:
	@echo "Running local database self-check..."
	CGO_ENABLED=1 go run self-check.go

# Clean compiled binaries
clean:
	@echo "Cleaning up dist/ directory..."
	rm -rf dist/
