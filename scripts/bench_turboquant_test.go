//go:build integration

package scripts

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datnguyenzzz/agent-context/internal/turboquant"
)

const (
	Seed     = 42
	BitWidth = 4
	K        = 32
)

var KValues = []int{1, 2, 4, 8, 16, 32}

func readNpyFile(path string, shape []int, limit int) ([]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Read magic and version (10 bytes)
	magic := make([]byte, 8)
	if _, err := io.ReadFull(f, magic); err != nil {
		return nil, err
	}

	var headerLen uint16
	if err := binary.Read(f, binary.LittleEndian, &headerLen); err != nil {
		return nil, err
	}

	headerStr := make([]byte, headerLen)
	if _, err := io.ReadFull(f, headerStr); err != nil {
		return nil, err
	}

	size := 1
	if len(shape) > 0 {
		firstDim := shape[0]
		if limit > 0 && limit < firstDim {
			firstDim = limit
		}
		size = firstDim
		for i := 1; i < len(shape); i++ {
			size *= shape[i]
		}
	}

	data := make([]float32, size)
	if err := binary.Read(f, binary.LittleEndian, data); err != nil {
		return nil, err
	}

	return data, nil
}

func loadOpenAI(dim int) ([][]float32, [][]float32, error) {
	path := "/Users/thanh.nguyen/Documents/My_Code/agent-context/data/" + fmt.Sprintf("openai-%d.npy", dim)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		panic("Warning: Benchmark dataset openai-%d.npy not found. Run scripts/download_data.py first.")
	}

	shape := []int{101000, dim}
	raw, err := readNpyFile(path, shape, 0)
	if err != nil {
		return nil, nil, err
	}

	totalVecs := 101000
	allVecs := make([][]float32, totalVecs)
	for i := 0; i < totalVecs; i++ {
		allVecs[i] = raw[i*dim : (i+1)*dim]
	}

	// Shuffle index permutation using seed 42
	r := rand.New(rand.NewSource(Seed))
	perm := r.Perm(totalVecs)

	database := make([][]float32, 100000)
	for i := 0; i < 100000; i++ {
		vec := make([]float32, dim)
		copy(vec, allVecs[perm[i]])
		normalize(vec)
		database[i] = vec
	}

	queries := make([][]float32, 1000)
	for i := 0; i < 1000; i++ {
		vec := make([]float32, dim)
		copy(vec, allVecs[perm[100000+i]])
		normalize(vec)
		queries[i] = vec
	}

	return database, queries, nil
}

func normalize(vec []float32) {
	var norm float64
	for _, val := range vec {
		norm += float64(val * val)
	}
	if norm > 0 {
		sq := float32(math.Sqrt(norm))
		for i := range vec {
			vec[i] /= sq
		}
	}
}

func computeRecall(trueTop1 []string, predicted [][]string, k int) float64 {
	hits := 0
	for i, trueID := range trueTop1 {
		for j := 0; j < k && j < len(predicted[i]); j++ {
			if predicted[i][j] == trueID {
				hits++
				break
			}
		}
	}
	return float64(hits) / float64(len(trueTop1))
}

func Test_Recall_TurboQuant(t *testing.T) {
	dimensions := []int{1536, 3072}

	for _, dim := range dimensions {
		fmt.Printf("\n=== OpenAI d=%d %d-bit (seed=%d) ===\n", dim, BitWidth, Seed)

		database, queries, err := loadOpenAI(dim)
		if err != nil {
			fmt.Printf("  ⚠️  Skipping: failed to load dataset for dimension %d: %v\n", dim, err)
			continue
		}

		// 1. Calculate Ground Truth exact top-1
		fmt.Println("  Calculating Ground Truth nearest neighbors...")
		trueTop1IDs := make([]string, len(queries))
		for i, q := range queries {
			bestIdx := -1
			bestSim := -1.0
			for j, dbVec := range database {
				dot := 0.0
				for k := range q {
					dot += float64(q[k] * dbVec[k])
				}
				if dot > bestSim {
					bestSim = dot
					bestIdx = j
				}
			}
			trueTop1IDs[i] = fmt.Sprintf("db-%d", bestIdx)
		}

		// 2. Initialize TurboQuant and Index
		tq, err := turboquant.NewTurboQuant(dim, BitWidth, Seed)
		if err != nil {
			panic(err)
		}

		tqvPath := filepath.Join(os.TempDir(), fmt.Sprintf("bench_turboquant_%d.tqv", dim))
		_ = os.Remove(tqvPath)
		defer os.Remove(tqvPath)

		index, err := turboquant.NewIndex(tqvPath, tq)
		if err != nil {
			panic(err)
		}

		// 3. Add database vectors to TurboQuant
		fmt.Println("  Adding vectors to TurboQuant index...")
		t0 := time.Now()
		for i, vec := range database {
			id := fmt.Sprintf("db-%d", i)
			_ = index.Add(id, vec)
		}
		addDur := time.Since(t0)
		fmt.Printf("  Added 100,000 vectors in %.1f seconds.\n", addDur.Seconds())

		// 4. Search and retrieve top-K nearest neighbors
		fmt.Println("  Running search queries against TurboQuant...")
		t1 := time.Now()
		predicted := make([][]string, len(queries))
		for i, q := range queries {
			results, err := index.Search(q, nil, K)
			if err != nil {
				panic(err)
			}
			ids := make([]string, len(results))
			for j, res := range results {
				ids[j] = res.ID
			}
			predicted[i] = ids
		}
		searchDur := time.Since(t1)

		// 5. Calculate Recall-1-@k
		recalls := make(map[int]float64)
		for _, kVal := range KValues {
			recalls[kVal] = computeRecall(trueTop1IDs, predicted, kVal)
		}

		fmt.Printf("  TurboQuant search (%.1fms) recall@1 = %.4f\n", float64(searchDur.Milliseconds()), recalls[1])
		fmt.Println("\n  TurboQuant Recalls:")
		for _, kVal := range KValues {
			fmt.Printf("    Recall-1-@%-2d : %.4f\n", kVal, recalls[kVal])
		}

		// 6. Save results directly to a clean, isolated JSON file starting with tq_
		tqRecalls := make(map[string]float64)
		for _, kv := range KValues {
			tqRecalls[fmt.Sprintf("%d", kv)] = math.Round(recalls[kv]*10000) / 10000
		}

		resultsDir := "/Users/thanh.nguyen/Documents/My_Code/agent-context/results"
		_ = os.MkdirAll(resultsDir, 0755)
		outPath := filepath.Join(resultsDir, fmt.Sprintf("tq_recall_d%d_4bit.json", dim))

		resultsData := make(map[string]interface{})
		resultsData["dataset"] = fmt.Sprintf("openai-%d", dim)
		resultsData["dim"] = dim
		resultsData["bit_width"] = BitWidth
		resultsData["seed"] = Seed
		resultsData["tq_recalls"] = tqRecalls

		jsonBytes, err := json.MarshalIndent(resultsData, "", "  ")
		if err == nil {
			_ = os.WriteFile(outPath, jsonBytes, 0644)
			fmt.Printf("  ✓ Saved recall results to %s\n", outPath)
		}
	}
}
