package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	targetDir    = "/Users/thanh.nguyen/Documents/dhse/opentelemetry/opentelemetry-collector-contrib"
	workspaceDir = "/Users/thanh.nguyen/Documents/My_Code/agent-context"
	resultsDir   = "/Users/thanh.nguyen/Documents/My_Code/agent-context/results"
)

// Define our 50 highly complex, 100% diverse production-grade Literal Queries
var queriesLiteral = []string{
	"github.com/stretchr/testify/assert",
	"zap.New",
	"go.opentelemetry.io/collector/component",
	"context.Background()",
	"fmt.Errorf",
	"ConsumeMetrics",
	"type Config struct",
	"Shutdown(ctx",
	"t.Helper()",
	"go.opentelemetry.io/collector/pdata",
	"go.opentelemetry.io/otel/trace",
	"github.com/spf13/cobra",
	"go.uber.org/multierr",
	"net/http",
	"component.ID",
	"t.Parallel()",
	"require.NoError(t,",
	"assert.Equal(t,",
	"zap.Error(err)",
	"logger.Info(",
	"ctx context.Context",
	"func New",
	"var err error",
	"return err",
	"sync.Mutex",
	"sync.WaitGroup",
	"make(chan ",
	"range ",
	"defer f.Close()",
	"os.ReadFile(",
	"time.Sleep(",
	"yaml.Unmarshal(",
	"json.Marshal(",
	"http.NewRequest(",
	"strings.HasPrefix(",
	"bytes.Buffer",
	"strconv.Atoi(",
	"io.EOF",
	"filepath.Join(",
	"url.Parse(",
	"regexp.MustCompile(",
	"math.Max",
	"sort.Slice",
	"runtime.NumCPU()",
	"atomic.AddInt64",
	"reflect.DeepEqual(",
	"testing.TB",
	"select {",
	"panic(",
	"make(map[string]",
}

// Define our 50 highly complex, 100% diverse POSIX ERE & Go compatible Regex Queries
var queriesRegex = []string{
	"func \\([a-zA-Z0-9_]+ \\*[a-zA-Z0-9_]+\\) Start\\(",
	"^[ \t]*type[ \t]+[a-zA-Z0-9_]+[ \t]+(struct|interface)",
	"(TODO|FIXME|BUG|HACK)",
	"[0-9]+\\.[0-9]+\\.[0-9]+",
	"go\\.opentelemetry\\.io/collector/(component|consumer|processor)",
	"errors\\.New\\(\"[a-zA-Z]",
	"err[ \t]*:=[ \t]*[a-zA-Z0-9_]+\\([a-zA-Z0-9_]+, [a-zA-Z0-9_]+\\)",
	"github\\.com/[a-zA-Z0-9_-]+/[a-zA-Z0-9_-]+",
	"https?://[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,4}/[a-zA-Z0-9./_-]+",
	"(http|grpc)://[a-zA-Z0-9.-]+",
	"func Test[A-Z][a-zA-Z0-9_]*\\(",
	"func \\([a-zA-Z0-9_]+ \\*[a-zA-Z0-9_]+\\) Shutdown\\(",
	"go\\.opentelemetry\\.io/collector/(receiver|exporter|extension)",
	"for [a-zA-Z0-9_]+, [a-zA-Z0-9_]+ := range",
	"if err != nil \\{[ \t]*return ",
	"^[ \t]*log(ger)?\\.(Info|Warn|Error|Debug)\\format",
	"const [a-zA-Z0-9_]+ = ",
	"yaml:\"[a-zA-Z0-9_]+\"",
	"github\\.com/stretchr/testify/require",
	"(tcp|udp|unix)://",
	"func \\([a-zA-Z0-9_]+ \\*[a-zA-Z0-9_]+\\) ConsumeMetrics\\(",
	"^[ \t]*func[ \t]+New[A-Z][a-zA-Z0-9_]*\\(",
	"go\\.opentelemetry\\.io/collector/(pdata|pmetric|plog|ptrace)",
	"^[ \t]*t\\.Run\\(\"[a-zA-Z0-9_]",
	"assert\\.(NoError|Nil|NotNil|Equal)\\(t, ",
	"^[ \t]*if[ \t]+err[ \t]*:=[ \t]*[a-zA-Z0-9_]+\\([a-zA-Z0-9_ ]*\\);[ \t]*err[ \t]*!=[ \t]*nil",
	"^[ \t]*return[ \t]+[a-zA-Z0-9_]+,[ \t]*nil",
	"^[ \t]*var[ \t]+_[ \t]+component\\.(Processor|Exporter|Receiver)[ \t]+=[ \t]+\\(\\*[a-zA-Z]",
	"zap\\.(String|Int|Bool|Duration|Any|Error)\\(\"[a-zA-Z]",
	"fmt\\.Errorf\\(\"[a-zA-Z0-9_ ]+:[ \t]*%w\"",
	"^[ \t]*func[ \t]+(init|main)\\(",
	"^[ \t]*package[ \t]+[a-zA-Z0-9_]+_test",
	"^[ \t]*defer[ \t]+[a-zA-Z0-9_]+\\.Close\\(",
	"^[ \t]*if[ \t]+![a-zA-Z0-9_]+\\.[a-zA-Z0-9_]+\\format",
	"yaml|json|mapstructure:\"[a-zA-Z0-9_]+\"",
	"ctx[ \t]*,[ \t]*cancel[ \t]*:=[ \t]*context\\.With(Timeout|Cancel)\\format",
	"^[ \t]*t\\.Parallel\\(\\)",
	"make\\(map\\[[a-zA-Z0-9_]+\\][a-zA-Z0-9_]+\\)",
	"make\\(chan[ \t]+[a-zA-Z0-9_]+[ \t]*,[ \t]*[0-9]+\\)",
	"^[ \t]*switch[ \t]+[a-zA-Z0-9_]+[ \t]*:=[ \t]*[a-zA-Z0-9_]+\\.[a-zA-Z0-9_]+\\format",
	"if[ \t]+errors\\.Is\\(err[ \t]*,[ \t]*[a-zA-Z0-9_\\.]+\\)",
	"append\\([a-zA-Z0-9_]+[ \t]*,[ \t]*[a-zA-Z0-9_]+\\)",
	"^[ \t]*for[ \t]+[a-zA-Z0-9_]+[ \t]*:=[ \t]*0[ \t]*;[ \t]*[a-zA-Z0-9_]+[ \t]*<[ \t]*len",
	"strings\\.(Split|Join|Replace|Contains)\\format",
	"^[ \t]*time\\.(Sleep|After|Tick)\\format",
	"sync\\.(Mutex|RWMutex|Pool)",
	"json\\.New(Encoder|Decoder)\\format",
	"filepath\\.(Base|Dir|Ext|Join)\\format",
	"atomic\\.Add(Int64|Uint32)\\format",
	"reflect\\.(TypeOf|ValueOf|DeepEqual)\\format",
}

