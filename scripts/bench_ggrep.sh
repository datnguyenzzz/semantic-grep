#!/bin/bash

# ==============================================================================
# 🚀 5-Way Multi-Query Literal and Regex Scaling Benchmark (JSON & Gnuplot)
# ==============================================================================
# This script executes five search engines recursively across a large codebase
# using 10 different Literal search queries and 10 different Regex search queries
# of varying matching sizes.
# It exports the results to a structured JSON file and generates beautiful,
# isolated scaling comparison charts for both categories using Gnuplot.
# ==============================================================================

set -e

TARGET_DIR="/Users/thanh.nguyen/Documents/dhse/opentelemetry-go"
WORKSPACE_DIR="/Users/thanh.nguyen/Documents/My_Code/agent-context"
GGREP_BIN="$WORKSPACE_DIR/dist/ggrep"
RESULTS_DIR="$WORKSPACE_DIR/results"

mkdir -p "$RESULTS_DIR"

echo "================================================================================"
echo "                   🛠  COMPILING GGREP & GRREP BINARIES  🛠"
echo "================================================================================"
make build

# Install/Build bep/grrep dynamically
if ! command -v grrep &> /dev/null; then
    echo "📥 Installing bep/grrep dynamically..."
    go install github.com/bep/grrep@latest
fi
GRREP_BIN=$(go env GOPATH)/bin/grrep
if [ -z "$GRREP_BIN" ] || [ ! -f "$GRREP_BIN" ]; then
    GRREP_BIN="$HOME/go/bin/grrep"
fi

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

if command -v rg &> /dev/null; then
    RG_BIN="rg"
else
    RG_BIN="dist/rg"
fi

# Define 50 highly complex, 100% diverse production-grade Go Literal Queries (guaranteed matches, no repetition)
QUERIES_LITERAL=(
    "github.com/stretchr/testify/assert"
    "zap.New"
    "go.opentelemetry.io/collector/component"
    "context.Background()"
    "fmt.Errorf"
    "ConsumeMetrics"
    "type Config struct"
    "Shutdown(ctx"
    "t.Helper()"
    "go.opentelemetry.io/collector/pdata"
    "go.opentelemetry.io/otel/trace"
    "github.com/spf13/cobra"
    "go.uber.org/multierr"
    "net/http"
    "component.ID"
    "t.Parallel()"
    "require.NoError(t,"
    "assert.Equal(t,"
    "zap.Error(err)"
    "logger.Info("
    "ctx context.Context"
    "func New"
    "var err error"
    "return err"
    "sync.Mutex"
    "sync.WaitGroup"
    "make(chan "
    "range "
    "defer f.Close()"
    "os.ReadFile("
    "time.Sleep("
    "yaml.Unmarshal("
    "json.Marshal("
    "http.NewRequest("
    "strings.HasPrefix("
    "bytes.Buffer"
    "strconv.Atoi("
    "io.EOF"
    "filepath.Join("
    "url.Parse("
    "regexp.MustCompile("
    "math.Max"
    "sort.Slice"
    "runtime.NumCPU()"
    "atomic.AddInt64"
    "reflect.DeepEqual("
    "testing.TB"
    "select {"
    "panic("
    "make(map[string]"
)

