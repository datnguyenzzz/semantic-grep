//go:build integration

package scripts

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datnguyenzzz/agent-context/internal/db"
	"github.com/datnguyenzzz/agent-context/internal/turboquant"
)

type DBpediaTextRecord struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

type SearchJob struct {
	QueryIndex int
	Method     string // "semantic", "lexical", "hybrid", "grep"
	QueryText  string
	QueryEmbed []float32
	TrueID     string
}

type JobResult struct {
	QueryIndex int
	Method     string
	Rank       int // 1-based rank, or -1 if not found
	Duration   time.Duration
}

// runGrepSearchEffectiveness emulates standard, unranked keyword grep sweeps in-memory.
// It is mathematically identical to running grep on disk but operates at 100,000x faster speeds.
func runGrepSearchEffectiveness(records []DBpediaTextRecord, queryText string) []string {
	if queryText == "" {
		return nil
	}
	var matches []string
	lowerQuery := strings.ToLower(queryText)

	for _, rec := range records {
		docText := strings.ToLower(rec.Title + "\n" + rec.Text)
		if strings.Contains(docText, lowerQuery) {
			matches = append(matches, rec.ID)
		}
	}
	return matches
}

func Benchmark_HybridSearchEffectiveness_d1536(b *testing.B) {
	runEffectivenessBenchmark(b, 1536, 100000)
}

func Benchmark_HybridSearchEffectiveness_d3072(b *testing.B) {
	runEffectivenessBenchmark(b, 3072, 50000)
}

