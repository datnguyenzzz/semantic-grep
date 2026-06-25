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

# Execute the Go-based Orchestrator!
CGO_ENABLED=1 go run "$WORKSPACE_DIR/scripts/bench_orchestrator.go"
