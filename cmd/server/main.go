package main

// ponytail: keep mcp server simple, use official go-sdk, map arguments cleanly, and run periodic Merkle tree index updates in background

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/datnguyenzzz/agent-context/internal/callgraph"
	"github.com/datnguyenzzz/agent-context/internal/db"
	"github.com/datnguyenzzz/agent-context/internal/llm"
	"github.com/datnguyenzzz/agent-context/internal/merkle"
	"github.com/datnguyenzzz/agent-context/internal/turboquant"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SearchArgs struct {
	Query string  `json:"query" jsonschema:"The semantic search query, detailed question, or coding concept to locate. Always pass the complete user question or detailed context instead of single keywords to ensure high-fidelity semantic matching."`
	CWD   *string `json:"cwd,omitempty" jsonschema:"Optional directory to restrict search results to. If not provided, the search is global across all indexed codebases."`
}

type CallGraphArgs struct {
	FunctionName string  `json:"function_name" jsonschema:"The name of the target Go function/method to explore (e.g. 'SaveMemory' or 'SearchMemories')."`
	CWD          *string `json:"cwd" jsonschema:"Mandatory absolute directory path of the codebase where the target function and its files reside."`
	Direction    *string `json:"direction,omitempty" jsonschema:"Optional direction to traverse. Supported values: 'caller', 'callee', or 'both'. Defaults to 'both'."`
	Depth        *int    `json:"depth,omitempty" jsonschema:"Optional maximum depth of call chain traversal. Defaults to 2."`
}

type MemoryResult struct {
	Codebase   string  `json:"codebase"`
	Path       string  `json:"path"`
	Similarity float64 `json:"similarity_percentage"`
	Content    string  `json:"content"`
}

type MemoryResponse struct {
	SchemaDescription map[string]string `json:"schema_description"`
	Results           []MemoryResult    `json:"results"`
}

var MemorySchemaDescription = map[string]string{
	"schema_description":    "Explanatory key-value definitions for all properties inside this search_memory response structure.",
	"results":               "A list of semantically matched codebase code chunks, sorted descending by similarity.",
	"codebase":              "The base directory name of the local workspace codebase.",
	"path":                  "The absolute or relative path of the file containing the matched segment.",
	"similarity_percentage": "The similarity score percentage between the search query and the segment (higher means closer match).",
	"content":               "The actual matching code segment lines read on demand from disk.",
}

type CallGraphMCPResponse struct {
	SchemaDescription map[string]string     `json:"schema_description"`
	TargetNode        *callgraph.Node       `json:"target_node"`
	Callers           []*callgraph.CallNode `json:"callers,omitempty"`
	Callees           []*callgraph.CallNode `json:"callees,omitempty"`
}

var CallGraphSchemaDescription = map[string]string{
	"schema_description": "Explanatory key-value definitions for all properties inside this response structure.",
	"target_node":        "The metadata of the block/function that was explored (Name, FilePath, StartLine, EndLine).",
	"callers":            "A tree representation of functions/blocks that call or depend on the target node.",
	"callees":            "A tree representation of functions/blocks called or referenced by the target node.",
	"children":           "Nested dependencies (e.g. callers of a caller, or callees of a callee) tracing the execution tree recursively.",
}

func startPeriodicIndexUpdate(index *turboquant.Index) {
	// ponytail: periodically run incremental codebase index update in background, ONLY for registered codebases in DB
	syncInterval := 10 * time.Minute
	if val := os.Getenv("BACKGROUND_SYNC_INTERVAL"); val != "" {
		if dur, err := time.ParseDuration(val); err == nil {
			syncInterval = dur
		}
	}
	ticker := time.NewTicker(syncInterval)
	go func() {
		// 1. Run an immediate initial indexing sweep on startup
		runIndexSweep(index, true)

		// 2. Fall into periodic background update ticks
		for range ticker.C {
			runIndexSweep(index, false)
		}
	}()
}