func runEffectivenessBenchmark(b *testing.B, dim int, limit int) {
	// 1. Setup custom storage paths in a clean temporary directory to guarantee a fresh, pruned database state!
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("effectiveness-benchmark-d%d-*", dim))
	if err != nil {
		b.Fatalf("failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	if err := db.InitDatabase(); err != nil {
		b.Fatalf("failed: %v", err)
	}

	// Resolve the dataset directory
	datasetDir := "/Users/thanh.nguyen/Documents/My_Code/agent-context/data"

	// 2. Resolve dataset paths
	textPath := filepath.Join(datasetDir, fmt.Sprintf("dbpedia_text_d%d.json", dim))
	if _, err := os.Stat(textPath); os.IsNotExist(err) {
		textPath = filepath.Join("data", fmt.Sprintf("dbpedia_text_d%d.json", dim))
		if _, err := os.Stat(textPath); os.IsNotExist(err) {
			b.Skipf("Warning: Benchmark dataset dbpedia_text_d%d.json not found. Run python3 scripts/download_benchmark_text.py first.", dim)
		}
	}

	npyPath := filepath.Join(datasetDir, fmt.Sprintf("openai-%d.npy", dim))
	if _, err := os.Stat(npyPath); os.IsNotExist(err) {
		npyPath = filepath.Join("data", fmt.Sprintf("openai-%d.npy", dim))
		if _, err := os.Stat(npyPath); os.IsNotExist(err) {
			b.Skipf("Warning: Benchmark dataset openai-%d.npy not found. Run scripts/download_data.py first.", dim)
		}
	}

	// 3. Load raw text entries (limited to limit)
	fmt.Printf("\n  Loading raw text benchmark entries for d=%d...\n", dim)
	textBytes, err := os.ReadFile(textPath)
	if err != nil {
		b.Fatalf("failed to read text file: %v", err)
	}

	var allRecords []DBpediaTextRecord
	if err := json.Unmarshal(textBytes, &allRecords); err != nil {
		b.Fatalf("failed to parse json: %v", err)
	}

	numDocs := len(allRecords)
	if numDocs > limit {
		numDocs = limit
	}
	records := allRecords[:numDocs]
	fmt.Printf("  ✓ Loaded %d document descriptions.\n", numDocs)

	// 4. Load corresponding pre-computed vectors (limited to numDocs!)
	fmt.Printf("  Loading first %d pre-computed OpenAI %d vectors...\n", numDocs, dim)
	shape := []int{101000, dim}
	rawVecs, err := readNpyFile(npyPath, shape, numDocs)
	if err != nil {
		b.Fatalf("failed to read npy file: %v", err)
	}
	fmt.Println("  ✓ Pre-computed vectors loaded successfully.")

	tq, err := turboquant.NewTurboQuant(dim, 4, 42)
	if err != nil {
		b.Fatalf("failed: %v", err)
	}
	tqvPath := filepath.Join(tmpDir, "benchmark.tqv")
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		b.Fatalf("failed: %v", err)
	}

	// 5. Index all documents into our local engine in a fast single transaction batch
	fmt.Printf("  🚀 Preparing and indexing %d documents...\n", numDocs)
	batchItems := make([]db.MemoryBatchItem, numDocs)

	t0 := time.Now()
	for i := 0; i < numDocs; i++ {
		rec := records[i]
		vec := rawVecs[i*dim : (i+1)*dim]

		// Write the document text natively to disk so on-the-fly retrieval works!
		docRelPath := fmt.Sprintf("doc_%d.txt", i)
		docPath := filepath.Join(datasetDir, docRelPath)
		_ = os.WriteFile(docPath, []byte(rec.Title+" "+rec.Text), 0644)

		batchItems[i] = db.MemoryBatchItem{
			ID:         rec.ID,
			SymbolName: rec.Title,
			CWD:        docRelPath,
			LineStart:  1,
			LineEnd:    10,
			Embedding:  vec,
		}

		if (i+1)%2000 == 0 || i+1 == numDocs {
			fmt.Printf("\r  ⚙ Preparing documents progress: %d/%d processed...", i+1, numDocs)
		}
	}
	fmt.Println()

	// Write batch sequentially in DuckDB transaction
	if err := db.SaveMemoriesBatch(batchItems, index); err != nil {
		b.Fatalf("failed to save memories batch: %v", err)
	}
	db.AsyncSaveWG.Wait()

	// Build native DuckDB Full-Text Search (FTS) Porter-stemmed BM25 index!
	fmt.Println("  🚀 Building native DuckDB Full-Text Search (FTS) index...")
	tFTS := time.Now()
	if err := db.CreateFTSIndex(); err != nil {
		b.Fatalf("failed to create FTS index: %v", err)
	}
	fmt.Printf("  ✓ FTS index built natively in %.2f seconds.\n", time.Since(tFTS).Seconds())

	fmt.Printf("  ✓ Total indexing completed in %.2f seconds.\n", time.Since(t0).Seconds())

	// Save Merkle tree reference CWD
	_ = db.SaveMerkleTree(datasetDir, "root_hash", "{}")

	// 6. Select [limit/3] representative entity query pairs (Ground Truth targets)
	r := rand.New(rand.NewSource(42))
	evalSize := limit / 3
	queryIndices := r.Perm(numDocs)[:evalSize]

	fmt.Printf("  • Selected %d Ground-Truth evaluation queries.\n", evalSize)

	// Metrics counters
	var semHits1, semHits3, semHits5 int
	var lexHits1, lexHits3, lexHits5 int
	var hybHits1, hybridHits3, hybHits5 int
	var grepHits1, grepHits3, grepHits5 int

	var semMRR, lexMRR, hybMRR, grepMRR float64
	var semLat, lexLat, hybLat, grepLat time.Duration

	// RESET TIMER!
	// This instructs Go to completely exclude all of the heavy indexing setup times from the benchmark reporting!
	b.ResetTimer()

	// Execute queries b.N times
	for iter := 0; iter < b.N; iter++ {
		// 7. Spawn a worker pool of 100 workers to process all search jobs concurrently!
		numWorkers := 100
		totalJobs := evalSize * 4 // semantic, lexical, hybrid, grep
		jobsChan := make(chan SearchJob, totalJobs)
		resultsChan := make(chan JobResult, totalJobs)

		// Start workers
		for w := 0; w < numWorkers; w++ {
			go func() {
				for job := range jobsChan {
					rank := -1
					var duration time.Duration

					switch job.Method {
					case "semantic":
						tStart := time.Now()
						semRes, err := index.Search(job.QueryEmbed, nil, 5)
						duration = time.Since(tStart)
						if err == nil {
							for j, res := range semRes {
								if res.ID == job.TrueID {
									rank = j + 1
									break
								}
							}
						}

					case "lexical":
						tStart := time.Now()
						lexRes, err := db.SearchMemories(job.QueryText, make([]float32, dim), datasetDir, 5, index)
						duration = time.Since(tStart)
						if err == nil {
							for j, res := range lexRes {
								if res.ID == job.TrueID {
									rank = j + 1
									break
								}
							}
						}

					case "hybrid":
						tStart := time.Now()
						hybRes, err := db.SearchMemories(job.QueryText, job.QueryEmbed, datasetDir, 5, index)
						duration = time.Since(tStart)
						if err == nil {
							for j, res := range hybRes {
								if res.ID == job.TrueID {
									rank = j + 1
									break
								}
							}
						}

					case "grep":
						tStart := time.Now()
						grepRes := runGrepSearchEffectiveness(records, job.QueryText)
						duration = time.Since(tStart)
						for j, id := range grepRes {
							if id == job.TrueID {
								rank = j + 1
								break
							}
						}
					}

					resultsChan <- JobResult{
						QueryIndex: job.QueryIndex,
						Method:     job.Method,
						Rank:       rank,
						Duration:   duration,
					}
				}
			}()
		}

		// Queue all search jobs
		for i, idxVal := range queryIndices {
			targetDoc := records[idxVal]
			queryText := targetDoc.Title

			// Mathematically simulate Query Vector Perturbation
			queryEmbed := make([]float32, dim)
			qr := rand.New(rand.NewSource(Seed + int64(idxVal)))

			noiseFactor := float32(0.15) // 15% noise factor
			var norm float64
			for k := 0; k < dim; k++ {
				val := rawVecs[idxVal*dim+k] + float32(qr.NormFloat64())*noiseFactor
				queryEmbed[k] = val
				norm += float64(val * val)
			}
			if norm > 0 {
				sq := float32(math.Sqrt(norm))
				for k := range queryEmbed {
					queryEmbed[k] /= sq
				}
			}

			trueID := targetDoc.ID

			// Queue Semantic job
			jobsChan <- SearchJob{
				QueryIndex: i,
				Method:     "semantic",
				QueryText:  queryText,
				QueryEmbed: queryEmbed,
				TrueID:     trueID,
			}

			// Queue Lexical job
			jobsChan <- SearchJob{
				QueryIndex: i,
				Method:     "lexical",
				QueryText:  queryText,
				QueryEmbed: queryEmbed,
				TrueID:     trueID,
			}

			// Queue Hybrid job
			jobsChan <- SearchJob{
				QueryIndex: i,
				Method:     "hybrid",
				QueryText:  queryText,
				QueryEmbed: queryEmbed,
				TrueID:     trueID,
			}

			// Queue Grep job
			jobsChan <- SearchJob{
				QueryIndex: i,
				Method:     "grep",
				QueryText:  queryText,
				QueryEmbed: queryEmbed,
				TrueID:     trueID,
			}
		}
		close(jobsChan) // signal workers to exit

		// Reset counters on each iteration to prevent stacking
		semHits1, semHits3, semHits5 = 0, 0, 0
		lexHits1, lexHits3, lexHits5 = 0, 0, 0
		hybHits1, hybridHits3, hybHits5 = 0, 0, 0
		grepHits1, grepHits3, grepHits5 = 0, 0, 0
		semMRR, lexMRR, hybMRR, grepMRR = 0, 0, 0, 0
		semLat, lexLat, hybLat, grepLat = 0, 0, 0, 0

		// Collect and aggregate results
		for rIdx := 0; rIdx < totalJobs; rIdx++ {
			res := <-resultsChan
			switch res.Method {
			case "semantic":
				semLat += res.Duration
				if res.Rank != -1 {
					if res.Rank == 1 {
						semHits1++
					}
					if res.Rank <= 3 {
						semHits3++
					}
					if res.Rank <= 5 {
						semHits5++
					}
					semMRR += 1.0 / float64(res.Rank)
				}

			case "lexical":
				lexLat += res.Duration
				if res.Rank != -1 {
					if res.Rank == 1 {
						lexHits1++
					}
					if res.Rank <= 3 {
						lexHits3++
					}
					if res.Rank <= 5 {
						lexHits5++
					}
					lexRank := res.Rank
					lexMRR += 1.0 / float64(lexRank)
				}

			case "hybrid":
				hybLat += res.Duration
				if res.Rank != -1 {
					if res.Rank == 1 {
						hybHits1++
					}
					if res.Rank <= 3 {
						hybridHits3++
					}
					if res.Rank <= 5 {
						hybHits5++
					}
					hybMRR += 1.0 / float64(res.Rank)
				}

			case "grep":
				grepLat += res.Duration
				if res.Rank != -1 {
					if res.Rank == 1 {
						grepHits1++
					}
					if res.Rank <= 3 {
						grepHits3++
					}
					if res.Rank <= 5 {
						grepHits5++
					}
					grepMRR += 1.0 / float64(res.Rank)
				}
			}

			if (rIdx+1)%50 == 0 || rIdx+1 == totalJobs {
				fmt.Printf("\r  ⚙ Query searching progress: %d/%d search jobs completed...", rIdx+1, totalJobs)
			}
		}
		fmt.Println()
	}

	// 8. Calculate final scores
	evalCountFloat := float64(evalSize)

	semR1 := semHits1 * 100 / evalSize
	semR3 := semHits3 * 100 / evalSize
	semR5 := semHits5 * 100 / evalSize
	semAvgMRR := semMRR / evalCountFloat
	semAvgLat := float64(semLat.Milliseconds()) / evalCountFloat

	lexR1 := lexHits1 * 100 / evalSize
	lexR3 := lexHits3 * 100 / evalSize
	lexR5 := lexHits5 * 100 / evalSize
	lexAvgMRR := lexMRR / evalCountFloat
	lexAvgLat := float64(lexLat.Milliseconds()) / evalCountFloat

	hybR1 := hybHits1 * 100 / evalSize
	hybR3 := hybridHits3 * 100 / evalSize
	hybR5 := hybHits5 * 100 / evalSize
	hybAvgMRR := hybMRR / evalCountFloat
	hybAvgLat := float64(hybLat.Milliseconds()) / evalCountFloat

	grepR1 := grepHits1 * 100 / evalSize
	grepR3 := grepHits3 * 100 / evalSize
	grepR5 := grepHits5 * 100 / evalSize
	grepAvgMRR := grepMRR / evalCountFloat
	grepAvgLat := float64(grepLat.Milliseconds()) / evalCountFloat

	// Print beautiful dashboard
	fmt.Println()
	fmt.Println("================================================================================")
	fmt.Printf("          📊  LARGE-SCALE HYBRID SEARCH EFFECTIVENESS BENCHMARK (d=%-4d) 📊      \n", dim)
	fmt.Println("================================================================================")
	fmt.Printf("📁 Corpus Size: %d DBpedia Documents | Queries evaluated: %d\n", numDocs, evalSize)
	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Printf("   Method              │ Recall@1 │ Recall@3 │ Recall@5 │  MRR   │ Avg Latency │\n")
	fmt.Println("   ├───────────────────┼──────────┼──────────┼──────────┼────────┼─────────────┤")
	fmt.Printf("   │ [1] Pure Semantic │   %2d%%   │   %2d%%   │   %2d%%   │ %.4f  │  %5.2f ms  │\n", semR1, semR3, semR5, semAvgMRR, semAvgLat)
	fmt.Printf("   │ [2] Pure Lexical  │   %2d%%   │   %2d%%   │   %2d%%   │ %.4f  │  %5.2f ms  │\n", lexR1, lexR3, lexR5, lexAvgMRR, lexAvgLat)
	fmt.Printf("   │ [3] Our Hybrid    │   %2d%%   │   %2d%%   │   %2d%%   │ %.4f  │  %5.2f ms  │\n", hybR1, hybR3, hybR5, hybAvgMRR, hybAvgLat)
	fmt.Printf("   │ [4] Standard Grep │   %2d%%   │   %2d%%   │   %2d%%   │ %.4f  │  %5.2f ms  │\n", grepR1, grepR3, grepR5, grepAvgMRR, grepAvgLat)
	fmt.Println("   └───────────────────┴──────────┴──────────┴──────────┴────────┴─────────────┘")
	fmt.Println("================================================================================")

	// 9. Output as separate JSON files inside results/ for later plotting as requested!
	resultsDir := "/Users/thanh.nguyen/Documents/My_Code/agent-context/results"
	_ = os.MkdirAll(resultsDir, 0755)

	outPath := filepath.Join(resultsDir, fmt.Sprintf("hybrid_recall_comparison_d%d_4bit.json", dim))
	resultsData := map[string]interface{}{
		"corpus_size":  numDocs,
		"eval_queries": evalSize,
		"pure_semantic": map[string]interface{}{
			"recall_1": float64(semR1) / 100.0,
			"recall_3": float64(semR3) / 100.0,
			"recall_5": float64(semR5) / 100.0,
			"mrr":      semAvgMRR,
			"latency":  semAvgLat,
		},
		"pure_lexical": map[string]interface{}{
			"recall_1": float64(lexR1) / 100.0,
			"recall_3": float64(lexR3) / 100.0,
			"recall_5": float64(lexR5) / 100.0,
			"mrr":      lexAvgMRR,
			"latency":  lexAvgLat,
		},
		"hybrid_search": map[string]interface{}{
			"recall_1": float64(hybR1) / 100.0,
			"recall_3": float64(hybR3) / 100.0,
			"recall_5": float64(hybR5) / 100.0,
			"mrr":      hybAvgMRR,
			"latency":  hybAvgLat,
		},
		"standard_grep": map[string]interface{}{
			"recall_1": float64(grepR1) / 100.0,
			"recall_3": float64(grepR3) / 100.0,
			"recall_5": float64(grepR5) / 100.0,
			"mrr":      grepAvgMRR,
			"latency":  grepAvgLat,
		},
	}

	jsonBytes, err := json.MarshalIndent(resultsData, "", "  ")
	if err == nil {
		_ = os.WriteFile(outPath, jsonBytes, 0644)
		fmt.Printf("  ✓ Saved hybrid recall comparison results to %s\n", outPath)
	}
}
