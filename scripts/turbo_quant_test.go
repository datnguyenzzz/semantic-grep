//go:build integration

package scripts

import (
	"agent-mem/internal/llm"
	"agent-mem/internal/turboquant"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"testing"
	"time"
)

func Test_TurboQuant(t *testing.T) {
	// ── Scenario 1: AI Embedding Vectors (TurboQuant's strength) ──
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Scenario 1: AI Embedding Vectors (simulating LLM KV Cache)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	dim := 256
	numVecs := 1000
	rng := rand.New(rand.NewSource(42))

	vecs := make([][]float32, numVecs)
	for i := range vecs {
		v := make([]float32, dim)
		for j := range v {
			v[j] = float32(rng.NormFloat64())
		}
		vecs[i] = v
	}

	rawBytes := vecsToBytes(vecs)
	origSize := len(rawBytes)
	fmt.Printf("  Data: %d vectors, dim %d, original size %.2f KB\n\n", numVecs, dim, float64(origSize)/1024)

	printHeader()
	benchGzip(rawBytes, origSize, vecs)
	benchZlib(rawBytes, origSize, vecs)
	benchTQ(2, dim, origSize, vecs)
	benchTQ(3, dim, origSize, vecs)
	benchTQ(4, dim, origSize, vecs)
	printFooter()
	fmt.Println()

	// ── Scenario 2: Repetitive Text (gzip/zlib's strength) ──
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Scenario 2: Repetitive Text Data (generic compression excels)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	textData := generateRepetitiveText(100000)
	fmt.Printf("  Data: repetitive text, original size %.2f KB\n\n", float64(len(textData))/1024)

	printHeaderSimple()
	benchGenericSimple("gzip", textData, compressGzip)
	benchGenericSimple("zlib", textData, compressZlib)
	fmt.Printf("│ TurboQuant │    N/A     │   N/A    │  N/A   │ N/A: text is not vector data       │\n")
	printFooterSimple()
	fmt.Println()

	// ── Scenario 3: Random Binary (neither excels) ──
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Scenario 3: Random Binary Data (high entropy, no patterns)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	randomData := make([]byte, 100000)
	rng2 := rand.New(rand.NewSource(99))
	for i := range randomData {
		randomData[i] = byte(rng2.Intn(256))
	}
	fmt.Printf("  Data: random bytes, original size %.2f KB\n\n", float64(len(randomData))/1024)

	printHeaderSimple()
	benchGenericSimple("gzip", randomData, compressGzip)
	benchGenericSimple("zlib", randomData, compressZlib)
	fmt.Printf("│ TurboQuant │    N/A     │   N/A    │  N/A   │ N/A: random data is not vectors    │\n")
	printFooterSimple()
	fmt.Println()

	// ── Scenario 4: Real-World LiteLLM Embeddings (User request) ──
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Scenario 4: Real-World LiteLLM Embeddings Quantization (User request)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	// ponytail: skip live LiteLLM embedding quantization test if server is not reachable
	conn, err := net.DialTimeout("tcp", "localhost:36253", 100*time.Millisecond)
	if err != nil {
		fmt.Println("  [Skipping Real-World LiteLLM Embeddings test: local LiteLLM on localhost:36253 is offline]")
		fmt.Println()
	} else {
		conn.Close()

		sampleTexts := []string{
			"Persistent memory is critical for AI agents.",
			"Go and DuckDB provide high performance.",
			"Merkle trees enable fast codebase indexing.",
		}

		for _, txt := range sampleTexts {
			fmt.Printf("  • Text: \"%s\"\n", txt)
			embedding, err := llm.GetEmbedding(txt)
			if err != nil {
				fmt.Printf("    ERROR generating embedding: %v\n\n", err)
				continue
			}

			dim := len(embedding)
			origSize := dim * 4 // size of float32 slice in bytes
			fmt.Printf("    Dimension: %d, Original Size: %d bytes\n", dim, origSize)

			for _, bw := range []int{2, 3, 4} {
				tq, err := turboquant.NewTurboQuant(dim, bw, 42)
				if err != nil {
					fmt.Printf("    %d-bit: error initializing: %v\n", bw, err)
					continue
				}

				qv, err := tq.Quantize(embedding)
				if err != nil {
					fmt.Printf("    %d-bit: error quantizing: %v\n", bw, err)
					continue
				}

				ser, err := tq.Serialize(qv)
				if err != nil {
					fmt.Printf("    %d-bit: error serializing: %v\n", bw, err)
					continue
				}

				deser, err := tq.Deserialize(ser)
				if err != nil {
					fmt.Printf("    %d-bit: error deserializing: %v\n", bw, err)
					continue
				}

				dequantized, err := tq.Dequantize(deser)
				if err != nil {
					fmt.Printf("    %d-bit: error dequantizing: %v\n", bw, err)
					continue
				}

				sim, err := turboquant.CosineSimilarity(embedding, dequantized)
				if err != nil {
					fmt.Printf("    %d-bit: error calculating similarity: %v\n", bw, err)
					continue
				}

				ratio := float64(origSize) / float64(len(ser))
				fmt.Printf("    [%d-bit] Serialized Size: %d bytes | Ratio: %5.1fx | Cosine Similarity: %.4f\n", bw, len(ser), ratio, sim)
			}
			fmt.Println()
		}
	}

	// ── Scenario 5: Pre-Rotated Fast Quantized Search (Mathematical Correctness Verification) ──
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Scenario 5: Pre-Rotated Fast Quantized Search (Mathematical Check)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	dim = 3072
	tq, err := turboquant.NewTurboQuant(dim, 4, 42)
	if err != nil {
		t.Fatalf("failed to initialize TurboQuant: %v", err)
	}

	// Generate a query vector and a matching/noise vector
	query := make([]float32, dim)
	match := make([]float32, dim)
	for i := 0; i < dim; i++ {
		query[i] = float32(rng.NormFloat64())
		match[i] = query[i] + float32(rng.NormFloat64()*0.1) // Add slight noise
	}

	// Quantize and serialize the match vector
	qvMatch, err := tq.Quantize(match)
	if err != nil {
		t.Fatalf("failed to quantize match vector: %v", err)
	}

	// 1. Compute similarity using standard dequantize + CosineSimilarity loop
	dequantized, err := tq.Dequantize(qvMatch)
	if err != nil {
		t.Fatalf("failed to dequantize match vector: %v", err)
	}

	standardSim, err := turboquant.CosineSimilarity(query, dequantized)
	if err != nil {
		t.Fatalf("failed to calculate standard similarity: %v", err)
	}

	// 2. Compute similarity using the fast pre-rotated scoring loop
	preparedQuery, err := tq.PrepareQuery(query)
	if err != nil {
		t.Fatalf("failed to prepare query: %v", err)
	}

	fastSim := tq.ScorePrepared(preparedQuery, qvMatch)

	fmt.Printf("  • Standard Similarity (Dequantize + CosSim):  %.6f\n", standardSim)
	fmt.Printf("  • Optimized Similarity (Pre-rotated + Score):  %.6f\n", fastSim)

	// Check if the fast scored value matches the standard one within float32 precision epsilon (1e-5)
	diff := standardSim - fastSim
	if diff < 0 {
		diff = -diff
	}

	if diff > 1e-5 {
		t.Errorf("fast scoring mathematical discrepancy too high: standard=%.6f, fast=%.6f, diff=%.6f", standardSim, fastSim, diff)
	} else {
		fmt.Println("  ✓ Mathematical verification passed successfully (difference < 1e-5)!")
	}
	fmt.Println()
}

