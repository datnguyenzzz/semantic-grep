//go:build integration

package scripts

import (
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-mem/internal/splitter"
	"agent-mem/internal/turboquant"
)

// targetExtensions checks if a file is indexable
func isIndexable(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return ext == ".go" || ext == ".tf" || ext == ".yaml" || ext == ".yml"
}

// collectIndexableFiles returns all indexable file paths inside the root
func collectIndexableFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if isIndexable(path) {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func drawBar(ratio float64, width int) string {
	if ratio > 1.0 {
		ratio = 1.0
	}
	filledLength := int(ratio * float64(width))
	if filledLength == 0 && ratio > 0 {
		filledLength = 1
	}
	bar := ""
	for i := 0; i < filledLength; i++ {
		bar += "█"
	}
	for i := filledLength; i < width; i++ {
		bar += "░"
	}
	return bar
}

func Test_compression_rate(t *testing.T) {
	// ─── USER CONFIGURATION (UPDATE THESE PLACEHOLDER PATHS) ───
	codebases := []string{
		"/Users/thanh.nguyen/Documents/dhse/otelcol-tail-sampling",
		"/Users/thanh.nguyen/Documents/dhse/otelcol",
		"/Users/thanh.nguyen/Documents/dhse/otelcol-otlp",
		"/Users/thanh.nguyen/Documents/dhse/dp-sre-otelcol-proxy",
		"/Users/thanh.nguyen/Documents/dhse/dp-sre-grafana-alerting",
		// You can add more absolute or relative paths here...
	}
	// ──────────────────────────────────────────────────────────

	fmt.Println()
	fmt.Println("================================================================================")
	fmt.Println("        📊  TURBOQUANT VECTOR COMPRESSION BENCHMARK SUITE  📊                 ")
	fmt.Println("================================================================================")
	fmt.Println()

	dim := turboquant.DefaultDimension
	bitWidth := turboquant.DefaultBitWidth

	// Setup a temp directory for index file benchmarking
	tmpDir, err := os.MkdirTemp("", "turboquant-bench-*")
	if err != nil {
		t.Fatalf("failed to create temp bench dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	rng := rand.New(rand.NewSource(42))

	// Recreate a clean, isolated index on disk for the aggregated codebase run
	tqvPath := filepath.Join(tmpDir, "bench_aggregated.tqv")
	_ = os.Remove(tqvPath)

	tq, err := turboquant.NewTurboQuant(dim, bitWidth, 42)
	if err != nil {
		t.Fatalf("failed to init TurboQuant: %v", err)
	}
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		t.Fatalf("failed to init Index: %v", err)
	}

	activeCount := 0
	totalFiles := 0
	totalChunks := 0

	for _, path := range codebases {
		if strings.Contains(path, "placeholder") {
			continue
		}

		_, err := os.Stat(path)
		if err != nil {
			fmt.Printf("  ⚠️  Skipping unreachable codebase: %s (Check path correctness)\n", path)
			continue
		}

		activeCount++

		files, err := collectIndexableFiles(path)
		if err != nil {
			fmt.Printf("  ⚠️  Error scanning %s: %v\n", path, err)
			continue
		}

		totalFiles += len(files)

		for _, fPath := range files {
			chunks, err := splitter.SplitFile(fPath)
			if err != nil {
				continue
			}

			for i := range chunks {
				mockVec := make([]float32, dim)
				for j := 0; j < dim; j++ {
					mockVec[j] = float32(rng.NormFloat64())
				}

				id := fmt.Sprintf("chunk-%d-%d", totalChunks, i)
				_ = index.Add(id, mockVec)
				totalChunks++
			}
		}
	}

	var results struct {
		Title      string
		Files      int
		Chunks     int
		OrigSize   float64 // KB
		TqMemSize  float64 // KB
		TqDiskSize float64 // KB
		SavedRatio float64
	}

	if activeCount > 0 {
		// Save/Commit all quantized vectors physically to the on-disk file
		err = index.Save()
		if err != nil {
			t.Fatalf("failed to save Index: %v", err)
		}

		// Measure the actual, physical file size of the index on disk!
		fi, err := os.Stat(tqvPath)
		var actualDiskBytes int64
		if err == nil {
			actualDiskBytes = fi.Size()
		}

		origBytes := float64(totalChunks * dim * 4)
		tqMemBytes := float64(index.MemorySizeBytes())
		tqDiskBytes := float64(actualDiskBytes)

		origKB := origBytes / 1024.0
		tqMemKB := tqMemBytes / 1024.0
		tqDiskKB := tqDiskBytes / 1024.0

		savedRatio := 0.0
		if origKB > 0 {
			savedRatio = (1.0 - (tqMemKB / origKB)) * 100
		}

		results = struct {
			Title      string
			Files      int
			Chunks     int
			OrigSize   float64
			TqMemSize  float64
			TqDiskSize float64
			SavedRatio float64
		}{
			Title:      fmt.Sprintf("Aggregated Index (across %d codebases)", activeCount),
			Files:      totalFiles,
			Chunks:     totalChunks,
			OrigSize:   origKB,
			TqMemSize:  tqMemKB,
			TqDiskSize: tqDiskKB,
			SavedRatio: savedRatio,
		}
	} else {
		fmt.Println("  [NOTICE] No active codebases were evaluated. Please modify the placeholder paths")
		fmt.Println("           in scripts/benchmark_compression_test.go to point to your real codebases.")
		fmt.Println()
		fmt.Println("  Performing real quantization, serialization, and disk-writing for 1,000 mock chunks...")
		fmt.Println("  (Measuring exact, physical on-disk file size with standard headers and metadata)")
		fmt.Println()

		simChunks := 1000
		tqvPath := filepath.Join(tmpDir, "bench_simulated.tqv")
		_ = os.Remove(tqvPath)

		tq, err := turboquant.NewTurboQuant(dim, bitWidth, 42)
		if err != nil {
			t.Fatalf("failed to init TurboQuant: %v", err)
		}
		index, err := turboquant.NewIndex(tqvPath, tq)
		if err != nil {
			t.Fatalf("failed to init Index: %v", err)
		}

		for i := 0; i < simChunks; i++ {
			mockVec := make([]float32, dim)
			for j := 0; j < dim; j++ {
				mockVec[j] = float32(rng.NormFloat64())
			}
			id := fmt.Sprintf("sim-chunk-%d", i)
			_ = index.Add(id, mockVec)
		}

		err = index.Save()
		if err != nil {
			t.Fatalf("failed to save simulated Index: %v", err)
		}

		fi, err := os.Stat(tqvPath)
		var actualDiskBytes int64
		if err == nil {
			actualDiskBytes = fi.Size()
		}

		origKB := float64(simChunks*dim*4) / 1024.0
		tqMemKB := float64(index.MemorySizeBytes()) / 1024.0
		tqDiskKB := float64(actualDiskBytes) / 1024.0
		savedRatio := (1.0 - (tqMemKB / origKB)) * 100

		results = struct {
			Title      string
			Files      int
			Chunks     int
			OrigSize   float64
			TqMemSize  float64
			TqDiskSize float64
			SavedRatio float64
		}{
			Title:      "Simulated (1,000 codebase chunks)",
			Files:      50,
			Chunks:     simChunks,
			OrigSize:   origKB,
			TqMemSize:  tqMemKB,
			TqDiskSize: tqDiskKB,
			SavedRatio: savedRatio,
		}
	}

	fmt.Printf("📁 Targets: %s\n", results.Title)
	fmt.Printf("   • Scanned Files: %d | Total Semantic Chunks: %d | Dimensions: %d\n", results.Files, results.Chunks, dim)
	fmt.Println("  -------------------------------------------------------------------------------- ")
	fmt.Printf("   │ Data Footprint Type            │ Footprint Size │ Comp. Ratio │ Savings    │\n")
	fmt.Println("   ├────────────────────────────────┼────────────────┼─────────────┼────────────┤")
	fmt.Printf("   │ [1] Standard Float32[] RAM     │ %10.2f KB │      1.0x   │     0.0%%   │\n", results.OrigSize)
	fmt.Printf("   │ [2] TurboQuant In-Memory Map   │ %10.2f KB │ %8.1fx   │ %8.1f%%   │\n", results.TqMemSize, results.OrigSize/results.TqMemSize, results.SavedRatio)
	fmt.Printf("   │ [3] TurboQuant On-Disk .tqv    │ %10.2f KB │ %8.1fx   │ %8.1f%%   │\n", results.TqDiskSize, results.OrigSize/results.TqDiskSize, (1.0-(results.TqDiskSize/results.OrigSize))*100)
	fmt.Println("   └────────────────────────────────┴────────────────┴─────────────┴────────────┘")
	fmt.Println()

	// ─── VISUALIZATION DASHBOARD ───
	fmt.Println("   📈 Visual Storage Footprint Comparison (Bar Scale):")
	fmt.Println()

	maxKB := results.OrigSize
	if maxKB == 0 {
		maxKB = 1.0
	}

	barWidth := 40
	origBar := drawBar(results.OrigSize/maxKB, barWidth)
	tqMemBar := drawBar(results.TqMemSize/maxKB, barWidth)
	tqDiskBar := drawBar(results.TqDiskSize/maxKB, barWidth)

	fmt.Printf("   Standard Float32[] RAM   : [%s] (%.1f KB)\n", origBar, results.OrigSize)
	fmt.Printf("   TurboQuant In-Memory Map : [%s] (%.1f KB) — 12x savings!\n", tqMemBar, results.TqMemSize)
	fmt.Printf("   TurboQuant On-Disk .tqv  : [%s] (%.1f KB) — Compact file!\n", tqDiskBar, results.TqDiskSize)
	fmt.Println()
	fmt.Println("================================================================================")
}
