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

# [3] Index a target codebase (Default: DIR=.)
# Usage: make index DIR=/path/to/repo
DIR ?= .
index:
	@if [ -f ./dist/indexer ]; then \
		read -p "Indexer binary found. Do you want to rebuild it first? [y/N]: " ans; \
		if [ "$$ans" = "y" ] || [ "$$ans" = "Y" ]; then \
			make build; \
		fi \
	else \
		make build; \
	fi
	@echo "================================================================================"
	@echo "Indexing target codebase: $(DIR)..."
	./dist/indexer $(DIR)

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