# Define 50 highly complex, 100% diverse POSIX ERE & Go compatible Regex Queries (guaranteed matches, no repetition, no multi-line \n)
QUERIES_REGEX=(
    "func \\([a-zA-Z0-9_]+ \\*[a-zA-Z0-9_]+\\) Start\\("
    "^[ \t]*type[ \t]+[a-zA-Z0-9_]+[ \t]+(struct|interface)"
    "(TODO|FIXME|BUG|HACK)"
    "[0-9]+\\.[0-9]+\\.[0-9]+"
    "go\\.opentelemetry\\.io/collector/(component|consumer|processor)"
    "errors\\.New\\(\"[a-zA-Z]"
    "err[ \t]*:=[ \t]*[a-zA-Z0-9_]+\\([a-zA-Z0-9_]+, [a-zA-Z0-9_]+\\)"
    "github\\.com/[a-zA-Z0-9_-]+/[a-zA-Z0-9_-]+"
    "https?://[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,4}/[a-zA-Z0-9./_-]+"
    "(http|grpc)://[a-zA-Z0-9.-]+"
    "func Test[A-Z][a-zA-Z0-9_]*\\("
    "func \\([a-zA-Z0-9_]+ \\*[a-zA-Z0-9_]+\\) Shutdown\\("
    "go\\.opentelemetry\\.io/collector/(receiver|exporter|extension)"
    "for [a-zA-Z0-9_]+, [a-zA-Z0-9_]+ := range"
    "if err != nil \\{[ \t]*return "
    "^[ \t]*log(ger)?\\.(Info|Warn|Error|Debug)\\("
    "const [a-zA-Z0-9_]+ = "
    "yaml:\"[a-zA-Z0-9_]+\""
    "github\\.com/stretchr/testify/require"
    "(tcp|udp|unix)://"
    "func \\([a-zA-Z0-9_]+ \\*[a-zA-Z0-9_]+\\) ConsumeMetrics\\("
    "^[ \t]*func[ \t]+New[A-Z][a-zA-Z0-9_]*\\("
    "go\\.opentelemetry\\.io/collector/(pdata|pmetric|plog|ptrace)"
    "^[ \t]*t\\.Run\\(\"[a-zA-Z0-9_]"
    "assert\\.(NoError|Nil|NotNil|Equal)\\(t, "
    "^[ \t]*if[ \t]+err[ \t]*:=[ \t]*[a-zA-Z0-9_]+\\([a-zA-Z0-9_ ]*\\);[ \t]*err[ \t]*!=[ \t]*nil"
    "^[ \t]*return[ \t]+nil,[ \t]*err"
    "^[ \t]*var[ \t]+_[ \t]+component\\.(Processor|Exporter|Receiver)[ \t]+=[ \t]+\\(\\*[a-zA-Z]"
    "zap\\.(String|Int|Bool|Duration|Any|Error)\\(\"[a-zA-Z]"
    "fmt\\.Errorf\\(\"[a-zA-Z0-9_ ]+:[ \t]*%w\""
    "^[ \t]*func[ \t]+(init|main)\\("
    "^[ \t]*package[ \t]+[a-zA-Z0-9_]+_test"
    "^[ \t]*defer[ \t]+[a-zA-Z0-9_]+\\.Close\\("
    "^[ \t]*if[ \t]+![a-zA-Z0-9_]+\\.[a-zA-Z0-9_]+\\("
    "yaml|json|mapstructure:\"[a-zA-Z0-9_]+\""
    "ctx[ \t]*,[ \t]*cancel[ \t]*:=[ \t]*context\\.With(Timeout|Cancel)\\("
    "^[ \t]*t\\.Parallel\\(\\)"
    "make\\(map\\[[a-zA-Z0-9_]+\\][a-zA-Z0-9_]+\\)"
    "make\\(chan[ \t]+[a-zA-Z0-9_]+[ \t]*,[ \t]*[0-9]+\\)"
    "^[ \t]*switch[ \t]+[a-zA-Z0-9_]+[ \t]*:=[ \t]*[a-zA-Z0-9_]+\\.[a-zA-Z0-9_]+\\("
    "if[ \t]+errors\\.Is\\(err[ \t]*,[ \t]*[a-zA-Z0-9_\\.]+\\)"
    "append\\([a-zA-Z0-9_]+[ \t]*,[ \t]*[a-zA-Z0-9_]+\\)"
    "^[ \t]*for[ \t]+[a-zA-Z0-9_]+[ \t]*:=[ \t]*0[ \t]*;[ \t]*[a-zA-Z0-9_]+[ \t]*<[ \t]*len"
    "strings\\.(Split|Join|Replace|Contains)\\("
    "^[ \t]*time\\.(Sleep|After|Tick)\\("
    "sync\\.(Mutex|RWMutex|Pool)"
    "json\\.New(Encoder|Decoder)\\("
    "filepath\\.(Base|Dir|Ext|Join)\\("
    "atomic\\.Add(Int64|Uint32)\\("
    "reflect\\.(TypeOf|ValueOf|DeepEqual)\\("
)

# Enforce bash time to output just the real seconds with 3 decimal places
export TIMEFORMAT='%R'