func runIndexSweep(index *turboquant.Index, isStartup bool) {
	codebases, err := db.ListCodebases()
	if err != nil {
		return
	}
	if len(codebases) == 0 {
		return
	}

	if isStartup {
		log.Println("Starting initial codebase indexing sweep on server startup...")
	}

	updated := false
	for _, c := range codebases {
		added, modified, deleted, err := merkle.UpdateIndex(c.CWD, index)
		if err == nil && (added > 0 || modified > 0 || deleted > 0) {
			updated = true
			if isStartup {
				log.Printf("✓ Codebase indexed successfully: %s (Added: %d, Modified: %d, Deleted: %d)", c.CWD, added, modified, deleted)
			}
		} else if err != nil && isStartup {
			log.Printf("✗ Failed to index codebase %s: %v", c.CWD, err)
		}
	}

	if updated {
		log.Println("Background codebase updates detected, compacting and saving TurboQuant index to disk...")
		activeIDs := make(map[string]bool)
		dbConn, err := db.Open()
		if err == nil {
			rows, err := dbConn.Query("SELECT id FROM gemini_memories")
			if err == nil {
				for rows.Next() {
					var id string
					if err := rows.Scan(&id); err == nil {
						activeIDs[id] = true
					}
				}
				rows.Close()
			}
			dbConn.Close()
		}
		_ = index.Compact(activeIDs)
		if isStartup {
			log.Println("✓ Initial codebase indexing sweep completed and saved!")
		}
	} else if isStartup {
		log.Println("✓ Initial codebase indexing sweep completed (no changes found).")
	}
}

