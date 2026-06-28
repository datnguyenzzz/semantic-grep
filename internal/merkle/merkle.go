package merkle

// Merkle tree-based incremental indexer to prune unchanged code subtrees, avoiding redundant chunking & LLM embedding generation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/datnguyenzzz/semantic-grep/internal/callgraph"
	"github.com/datnguyenzzz/semantic-grep/internal/db"
	"github.com/datnguyenzzz/semantic-grep/internal/llm"
	"github.com/datnguyenzzz/semantic-grep/internal/splitter"
	"github.com/datnguyenzzz/semantic-grep/internal/turboquant"

	"github.com/google/uuid"
)

type NodeType string

const (
	NodeFile      NodeType = "file"
	NodeDirectory NodeType = "directory"
)

type MerkleNode struct {
	Type NodeType `json:"type"`
	Name string   `json:"name"`
	// Path is absolute path
	Path     string                 `json:"path"`
	Hash     string                 `json:"hash"`
	Children map[string]*MerkleNode `json:"children,omitempty"`
}

func sha256Hash(data string) string {
	h := sha256.New()
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

func isIndexable(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return ext == ".go" || ext == ".tf" || ext == ".yaml" || ext == ".yml"
}

// BuildMerkleTree scans the filesystem recursively and constructs the Merkle Tree of indexable files
func BuildMerkleTree(absPath string) (*MerkleNode, error) {
	fi, err := os.Stat(absPath)
	if err != nil {
		return nil, err
	}

	if !fi.IsDir() {
		if !isIndexable(fi.Name()) {
			return nil, nil
		}

		// Split file into AST-based chunks to determine the chunk structure & hashes
		chunks, err := splitter.SplitFile(absPath)
		if err != nil {
			return nil, err
		}

		var chunkHashes []string
		for _, chunk := range chunks {
			chunkHash := sha256Hash(chunk.Content)
			chunkHashes = append(chunkHashes, chunkHash)
		}

		// Compute file hash from chunk hashes
		var fileHash string
		if len(chunkHashes) == 0 {
			fileHash = sha256Hash("")
		} else {
			fileHash = sha256Hash(strings.Join(chunkHashes, ""))
		}

		return &MerkleNode{
			Type: NodeFile,
			Name: fi.Name(),
			Path: absPath,
			Hash: fileHash,
		}, nil
	}

	// It's a directory, read its children
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return nil, err
	}

	children := make(map[string]*MerkleNode)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			// Skip ignorable folders
			if name == ".git" || name == "node_modules" || name == "dist" || name == "bin" || name == "vendor" || name == ".gemini" {
				continue
			}
		}

		childAbsPath := filepath.Join(absPath, name)
		childNode, err := BuildMerkleTree(childAbsPath)
		if err != nil {
			return nil, err
		}
		if childNode != nil {
			children[name] = childNode
		}
	}

	// If directory contains no indexable files, exclude it from tree
	if len(children) == 0 {
		return nil, nil
	}

	// Calculate deterministic hash by sorting child nodes
	var keys []string
	for name := range children {
		keys = append(keys, name)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, name := range keys {
		sb.WriteString(name)
		sb.WriteString(children[name].Hash)
	}
	dirHash := sha256Hash(sb.String())

	return &MerkleNode{
		Type:     NodeDirectory,
		Name:     fi.Name(),
		Path:     absPath,
		Hash:     dirHash,
		Children: children,
	}, nil
}

// DiffTrees compares a previous and a new Merkle Tree to find added, modified, and deleted files.
// Prunes tree traversal instantly for matching node hashes.
func DiffTrees(prev, next *MerkleNode) (added, modified, deleted []string) {
	if prev == nil && next == nil {
		return
	}

	if prev == nil {
		// All files under next are added
		return collectFiles(next), nil, nil
	}

	if next == nil {
		// All files under prev are deleted
		return nil, nil, collectFiles(prev)
	}

	if prev.Hash == next.Hash {
		// Subtree is identical
		return nil, nil, nil
	}

	if prev.Type != next.Type {
		// Path changed from directory to file or vice-versa
		return collectFiles(next), nil, collectFiles(prev)
	}

	if prev.Type == NodeFile && next.Type == NodeFile {
		// Both are files, but hashes differ: modified
		return nil, []string{next.Path}, nil
	}

	// Recurse children
	for name, nextChild := range next.Children {
		prevChild := prev.Children[name]
		a, m, d := DiffTrees(prevChild, nextChild)
		added = append(added, a...)
		modified = append(modified, m...)
		deleted = append(deleted, d...)
	}

	for name, prevChild := range prev.Children {
		// the file "<name>" has been removed entirely
		if _, exists := next.Children[name]; !exists {
			deleted = append(deleted, collectFiles(prevChild)...)
		}
	}

	return
}