// ─── helpers ───

func vecsToBytes(vecs [][]float32) []byte {
	var buf bytes.Buffer
	for _, v := range vecs {
		for _, f := range v {
			_ = binary.Write(&buf, binary.LittleEndian, f)
		}
	}
	return buf.Bytes()
}

func bytesToVecs(data []byte, dim int) [][]float32 {
	numVecs := len(data) / (dim * 4)
	vecs := make([][]float32, numVecs)
	reader := bytes.NewReader(data)
	for i := range vecs {
		v := make([]float32, dim)
		for j := range v {
			_ = binary.Read(reader, binary.LittleEndian, &v[j])
		}
		vecs[i] = v
	}
	return vecs
}

func compressGzip(data []byte) ([]byte, time.Duration) {
	var buf bytes.Buffer
	start := time.Now()
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = w.Write(data)
	_ = w.Close()
	return buf.Bytes(), time.Since(start)
}

func decompressGzip(data []byte) ([]byte, time.Duration) {
	start := time.Now()
	r, _ := gzip.NewReader(bytes.NewReader(data))
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	_ = r.Close()
	return buf.Bytes(), time.Since(start)
}

func compressZlib(data []byte) ([]byte, time.Duration) {
	var buf bytes.Buffer
	start := time.Now()
	w, _ := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	_, _ = w.Write(data)
	_ = w.Close()
	return buf.Bytes(), time.Since(start)
}

func decompressZlib(data []byte) ([]byte, time.Duration) {
	start := time.Now()
	r, _ := zlib.NewReader(bytes.NewReader(data))
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	_ = r.Close()
	return buf.Bytes(), time.Since(start)
}

func generateRepetitiveText(size int) []byte {
	patterns := []string{
		"The quick brown fox jumps over the lazy dog. ",
		"Hello World! This is a test of compression. ",
		"Lorem ipsum dolor sit amet, consectetur adipiscing elit. ",
		"AAAAAABBBBBBCCCCCCDDDDDD ",
	}
	var buf bytes.Buffer
	for buf.Len() < size {
		buf.WriteString(patterns[buf.Len()%len(patterns)])
	}
	return buf.Bytes()[:size]
}

