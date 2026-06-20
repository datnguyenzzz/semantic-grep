# Makefile for Gemini CLI Persistent Memory Extension (agent-mem)
# ponytail: keep targets clean, simple, and utilize standard shell commands

.PHONY: all build rebuild install test clean self-check benchmark

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

# [2] Install options for Gemini and Claude Code CLIs
install: build
	@if [ -t 0 ]; then \
		read -p "Enter LITELLM_BASE_URL (Optional, press Enter to skip): " url; \
		read -p "Enter LITELLM_EMBEDDING_MODEL (Optional, press Enter to skip): " model; \
		if [ ! -z "$$url" ] || [ ! -z "$$model" ]; then \
			[ -z "$$url" ] && url="http://localhost:36253/v1" ; \
			[ -z "$$model" ] && model="gemini-embedding-001" ; \
			echo "LITELLM_BASE_URL=$$url" > $(HOME)/.agent-mem.env ; \
			echo "LITELLM_EMBEDDING_MODEL=$$model" >> $(HOME)/.agent-mem.env ; \
			echo "✓ Configurations saved successfully to $(HOME)/.agent-mem.env" ; \
		fi \
	fi
	@make install-gemini
	@make install-claude

install-gemini: build
	@echo "Uninstalling existing agent-context extension in Gemini CLI if any..."
	-gemini extensions uninstall agent-context 2>/dev/null || true
	-rm -rf ~/.gemini/extensions/agent-context 2>/dev/null || true
	@echo "Installing and linking the compiled Go-based extension to Gemini CLI..."
	-gemini extensions link . --consent || true
	@echo "Gemini CLI installation steps completed!"

install-claude: build
	@echo "Registering agent-mem MCP server to Claude Code CLI..."
	-claude mcp remove agent-mem 2>/dev/null || true
	-claude mcp add-json agent-mem '{"command":"$(shell pwd)/dist/server","args":[]}' --scope user || true
	@echo "Claude Code CLI installation steps completed!"

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
	go clean -testcache
	@echo "Running package unit tests..."
	CGO_ENABLED=1 go test ./... -v

test-integration:
	@echo "Running end-to-end integration tests..."
	CGO_ENABLED=1 go test -tags=integration -v

test-all: test test-integration self-check

self-check:
	@echo "Running local database self-check..."
	CGO_ENABLED=1 go run self-check.go

test-compression-rate:
	@echo "Running TurboQuant compression rate ..."
	CGO_ENABLED=1 go test ./scripts -tags=integration -timeout=6000s -run=Test_compression_rate -v

# Clean compiled binaries
clean:
	@echo "Cleaning up dist/ directory..."
	rm -rf dist/
