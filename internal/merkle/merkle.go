package merkle

// ponytail: Merkle tree-based incremental indexer to prune unchanged code subtrees, avoiding redundant chunking & LLM embedding generation

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

	"agent-mem/internal/db"
	"agent-mem/internal/llm"
	"agent-mem/internal/splitter"
	"agent-mem/internal/turboquant"

	"github.com/google/uuid"
)

type NodeType string

const (
	NodeFile      NodeType = "file"
	NodeDirectory NodeType = "directory"
)

type MerkleNode struct {
	Type     NodeType               `json:"type"`
	Name     string                 `json:"name"`
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
	// ponytail: support Go, Terraform, YAML, and Markdown files
	return ext == ".go" || ext == ".tf" || ext == ".yaml" || ext == ".yml" || ext == ".md"
}

// BuildMerkleTree scans the filesystem recursively and constructs the Merkle Tree of indexable files
func BuildMerkleTree(absPath, relPath string) (*MerkleNode, error) {
	fullPath := filepath.Join(absPath, relPath)
	fi, err := os.Stat(fullPath)
	if err != nil {
		return nil, err
	}

	if !fi.IsDir() {
		if !isIndexable(fi.Name()) {
			return nil, nil
		}
		// Cap at 200KB for files to parse (consistent with main indexer)
		if fi.Size() > 200*1024 {
			return nil, nil
		}

		// Split file into AST-based chunks to determine the chunk structure & hashes
		chunks, err := splitter.SplitFile(fullPath)
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
			Path: relPath,
			Hash: fileHash,
		}, nil
	}

	// It's a directory, read its children
	entries, err := os.ReadDir(fullPath)
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

		childRelPath := name
		if relPath != "" {
			childRelPath = filepath.Join(relPath, name)
		}

		childNode, err := BuildMerkleTree(absPath, childRelPath)
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
		Path:     relPath,
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
		// Subtree is identical! Prune recursion.
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

// UpdateIndex implements the Merkle-tree based incremental indexing
func UpdateIndex(absPath string, tq *turboquant.TurboQuant) (int, int, int, error) {
	if err := db.InitDatabase(); err != nil {
		return 0, 0, 0, fmt.Errorf("database init failed: %w", err)
	}

	// 1. Build the new Merkle tree from local codebase state
	newTree, err := BuildMerkleTree(absPath, "")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to build local Merkle tree: %w", err)
	}

	// If no indexable files found, clean up and exit
	if newTree == nil {
		// Retrieve previous tree to purge if it existed
		prevRoot, prevJSON, err := db.LoadMerkleTree(absPath)
		if err == nil && prevRoot != "" {
			var prevTree MerkleNode
			if json.Unmarshal([]byte(prevJSON), &prevTree) == nil {
				deleted := collectFiles(&prevTree)
				for _, path := range deleted {
					_ = db.DeleteFileMemories(absPath, path)
				}
			}
			_ = db.SaveMerkleTree(absPath, "", "{}")
		}
		return 0, 0, 0, nil
	}

	// 2. Load the previously stored Merkle tree
	prevRoot, prevJSON, err := db.LoadMerkleTree(absPath)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to load previous Merkle tree: %w", err)
	}

	var prevTree *MerkleNode
	if prevRoot != "" && prevJSON != "" && prevJSON != "{}" {
		var pt MerkleNode
		if err := json.Unmarshal([]byte(prevJSON), &pt); err == nil {
			prevTree = &pt
		}
	}

	// 3. Diff trees
	added, modified, deleted := DiffTrees(prevTree, newTree)

	// 4. Delete stale memories of removed files
	for _, relPath := range deleted {
		fmt.Printf("✗ Removing stale memories for deleted file: %s\n", relPath)
		if err := db.DeleteFileMemories(absPath, relPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to clear memories for deleted file %s: %v\n", relPath, err)
		}
	}

	// 5. Delete stale memories of modified files
	for _, relPath := range modified {
		fmt.Printf("⚙ Purging stale memories for modified file: %s\n", relPath)
		if err := db.DeleteFileMemories(absPath, relPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to clear memories for modified file %s: %v\n", relPath, err)
		}
	}

	// 6. Index added and modified files concurrently
	filesToProcess := append(added, modified...)
	type IndexJob struct {
		relPath          string
		startLine        int
		endLine          int
		formattedContent string
	}

	type IndexResult struct {
		relPath          string
		startLine        int
		endLine          int
		formattedContent string
		embedding        []float32
		err              error
	}

	var jobs []IndexJob
	for _, relPath := range filesToProcess {
		fullPath := filepath.Join(absPath, relPath)
		chunks, err := splitter.SplitFile(fullPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to split %s: %v\n", relPath, err)
			continue
		}

		for _, chunk := range chunks {
			formattedContent := fmt.Sprintf("File: %s (Lines: %d-%d)\nContent:\n%s", relPath, chunk.StartLine, chunk.EndLine, chunk.Content)
			jobs = append(jobs, IndexJob{
				relPath:          relPath,
				startLine:        chunk.StartLine,
				endLine:          chunk.EndLine,
				formattedContent: formattedContent,
			})
		}
	}

	if len(jobs) > 0 {
		fmt.Printf("⚙ Generating embeddings concurrently for %d AST chunks...\n", len(jobs))

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

				embedding, err := llm.GetEmbedding(j.formattedContent)
				results[idx] = IndexResult{
					relPath:          j.relPath,
					startLine:        j.startLine,
					endLine:          j.endLine,
					formattedContent: j.formattedContent,
					embedding:        embedding,
					err:              err,
				}

				done := atomic.AddInt32(&completed, 1)
				// ponytail: print carriage-return progress counter for responsive terminal status updates
				fmt.Printf("\r⚙ Progress: %d/%d chunks processed... (%s)", done, total, j.relPath)
			}(i, job)
		}

		wg.Wait()
		fmt.Println() // print newline after finishing progress ticker

		// Save results sequentially to DuckDB to avoid lock contention
		savedCount := 0
		for _, res := range results {
			if res.err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to generate embedding for %s: %v\n", res.relPath, res.err)
				continue
			}

			// ponytail: privacy preservation - save ONLY the metadata header to DuckDB instead of raw code chunks!
			metadataHeader := fmt.Sprintf("File: %s (Lines: %d-%d)", res.relPath, res.startLine, res.endLine)
			id := uuid.New().String()
			if err := db.SaveMemory(id, metadataHeader, "project", absPath, res.embedding, tq); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to save chunk to memory store: %v\n", err)
				continue
			}
			savedCount++
		}

		fmt.Printf("✓ Successfully indexed %d files (%d AST chunks)\n", len(filesToProcess), savedCount)
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