func collectFiles(node *MerkleNode) []string {
	if node == nil {
		return nil
	}
	if node.Type == NodeFile {
		return []string{node.Path}
	}
	var files []string
	for _, child := range node.Children {
		files = append(files, collectFiles(child)...)
	}
	return files
}

type IndexJob struct {
	relPath          string
	startLine        int
	endLine          int
	formattedContent string
	symbolName       string
}

type IndexResult struct {
	relPath          string
	startLine        int
	endLine          int
	formattedContent string
	symbolName       string
	embedding        []float32
	err              error
}

func purgePreviousTreeIfEmpty(absPath string, index *turboquant.Index) error {
	prevRoot, prevJSON, err := db.LoadMerkleTree(absPath)
	if err == nil && prevRoot != "" {
		var prevTree MerkleNode
		if json.Unmarshal([]byte(prevJSON), &prevTree) == nil {
			deleted := collectFiles(&prevTree)
			for _, path := range deleted {
				_ = db.DeleteFileMemories(absPath, path, index)
			}
		}
		_ = db.SaveMerkleTree(absPath, "", "{}")
	}
	return nil
}

func loadPreviousTree(absPath string) (*MerkleNode, error) {
	prevRoot, prevJSON, err := db.LoadMerkleTree(absPath)
	if err != nil {
		return nil, err
	}

	var prevTree *MerkleNode
	if prevRoot != "" && prevJSON != "" && prevJSON != "{}" {
		var pt MerkleNode
		if err := json.Unmarshal([]byte(prevJSON), &pt); err == nil {
			prevTree = &pt
		}
	}
	return prevTree, nil
}

func purgeStaleMemories(absPath string, files []string, index *turboquant.Index, action string) {
	for _, filePath := range files {
		relPath := filePath
		if filepath.IsAbs(relPath) {
			relPath, _ = filepath.Rel(absPath, filePath)
		}
		fmt.Printf("✗ %s stale memories for %s file: %s\n", action, strings.ToLower(action), relPath)
		if err := db.DeleteFileMemories(absPath, filePath, index); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to clear memories for %s file %s: %v\n", strings.ToLower(action), relPath, err)
		}
		_ = db.DeleteCallGraph(relPath)
	}
}

func formatContent(path string, lineStart, lineEnd int, content string) string {
	return fmt.Sprintf("File: %s (Lines: %d-%d)\nContent:\n%s", path, lineStart, lineEnd, content)
}

func prepareIndexerJobs(absPath string, files []string) []IndexJob {
	var jobs []IndexJob
	for _, filePath := range files {
		fullPath := filePath
		if !filepath.IsAbs(fullPath) {
			fullPath = filepath.Join(absPath, filePath)
		}

		relPath := filePath
		if filepath.IsAbs(relPath) {
			relPath, _ = filepath.Rel(absPath, filePath)
		}

		chunks, err := splitter.SplitFile(fullPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to split %s: %v\n", relPath, err)
			continue
		}

		for _, chunk := range chunks {
			jobs = append(jobs, IndexJob{
				relPath:          relPath,
				startLine:        chunk.StartLine,
				endLine:          chunk.EndLine,
				formattedContent: formatContent(relPath, chunk.StartLine, chunk.EndLine, chunk.Content),
				symbolName:       chunk.SymbolName,
			})
		}
	}
	return jobs
}

func generateEmbeddings(jobs []IndexJob) []IndexResult {
	results := make([]IndexResult, len(jobs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16) // Limit to 16 concurrent LiteLLM requests

	var completed int32
	total := len(jobs)

	for i, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, j IndexJob) {
			defer func() {
				<-sem
				wg.Done()
			}()

			embedding, err := llm.GetEmbedding(j.formattedContent, turboquant.DefaultDimension)
			results[idx] = IndexResult{
				relPath:          j.relPath,
				startLine:        j.startLine,
				endLine:          j.endLine,
				formattedContent: j.formattedContent,
				symbolName:       j.symbolName,
				embedding:        embedding,
				err:              err,
			}

			done := atomic.AddInt32(&completed, 1)
			// print carriage-return progress counter for responsive terminal status updates
			fmt.Printf("\r⚙ Progress: %d/%d chunks processed... (%s)", done, total, j.relPath)
		}(i, job)
	}

	wg.Wait()
	fmt.Println() // print newline after finishing progress ticker
	return results
}

