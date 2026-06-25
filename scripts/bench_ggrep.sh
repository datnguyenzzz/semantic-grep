#!/bin/bash

# ==============================================================================
# 🚀 Decoupled 5-Way Multi-Query Scaling Benchmark Runner
# ==============================================================================
# This script compiles the latest ggrep binaries, and then triggers our 
# robust, type-safe Go-based orchestrator (scripts/bench_orchestrator.go).
# All performance calibration, cache priming, metrics aggregation, JSON 
# marshalling, and Gnuplot rendering are orchestrated elegantly in Go.
# ==============================================================================

set -e

WORKSPACE_DIR="/Users/thanh.nguyen/Documents/My_Code/agent-context"

echo "================================================================================"
echo "                   🛠  COMPILING GGREP DUAL-ENGINES BINARIES  🛠"
echo "================================================================================"
make build
echo "✓ ggrep binaries successfully compiled!"
echo ""

# Install/Download ripgrep dynamically
if ! command -v rg &> /dev/null && [ ! -f "dist/rg" ]; then
    echo "📥 Downloading ripgrep pre-compiled binary dynamically..."
    ARCH=$(uname -m)
    if [ "$ARCH" = "arm64" ] || [ "$ARCH" = "aarch64" ]; then
        RG_URL="https://github.com/BurntSushi/ripgrep/releases/download/14.1.0/ripgrep-14.1.0-aarch64-apple-darwin.tar.gz"
    else
        RG_URL="https://github.com/BurntSushi/ripgrep/releases/download/14.1.0/ripgrep-14.1.0-x86_64-apple-darwin.tar.gz"
    fi
    curl -L "$RG_URL" -o dist/ripgrep.tar.gz
    tar -xzf dist/ripgrep.tar.gz -C dist --strip-components=1 2>/dev/null || true
    find dist -type f -name "rg" -exec mv {} dist/rg \; 2>/dev/null || true
    rm -f dist/ripgrep.tar.gz
fi

# Execute the Go-based Orchestrator!
CGO_ENABLED=1 go run "$WORKSPACE_DIR/scripts/bench_orchestrator.go"