# Define temporary file variables
GGREP_LIT_DAT="$RESULTS_DIR/ggrep_bench_literal_ggrep.dat"
GRREP_LIT_DAT="$RESULTS_DIR/ggrep_bench_literal_grrep.dat"
RIP_LIT_DAT="$RESULTS_DIR/ggrep_bench_literal_ripgrep.dat"
GIT_LIT_DAT="$RESULTS_DIR/ggrep_bench_literal_gitgrep.dat"
OS_LIT_DAT="$RESULTS_DIR/ggrep_bench_literal_osgrep.dat"

GGREP_RX_DAT="$RESULTS_DIR/ggrep_bench_regex_ggrep.dat"
GRREP_RX_DAT="$RESULTS_DIR/ggrep_bench_regex_grrep.dat"
RIP_RX_DAT="$RESULTS_DIR/ggrep_bench_regex_ripgrep.dat"
GIT_RX_DAT="$RESULTS_DIR/ggrep_bench_regex_gitgrep.dat"
OS_RX_DAT="$RESULTS_DIR/ggrep_bench_regex_osgrep.dat"

# Clear old results files
rm -f "$GGREP_LIT_DAT" "$GRREP_LIT_DAT" "$RIP_LIT_DAT" "$GIT_LIT_DAT" "$OS_LIT_DAT" "$GGREP_RX_DAT" "$GRREP_RX_DAT" "$RIP_RX_DAT" "$GIT_RX_DAT" "$OS_RX_DAT"

# Helper function to execute and return (matches, latency_ms)
function measure_engine() {
    local name="$1"
    shift
    local cmd=("$@")
    
    # 1 Warmup run
    "${cmd[@]}" > /dev/null 2>&1 || true
    
    # Measure execution time
    local t=$( { time "${cmd[@]}" > /dev/null 2>&1 || true; } 2>&1 )
    local ms=$(awk "BEGIN { printf \"%.2f\", $t * 1000 }")
    
    # Calculate matches count
    local matches=$( "${cmd[@]}" | wc -l | tr -d ' ' ) || true
    if [ -z "$matches" ]; then matches=0; fi

    echo "$matches|$ms"
}

# Initialize JSON array
JSON_OUTPUT="{\n"

# ==============================================================================
# 🔍 1. LITERAL SEARCH EVALUATIONS
# ==============================================================================
echo "================================================================================"
echo "          🔍  1. EXECUTING 5-WAY MULTI-QUERY LITERAL EVALUATIONS  🔍"
echo "================================================================================"