func batchSaveMemoriesAsync(absPath string, results []IndexResult, index *turboquant.Index) int {
	var batchItems []db.MemoryBatchItem
	savedCount := 0
	for _, res := range results {
		if res.err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to generate embedding for %s: %v\n", res.relPath, res.err)
			continue
		}

		id := uuid.New().String()
		batchItems = append(batchItems, db.MemoryBatchItem{
			ID:         id,
			SymbolName: res.symbolName,
			CWD:        filepath.Join(absPath, res.relPath),
			LineStart:  res.startLine,
			LineEnd:    res.endLine,
			Embedding:  res.embedding,
		})
		savedCount++
	}

	if len(batchItems) > 0 {
		db.AsyncSaveWG.Add(1)
		go func(items []db.MemoryBatchItem) {
			defer db.AsyncSaveWG.Done()
			if err := db.SaveMemoriesBatch(items, index); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Background async index save failed: %v\n", err)
			}
		}(batchItems)
	}
	return savedCount
}

func syncASTCallGraphs(absPath string, files []string) {
	fmt.Printf("⚙ Indexing Call Graph nodes and dependencies...\n")
	for _, filePath := range files {
		fullPath := filePath
		if !filepath.IsAbs(fullPath) {
			fullPath = filepath.Join(absPath, filePath)
		}

		relPath := filePath
		if filepath.IsAbs(relPath) {
			relPath, _ = filepath.Rel(absPath, filePath)
		}

		nodes, edges, err := callgraph.ParseFile(fullPath, relPath)
		if err == nil {
			_ = db.SaveCallGraph(relPath, nodes, edges)
		}
	}
	fmt.Printf("✓ Call Graph incrementally synchronized successfully.\n")
}

// UpdateIndex implements the Merkle-tree based incremental indexing
func UpdateIndex(absPath string, index *turboquant.Index) (int, int, int, error) {
	if err := db.InitDatabase(); err != nil {
		return 0, 0, 0, fmt.Errorf("database init failed: %w", err)
	}

	// 1. Build the new Merkle tree from local codebase state
	newTree, err := BuildMerkleTree(absPath)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to build local Merkle tree: %w", err)
	}

	// If no indexable files found, clean up and exit
	if newTree == nil {
		_ = purgePreviousTreeIfEmpty(absPath, index)
		return 0, 0, 0, nil
	}

	// 2. Load the previously stored Merkle tree
	prevTree, err := loadPreviousTree(absPath)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to load previous Merkle tree: %w", err)
	}

	// 3. Diff trees
	added, modified, deleted := DiffTrees(prevTree, newTree)

	// 4. Delete stale memories of removed/modified files
	purgeStaleMemories(absPath, deleted, index, "Removing")
	purgeStaleMemories(absPath, modified, index, "Purging")

	// 5. Index added and modified files concurrently
	filesToProcess := append(added, modified...)
	if len(filesToProcess) > 0 {
		jobs := prepareIndexerJobs(absPath, filesToProcess)
		if len(jobs) > 0 {
			results := generateEmbeddings(jobs)
			savedCount := batchSaveMemoriesAsync(absPath, results, index)
			fmt.Printf("✓ Successfully indexed %d files (%d AST chunks)\n", len(filesToProcess), savedCount)

			// 6. Incrementally parse and update the AST Call/Dependency Graph inside DuckDB
			syncASTCallGraphs(absPath, filesToProcess)
		}
	}

	// 7. Save updated Merkle Tree state
	newTreeBytes, err := json.Marshal(newTree)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to serialize Merkle Tree to JSON: %w", err)
	}

	if err := db.SaveMerkleTree(absPath, newTree.Hash, string(newTreeBytes)); err != nil {
		return 0, 0, 0, fmt.Errorf("failed to save Merkle Tree state: %w", err)
	}

	return len(added), len(modified), len(deleted), nil
}