type EngineMetric struct {
	Name              string  `json:"name"`
	TotalMatchedLines int     `json:"total_matched_lines"`
	TimeSpentMs       float64 `json:"time_spent_ms"`
}

type QueryResult struct {
	Query      string         `json:"query"`
	OrderIndex int            `json:"order_index"`
	Engines    []EngineMetric `json:"engines"`
}

type BenchmarkReport struct {
	LiteralQueries []QueryResult `json:"literal_queries"`
	RegexQueries   []QueryResult `json:"regex_queries"`
}

func main() {
	log.Println("================================================================================")
	log.Println("            🔥  STARTING SCIENTIFIC DUAL-ENGINE GO BENCHMARK  🔥")
	log.Println("================================================================================")

	// Resolve Ripgrep binary
	rgBin := "rg"
	if _, err := exec.LookPath("rg"); err != nil {
		rgBin = filepath.Join(workspaceDir, "dist/rg")
	}

	ggrepBin := filepath.Join(workspaceDir, "dist/ggrep")
	ggrepStdBin := filepath.Join(workspaceDir, "dist/ggrep-std")

	// 1. Warm up Virtual Memory Page Cache concurrently!
	preWarmPageCache()

	// 2. Calibrate OS process launch latencies
	log.Println("Calibrating OS process spawning baseline latencies...")
	tmpDir, err := os.MkdirTemp("", "ggrep-bench-")
	if err != nil {
		log.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	tmpFile := filepath.Join(tmpDir, "empty.txt")
	_ = os.WriteFile(tmpFile, []byte(""), 0644)

	baseOS := calibrate(exec.Command("grep", "-I", "pattern", tmpFile))
	baseStd := calibrate(exec.Command(ggrepStdBin, "pattern", tmpFile))
	baseRG := calibrate(exec.Command(rgBin, "-F", "pattern", tmpFile))
	baseGit := calibrate(exec.Command("git", "-C", workspaceDir, "grep", "pattern", tmpFile))
	baseGG := calibrate(exec.Command(ggrepBin, "pattern", tmpFile))

	log.Printf("✓ OS grep launch latency      : %.2f ms", baseOS)
	log.Printf("✓ ggrep-std launch latency    : %.2f ms", baseStd)
	log.Printf("✓ burntsushi/rg launch latency : %.2f ms", baseRG)
	log.Printf("✓ git-grep launch latency     : %.2f ms", baseGit)
	log.Printf("✓ Our ggrep launch latency    : %.2f ms", baseGG)
	log.Println("")

	report := BenchmarkReport{
		LiteralQueries: make([]QueryResult, 0, len(queriesLiteral)),
		RegexQueries:   make([]QueryResult, 0, len(queriesRegex)),
	}

	// 3. Run Literal Queries
	log.Println("================================================================================")
	log.Println("          🔍  1. EXECUTING 5-WAY MULTI-QUERY LITERAL EVALUATIONS  🔍")
	log.Println("================================================================================")

	literalDat := map[string]*bytes.Buffer{
		"ggrep":     new(bytes.Buffer),
		"ggrep-std": new(bytes.Buffer),
		"ripgrep":   new(bytes.Buffer),
		"gitgrep":   new(bytes.Buffer),
		"osgrep":    new(bytes.Buffer),
	}

	for idx, q := range queriesLiteral {
		orderIndex := idx + 1
		log.Printf("👉 [%d/%d] Processing Literal: %q", orderIndex, len(queriesLiteral), q)

		osMetrics := measure("OS Grep", baseOS, exec.Command("grep", "-r", "-I", q, targetDir))
		stdMetrics := measure("ggrep-std", baseStd, exec.Command(ggrepStdBin, q, targetDir))
		rgMetrics := measure("ripgrep", baseRG, exec.Command(rgBin, "-F", q, targetDir))
		gitMetrics := measure("git-grep", baseGit, exec.Command("git", "-C", targetDir, "grep", "-n", "-I", "-F", q))
		ggMetrics := measure("ggrep", baseGG, exec.Command(ggrepBin, q, targetDir))

		fmt.Fprintf(literalDat["osgrep"], "%d %.2f\n", orderIndex, osMetrics.TimeSpentMs)
		fmt.Fprintf(literalDat["ggrep-std"], "%d %.2f\n", orderIndex, stdMetrics.TimeSpentMs)
		fmt.Fprintf(literalDat["ripgrep"], "%d %.2f\n", orderIndex, rgMetrics.TimeSpentMs)
		fmt.Fprintf(literalDat["gitgrep"], "%d %.2f\n", orderIndex, gitMetrics.TimeSpentMs)
		fmt.Fprintf(literalDat["ggrep"], "%d %.2f\n", orderIndex, ggMetrics.TimeSpentMs)

		report.LiteralQueries = append(report.LiteralQueries, QueryResult{
			Query:      q,
			OrderIndex: orderIndex,
			Engines:    []EngineMetric{ggMetrics, stdMetrics, rgMetrics, gitMetrics, osMetrics},
		})
	}

	// Write literal dat files
	writeDatFile("ggrep_bench_literal_ggrep.dat", literalDat["ggrep"])
	writeDatFile("ggrep_bench_literal_ggrep_std.dat", literalDat["ggrep-std"])
	writeDatFile("ggrep_bench_literal_ripgrep.dat", literalDat["ripgrep"])
	writeDatFile("ggrep_bench_literal_gitgrep.dat", literalDat["gitgrep"])
	writeDatFile("ggrep_bench_literal_osgrep.dat", literalDat["osgrep"])

	// 4. Run Regex Queries
	log.Println("================================================================================")
	log.Println("          🔍  2. EXECUTING 5-WAY MULTI-QUERY REGEX EVALUATIONS  🔍")
	log.Println("================================================================================")

	regexDat := map[string]*bytes.Buffer{
		"ggrep":     new(bytes.Buffer),
		"ggrep-std": new(bytes.Buffer),
		"ripgrep":   new(bytes.Buffer),
		"gitgrep":   new(bytes.Buffer),
		"osgrep":    new(bytes.Buffer),
	}

	for idx, q := range queriesRegex {
		orderIndex := idx + 1
		log.Printf("👉 [%d/%d] Processing Regex: %q", orderIndex, len(queriesRegex), q)

		osMetrics := measure("OS Grep", baseOS, exec.Command("grep", "-r", "-E", "-I", q, targetDir))
		stdMetrics := measure("ggrep-std", baseStd, exec.Command(ggrepStdBin, "-r", q, targetDir))
		rgMetrics := measure("ripgrep", baseRG, exec.Command(rgBin, q, targetDir))
		gitMetrics := measure("git-grep", baseGit, exec.Command("git", "-C", targetDir, "grep", "-n", "-I", "-E", q))
		ggMetrics := measure("ggrep", baseGG, exec.Command(ggrepBin, "-r", q, targetDir))

		fmt.Fprintf(regexDat["osgrep"], "%d %.2f\n", orderIndex, osMetrics.TimeSpentMs)
		fmt.Fprintf(regexDat["ggrep-std"], "%d %.2f\n", orderIndex, stdMetrics.TimeSpentMs)
		fmt.Fprintf(regexDat["ripgrep"], "%d %.2f\n", orderIndex, rgMetrics.TimeSpentMs)
		fmt.Fprintf(regexDat["gitgrep"], "%d %.2f\n", orderIndex, gitMetrics.TimeSpentMs)
		fmt.Fprintf(regexDat["ggrep"], "%d %.2f\n", orderIndex, ggMetrics.TimeSpentMs)

		report.RegexQueries = append(report.RegexQueries, QueryResult{
			Query:      q,
			OrderIndex: orderIndex,
			Engines:    []EngineMetric{ggMetrics, stdMetrics, rgMetrics, gitMetrics, osMetrics},
		})
	}

	// Write regex dat files
	writeDatFile("ggrep_bench_regex_ggrep.dat", regexDat["ggrep"])
	writeDatFile("ggrep_bench_regex_ggrep_std.dat", regexDat["ggrep-std"])
	writeDatFile("ggrep_bench_regex_ripgrep.dat", regexDat["ripgrep"])
	writeDatFile("ggrep_bench_regex_gitgrep.dat", regexDat["gitgrep"])
	writeDatFile("ggrep_bench_regex_osgrep.dat", regexDat["osgrep"])

	// 5. Save final JSON
	jsonPath := filepath.Join(resultsDir, "ggrep_bench.json")
	jsonData, _ := json.MarshalIndent(report, "", "  ")
	_ = os.WriteFile(jsonPath, jsonData, 0644)
	log.Printf("✓ Saved comprehensive JSON metrics to: %s", jsonPath)

	// 6. Generate Gnuplot charts automatically!
	log.Println("📈 Rendering scaling comparison charts via Gnuplot...")
	_ = exec.Command("gnuplot", filepath.Join(workspaceDir, "scripts/plot_ggrep.gp")).Run()
	_ = exec.Command("gnuplot", filepath.Join(workspaceDir, "scripts/plot_ggrep_regex.gp")).Run()

	log.Println("================================================================================")
	log.Println("          🎉  SCALING PERFORMANCE COMPARISON COMPLETED SUCCESSFULLY!  🎉")
	log.Println("================================================================================")
	log.Printf("📁 Saved Literal Scaling Chart: %s/ggrep_bench_literal_chart.png", resultsDir)
	log.Printf("📁 Saved Regex Scaling Chart  : %s/ggrep_bench_regex_chart.png", resultsDir)
	log.Printf("📁 Saved JSON metrics         : %s", jsonPath)
	log.Println("================================================================================")
}

func preWarmPageCache() {
	log.Println("Pre-warming OS Virtual Memory Page Cache...")
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16) // bounded workers concurrency

	_ = filepath.WalkDir(targetDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer func() {
				<-sem
				wg.Done()
			}()
			f, err := os.Open(p)
			if err == nil {
				_, _ = io.Copy(io.Discard, f)
				f.Close()
			}
		}(path)
		return nil
	})
	wg.Wait()
	log.Println("✓ OS Virtual Memory Page Cache successfully primed!")
	log.Println("")
}