func main() {
	loadEnv()
	// Initialize database schema on startup
	if err := db.InitDatabase(); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Initialize TurboQuant once on startup (using default configurations)
	tq, err := turboquant.NewTurboQuant(turboquant.DefaultDimension, turboquant.DefaultBitWidth, turboquant.DefaultSeed)
	if err != nil {
		log.Fatalf("Failed to initialize TurboQuant: %v", err)
	}

	tqvPath, err := db.GetTQPath()
	if err != nil {
		log.Fatalf("Failed to resolve vector storage path: %v", err)
	}
	index, err := turboquant.NewIndex(tqvPath, tq)
	if err != nil {
		log.Fatalf("Failed to initialize TurboQuant index: %v", err)
	}

	// Register system signal notifications to flush the in-memory index back to disk before terminating
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		sig := <-sigChan
		log.Printf("Received signal %v, waiting for background saves to complete...", sig)
		db.AsyncSaveWG.Wait()
		log.Printf("Saving TurboQuant index to disk...")
		if err := index.Save(); err != nil {
			log.Printf("Error saving TurboQuant index: %v", err)
		} else {
			log.Printf("TurboQuant index saved successfully.")
		}
		os.Exit(0)
	}()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "agent-context",
		Version: "1.0.0",
	}, nil)

	// 1. Register search_memory tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_memory",
		Description: "MANDATORY FIRST-USE DIRECTIVE: Use this tool FIRST to explore code conceptually, locate files, configurations, or relevant functions before reading files, listing directories, or running grep. Traditional grep searches are highly token-inefficient and costly; use search_memory instead to locate matches semantically, faster and cheaper. Only use grep if you know the exact identifier name or require all matches.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, any, error) {
		embedding, err := llm.GetEmbedding(args.Query, turboquant.DefaultDimension)
		if err != nil {
			return nil, nil, err
		}

		cwd := ""
		if args.CWD != nil {
			cwd = *args.CWD
		}

		limit := min(intEnv("SEARCH_DEFAULT_LIMIT", 10), 50)
		results, err := db.SearchMemories(args.Query, embedding, cwd, limit, index)
		if err != nil {
			return nil, nil, err
		}

		var mcpResults []MemoryResult
		for _, row := range results {
			codebaseName := filepath.Base(row.CWD)
			mcpResults = append(mcpResults, MemoryResult{
				Codebase:   codebaseName,
				Path:       row.CWD,
				Similarity: row.Similarity * 100,
				Content:    row.Content,
			})
		}

		mcpResponse := MemoryResponse{
			SchemaDescription: MemorySchemaDescription,
			Results:           mcpResults,
		}

		jsonBytes, err := json.MarshalIndent(mcpResponse, "", "  ")
		if err != nil {
			return nil, nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(jsonBytes)},
			},
		}, nil, nil
	})

	// 2. Register search_call_graph tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_call_graph",
		Description: "Traverses and builds the bidirectional call/dependency graph (callers, callees, or both) of a function or method. Use this to understand code execution flow, sequence, or dependencies up to a custom depth. Do not use this for semantic keyword search; locate function names first via search_memory, then trace their call graph with this tool.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args CallGraphArgs) (*mcp.CallToolResult, any, error) {
		if args.CWD == nil || *args.CWD == "" {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "Error: 'cwd' is a mandatory argument for search_call_graph. Please provide the absolute path to the codebase directory where the target function and file reside."},
				},
				IsError: true,
			}, nil, nil
		}
		cwd := *args.CWD

		// Verify that the codebase is indexed/registered in DuckDB
		codebases, err := db.ListCodebases()
		if err != nil {
			return nil, nil, err
		}

		isIndexed := false
		for _, cb := range codebases {
			if cwd == cb.CWD || strings.HasPrefix(cwd, cb.CWD+string(filepath.Separator)) {
				isIndexed = true
				break
			}
		}

		if !isIndexed {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("Error: CWD directory %q is not indexed. Please index this codebase first using 'make index DIR=%s'.", cwd, cwd)},
				},
				IsError: true,
			}, nil, nil
		}

		direction := "both"
		if args.Direction != nil && *args.Direction != "" {
			direction = *args.Direction
		}

		depth := intEnv("CALL_GRAPH_DEFAULT_DEPTH", 2)
		if args.Depth != nil {
			depth = *args.Depth
		}

		// Fast database-only lookup (since on-the-fly fallback is removed)
		targetNode, err := db.GetCallNode(args.FunctionName)
		if err != nil || targetNode == nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("Error: Function %q not found in the indexed codebase. Please ensure the function name is spelled correctly and that the file containing it is indexed inside CWD %q.", args.FunctionName, cwd)},
				},
				IsError: true,
			}, nil, nil
		}

		report, err := callgraph.GenerateOnDemandTreeReport(targetNode, direction, depth, db.GetCallees, db.GetCallers)
		if err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("Error generating call graph tree: %v", err)},
				},
				IsError: true,
			}, nil, nil
		}

		mcpReport := CallGraphMCPResponse{
			SchemaDescription: CallGraphSchemaDescription,
			TargetNode:        report.TargetNode,
			Callers:           report.Callers,
			Callees:           report.Callees,
		}

		jsonBytes, err := json.MarshalIndent(mcpReport, "", "  ")
		if err != nil {
			return nil, nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(jsonBytes)},
			},
		}, nil, nil
	})

	// Start periodic background updates
	startPeriodicIndexUpdate(index)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("MCP Server failed to run: %v", err)
	}
}

func intEnv(key string, fallback int) int {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	var i int
	if _, err := fmt.Sscanf(val, "%d", &i); err != nil {
		return fallback
	}
	return i
}

func loadEnv() {
	if home, err := os.UserHomeDir(); err == nil {
		envPath := filepath.Join(home, ".agent-mem.env")
		if content, err := os.ReadFile(envPath); err == nil {
			for line := range strings.SplitSeq(string(content), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					key := strings.TrimSpace(parts[0])
					val := strings.TrimSpace(parts[1])
					os.Setenv(key, val)
				}
			}
		}
	}
}