func avgCosineSim(original, restored [][]float32, n int) float64 {
	if n > len(original) {
		n = len(original)
	}
	var total float64
	for i := 0; i < n; i++ {
		sim, err := turboquant.CosineSimilarity(original[i], restored[i])
		if err != nil {
			continue
		}
		total += sim
	}
	return total / float64(n)
}

// ─── print helpers ───

func printHeader() {
	fmt.Println("┌──────────────────┬────────────┬──────────┬──────────┬──────────┬────────────────┐")
	fmt.Println("│ Method           │ Compressed │ Ratio    │ Comp Time│ Dec Time │ Cosine Sim     │")
	fmt.Println("├──────────────────┼────────────┼──────────┼──────────┼──────────┼────────────────┤")
}

func printFooter() {
	fmt.Println("└───────────────────┴────────────┴──────────┴──────────┴──────────┴───────────────┘")
}

func printHeaderSimple() {
	fmt.Println("┌────────────┬────────────┬──────────┬────────┬─────────────────────────────────────┐")
	fmt.Println("│ Method     │ Compressed │ Ratio    │ Time   │ Note                                │")
	fmt.Println("├────────────┼────────────┼──────────┼────────┼─────────────────────────────────────┤")
}

func printFooterSimple() {
	fmt.Println("└────────────┴────────────┴──────────┴────────┴─────────────────────────────────────┘")
}

func benchGzip(rawBytes []byte, origSize int, vecs [][]float32) {
	compressed, compTime := compressGzip(rawBytes)
	decompressed, decompTime := decompressGzip(compressed)
	restoredVecs := bytesToVecs(decompressed, len(vecs[0]))
	sim := avgCosineSim(vecs, restoredVecs, 100)
	ratio := float64(origSize) / float64(len(compressed))
	fmt.Printf("│ gzip (lossless)  │ %7.1f KB │ %5.2fx   │ %6s   │ %6s   │ %.4f (exact) │\n",
		float64(len(compressed))/1024, ratio,
		compTime.Round(time.Millisecond), decompTime.Round(time.Millisecond), sim)
}

func benchZlib(rawBytes []byte, origSize int, vecs [][]float32) {
	compressed, compTime := compressZlib(rawBytes)
	decompressed, decompTime := decompressZlib(compressed)
	restoredVecs := bytesToVecs(decompressed, len(vecs[0]))
	sim := avgCosineSim(vecs, restoredVecs, 100)
	ratio := float64(origSize) / float64(len(compressed))
	fmt.Printf("│ zlib (lossless)  │ %7.1f KB │ %5.2fx   │ %6s   │ %6s   │ %.4f (exact) │\n",
		float64(len(compressed))/1024, ratio,
		compTime.Round(time.Millisecond), decompTime.Round(time.Millisecond), sim)
}

func benchTQ(bw, dim, origSize int, vecs [][]float32) {
	tq, err := turboquant.NewTurboQuant(dim, bw, 42)
	if err != nil {
		fmt.Printf("│ TQ %d-bit       │ ERROR: %v\n", bw, err)
		return
	}

	start := time.Now()
	qvs, err := tq.QuantizeBatch(vecs)
	if err != nil {
		fmt.Printf("│ TQ %d-bit       │ ERROR: %v\n", bw, err)
		return
	}
	compTime := time.Since(start)

	var totalSerSize int
	for _, qv := range qvs {
		data, _ := tq.Serialize(qv)
		totalSerSize += len(data)
	}

	start = time.Now()
	restored, _ := tq.DequantizeBatch(qvs)
	decompTime := time.Since(start)

	sim := avgCosineSim(vecs, restored, 100)
	ratio := float64(origSize) / float64(totalSerSize)

	label := fmt.Sprintf("TQ %d-bit (lossy)", bw)
	fmt.Printf("│ %-14s │ %7.1f KB │ %5.1fx   │ %6s   │ %6s   │ %.4f (approx)│\n",
		label,
		float64(totalSerSize)/1024, ratio,
		compTime.Round(time.Millisecond), decompTime.Round(time.Millisecond), sim)
}

func benchGenericSimple(name string, data []byte, compressFn func([]byte) ([]byte, time.Duration)) {
	compressed, compTime := compressFn(data)
	ratio := float64(len(data)) / float64(len(compressed))
	saved := (1.0 - 1.0/ratio) * 100
	fmt.Printf("│ %-10s │ %7.1f KB │ %5.1fx   │ %4s   │ Lossless, saved %.0f%%               │\n",
		name, float64(len(compressed))/1024, ratio,
		compTime.Round(time.Millisecond), saved)
}