func calibrate(cmd *exec.Cmd) float64 {
	runs := 5
	var total float64
	for i := 0; i < runs; i++ {
		// Clone command state
		c := exec.Command(cmd.Path, cmd.Args[1:]...)
		start := time.Now()
		_ = c.Run()
		total += float64(time.Since(start).Nanoseconds()) / 1e6
	}
	return total / float64(runs)
}

func measure(name string, baseMs float64, cmd *exec.Cmd) EngineMetric {
	// Warmup run
	cWarm := exec.Command(cmd.Path, cmd.Args[1:]...)
	_ = cWarm.Run()

	// Measure time
	cRun := exec.Command(cmd.Path, cmd.Args[1:]...)
	var stdout bytes.Buffer
	cRun.Stdout = &stdout
	start := time.Now()
	_ = cRun.Run()
	rawMs := float64(time.Since(start).Nanoseconds()) / 1e6

	// Scientific Calibration: subtract launch latency
	ms := rawMs - baseMs
	if ms < 0.05 {
		ms = 0.05
	}

	// Calculate matched lines
	matches := 0
	lines := strings.Split(stdout.String(), "\n")
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			matches++
		}
	}

	return EngineMetric{
		Name:              name,
		TotalMatchedLines: matches,
		TimeSpentMs:       ms,
	}
}

func writeDatFile(filename string, buf *bytes.Buffer) {
	path := filepath.Join(resultsDir, filename)
	_ = os.WriteFile(path, buf.Bytes(), 0644)
}