JSON_OUTPUT="${JSON_OUTPUT}  \"literal_queries\": [\n"
LIT_COUNT=${#QUERIES_LITERAL[@]}

for idx in "${!QUERIES_LITERAL[@]}"; do
    Q="${QUERIES_LITERAL[$idx]}"
    ORDER_INDEX=$(($idx + 1))
    echo "👉 [$ORDER_INDEX/$LIT_COUNT] Processing Literal: \"$Q\""

    # 1. Native OS grep
    RES_OS=$(measure_engine "OS Grep" grep -r -I "$Q" "$TARGET_DIR")
    MATCH_OS=$(echo "$RES_OS" | cut -d'|' -f1)
    MS_OS=$(echo "$RES_OS" | cut -d'|' -f2)
    echo "$ORDER_INDEX $MS_OS" >> "$OS_LIT_DAT"

    # 2. bep/grrep
    RES_GR=$(measure_engine "grrep" "$GRREP_BIN" -F "$Q" "$TARGET_DIR")
    MATCH_GR=$(echo "$RES_GR" | cut -d'|' -f1)
    MS_GR=$(echo "$RES_GR" | cut -d'|' -f2)
    echo "$ORDER_INDEX $MS_GR" >> "$GRREP_LIT_DAT"

    # 3. burntsushi/ripgrep
    RES_RG=$(measure_engine "ripgrep" "$RG_BIN" -F "$Q" "$TARGET_DIR")
    MATCH_RG=$(echo "$RES_RG" | cut -d'|' -f1)
    MS_RG=$(echo "$RES_RG" | cut -d'|' -f2)
    echo "$ORDER_INDEX $MS_RG" >> "$RIP_LIT_DAT"

    # 4. git-grep
    RES_GIT=$(measure_engine "git-grep" git -C "$TARGET_DIR" grep -n -I -F "$Q")
    MATCH_GIT=$(echo "$RES_GIT" | cut -d'|' -f1)
    MS_GIT=$(echo "$RES_GIT" | cut -d'|' -f2)
    echo "$ORDER_INDEX $MS_GIT" >> "$GIT_LIT_DAT"

    # 5. Our ggrep
    RES_GG=$(measure_engine "ggrep" "$GGREP_BIN" "$Q" "$TARGET_DIR")
    MATCH_GG=$(echo "$RES_GG" | cut -d'|' -f1)
    MS_GG=$(echo "$RES_GG" | cut -d'|' -f2)
    echo "$ORDER_INDEX $MS_GG" >> "$GGREP_LIT_DAT"

    # Append to JSON string
    JSON_OUTPUT="${JSON_OUTPUT}    {\n"
    JSON_OUTPUT="${JSON_OUTPUT}      \"query\": \"$Q\",\n"
    JSON_OUTPUT="${JSON_OUTPUT}      \"order_index\": $ORDER_INDEX,\n"
    JSON_OUTPUT="${JSON_OUTPUT}      \"engines\": [\n"
    JSON_OUTPUT="${JSON_OUTPUT}        { \"name\": \"Our ggrep\", \"total_matched_lines\": $MATCH_GG, \"time_spent_ms\": $MS_GG },\n"
    JSON_OUTPUT="${JSON_OUTPUT}        { \"name\": \"bep/grrep\", \"total_matched_lines\": $MATCH_GR, \"time_spent_ms\": $MS_GR },\n"
    JSON_OUTPUT="${JSON_OUTPUT}        { \"name\": \"burntsushi/rg\", \"total_matched_lines\": $MATCH_RG, \"time_spent_ms\": $MS_RG },\n"
    JSON_OUTPUT="${JSON_OUTPUT}        { \"name\": \"git-grep\", \"total_matched_lines\": $MATCH_GIT, \"time_spent_ms\": $MS_GIT },\n"
    JSON_OUTPUT="${JSON_OUTPUT}        { \"name\": \"Native OS grep\", \"total_matched_lines\": $MATCH_OS, \"time_spent_ms\": $MS_OS }\n"
    JSON_OUTPUT="${JSON_OUTPUT}      ]\n"
    
    if [ $ORDER_INDEX -eq $LIT_COUNT ]; then
        JSON_OUTPUT="${JSON_OUTPUT}    }\n"
    else
        JSON_OUTPUT="${JSON_OUTPUT}    },\n"
    fi
done

JSON_OUTPUT="${JSON_OUTPUT}  ],\n"

# ==============================================================================
# 🔍 2. REGEX SEARCH EVALUATIONS
# ==============================================================================
echo ""
echo "================================================================================"
echo "          🔍  2. EXECUTING 5-WAY MULTI-QUERY REGEX EVALUATIONS  🔍"
echo "================================================================================"

JSON_OUTPUT="${JSON_OUTPUT}  \"regex_queries\": [\n"
RX_COUNT=${#QUERIES_REGEX[@]}

for idx in "${!QUERIES_REGEX[@]}"; do
    Q="${QUERIES_REGEX[$idx]}"
    ORDER_INDEX=$(($idx + 1))
    echo "👉 [$ORDER_INDEX/$RX_COUNT] Processing Regex: \"$Q\""

    # 1. Native OS grep
    RES_OS=$(measure_engine "OS Grep" grep -r -E -I "$Q" "$TARGET_DIR")
    MATCH_OS=$(echo "$RES_OS" | cut -d'|' -f1)
    MS_OS=$(echo "$RES_OS" | cut -d'|' -f2)
    echo "$ORDER_INDEX $MS_OS" >> "$OS_RX_DAT"

    # 2. bep/grrep
    RES_GR=$(measure_engine "grrep" "$GRREP_BIN" "$Q" "$TARGET_DIR")
    MATCH_GR=$(echo "$RES_GR" | cut -d'|' -f1)
    MS_GR=$(echo "$RES_GR" | cut -d'|' -f2)
    echo "$ORDER_INDEX $MS_GR" >> "$GRREP_RX_DAT"

    # 3. burntsushi/ripgrep
    RES_RG=$(measure_engine "ripgrep" "$RG_BIN" "$Q" "$TARGET_DIR")
    MATCH_RG=$(echo "$RES_RG" | cut -d'|' -f1)
    MS_RG=$(echo "$RES_RG" | cut -d'|' -f2)
    echo "$ORDER_INDEX $MS_RG" >> "$RIP_RX_DAT"

    # 4. git-grep
    RES_GIT=$(measure_engine "git-grep" git -C "$TARGET_DIR" grep -n -I -E "$Q")
    MATCH_GIT=$(echo "$RES_GIT" | cut -d'|' -f1)
    MS_GIT=$(echo "$RES_GIT" | cut -d'|' -f2)
    echo "$ORDER_INDEX $MS_GIT" >> "$GIT_RX_DAT"

    # 5. Our ggrep
    RES_GG=$(measure_engine "ggrep" "$GGREP_BIN" -r "$Q" "$TARGET_DIR")
    MATCH_GG=$(echo "$RES_GG" | cut -d'|' -f1)
    MS_GG=$(echo "$RES_GG" | cut -d'|' -f2)
    echo "$ORDER_INDEX $MS_GG" >> "$GGREP_RX_DAT"

    # Append to JSON string
    JSON_OUTPUT="${JSON_OUTPUT}    {\n"
    JSON_OUTPUT="${JSON_OUTPUT}      \"query\": \"$Q\",\n"
    JSON_OUTPUT="${JSON_OUTPUT}      \"order_index\": $ORDER_INDEX,\n"
    JSON_OUTPUT="${JSON_OUTPUT}      \"engines\": [\n"
    JSON_OUTPUT="${JSON_OUTPUT}        { \"name\": \"Our ggrep\", \"total_matched_lines\": $MATCH_GG, \"time_spent_ms\": $MS_GG },\n"
    JSON_OUTPUT="${JSON_OUTPUT}        { \"name\": \"bep/grrep\", \"total_matched_lines\": $MATCH_GR, \"time_spent_ms\": $MS_GR },\n"
    JSON_OUTPUT="${JSON_OUTPUT}        { \"name\": \"burntsushi/rg\", \"total_matched_lines\": $MATCH_RG, \"time_spent_ms\": $MS_RG },\n"
    JSON_OUTPUT="${JSON_OUTPUT}        { \"name\": \"git-grep\", \"total_matched_lines\": $MATCH_GIT, \"time_spent_ms\": $MS_GIT },\n"
    JSON_OUTPUT="${JSON_OUTPUT}        { \"name\": \"Native OS grep\", \"total_matched_lines\": $MATCH_OS, \"time_spent_ms\": $MS_OS }\n"
    JSON_OUTPUT="${JSON_OUTPUT}      ]\n"
    
    if [ $ORDER_INDEX -eq $RX_COUNT ]; then
        JSON_OUTPUT="${JSON_OUTPUT}    }\n"
    else
        JSON_OUTPUT="${JSON_OUTPUT}    },\n"
    fi
done

JSON_OUTPUT="${JSON_OUTPUT}  ]\n"
JSON_OUTPUT="${JSON_OUTPUT}}"

# Write the final consolidated JSON results file
echo -e "$JSON_OUTPUT" > "$RESULTS_DIR/ggrep_bench.json"
echo "✓ Saved comprehensive JSON metrics to: $RESULTS_DIR/ggrep_bench.json"

# ==============================================================================
# 📊 GENERATE GNUPLOT PNG CHARTS
# ==============================================================================
echo "📈 Rendering scaling comparison charts via Gnuplot..."
gnuplot scripts/plot_ggrep.gp
gnuplot scripts/plot_ggrep_regex.gp

echo "================================================================================"
echo "          🎉  SCALING PERFORMANCE COMPARISON COMPLETED SUCCESSFULLY!  🎉"
echo "================================================================================"
echo "📁 Saved Literal Scaling Chart: $RESULTS_DIR/ggrep_bench_literal_chart.png"
echo "📁 Saved Regex Scaling Chart  : $RESULTS_DIR/ggrep_bench_regex_chart.png"
echo "📁 Saved JSON metrics         : $RESULTS_DIR/ggrep_bench.json"
echo "================================================================================"
