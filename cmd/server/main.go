package main

// keep mcp server simple, use official go-sdk, map arguments cleanly, and run periodic Merkle tree index updates in background

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

	"github.com/datnguyenzzz/semantic-grep/internal/callgraph"
	"github.com/datnguyenzzz/semantic-grep/internal/db"
	"github.com/datnguyenzzz/semantic-grep/internal/llm"
	"github.com/datnguyenzzz/semantic-grep/internal/merkle"
	"github.com/datnguyenzzz/semantic-grep/internal/turboquant"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SearchArgs struct {
	Query string  `json:"query" jsonschema:"The semantic search query, detailed question, or coding concept to locate. Always pass the complete user question or detailed context instead of single keywords to ensure high-fidelity semantic matching."`
	CWD   *string `json:"cwd,omitempty" jsonschema:"Optional absolute directory path to restrict search results to. If not provided, the search defaults to the current working directory where the server is running."`
}

type CallGraphArgs struct {
	SymbolName string  `json:"symbol_name" jsonschema:"The name of the target symbol (function, method, class, struct, resource) to explore (e.g. 'SaveMemory' or 'SearchMemories')."`
	CWD        *string `json:"cwd" jsonschema:"Mandatory absolute directory path of the codebase where the target symbol and its files reside."`
	Direction  *string `json:"direction,omitempty" jsonschema:"Optional direction to traverse. Supported values: 'caller', 'callee', or 'both'. Defaults to 'both'."`
	Depth      *int    `json:"depth,omitempty" jsonschema:"Optional maximum depth of call chain traversal. Defaults to 2."`
}

type MemoryResult struct {
	AbsolutePath string `json:"absolute_path"`
	SymbolName   string `json:"symbol_name"`
	StartLine    int    `json:"start_line"`
	EndLine      int    `json:"end_line"`
	Content      string `json:"content"`
}

type MemoryResponse struct {
	Results []MemoryResult `json:"results"`
}

type CallGraphMCPResponse struct {
	TargetNode *callgraph.Node       `json:"target_node"`
	Callers    []*callgraph.CallNode `json:"callers,omitempty"`
	Callees    []*callgraph.CallNode `json:"callees,omitempty"`
}

func startPeriodicIndexUpdate(index *turboquant.Index) {
	// periodically run incremental codebase index update in background, ONLY for registered codebases in DB
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
		Name:    "semantic-grep",
		Version: "1.0.0",
	}, nil)

	// 1. Register search_memory tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_memory",
		Description: "Use this tool FIRST to explore code conceptually, locate files, configurations, or relevant functions by natural human language.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The semantic search query, detailed question, or coding concept to locate. Always pass the complete user question or detailed context instead of single keywords to ensure high-fidelity semantic matching.",
				},
				"cwd": map[string]any{
					"type":        "string",
					"description": "Optional absolute directory path to restrict search results to. If not provided, the search defaults to the current working directory where the server is running.",
				},
			},
			"required": []string{"query"},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"results": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"absolute_path": map[string]any{
								"type":        "string",
								"description": "The absolute file path containing the matched code block.",
							},
							"symbol_name": map[string]any{
								"type":        "string",
								"description": "The name of the symbol/function declared in this block.",
							},
							"start_line": map[string]any{
								"type":        "integer",
								"description": "The 1-based start line of this block/function.",
							},
							"end_line": map[string]any{
								"type":        "integer",
								"description": "The 1-based end line of this block/function.",
							},
							"content": map[string]any{
								"type":        "string",
								"description": "The actual matching code segment lines read on demand from disk.",
							},
						},
						"required": []string{"absolute_path", "symbol_name", "start_line", "end_line", "content"},
					},
					"description": "A list of semantically matched codebase code chunks, sorted descending by similarity.",
				},
			},
			"required": []string{"results"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, any, error) {
		embedding, err := llm.GetEmbedding(args.Query, turboquant.DefaultDimension)
		if err != nil {
			return nil, nil, err
		}

		cwd := ""
		if args.CWD != nil && *args.CWD != "" {
			cwd = *args.CWD
		} else {
			if dir, err := os.Getwd(); err == nil {
				cwd = dir
			}
		}

		limit := min(intEnv("SEARCH_DEFAULT_LIMIT", 10), 50)
		results, err := db.SearchMemories(args.Query, embedding, cwd, limit, index)
		if err != nil {
			return nil, nil, err
		}

		// Initialize as an empty (non-nil) slice so an empty result marshals to [] and satisfies
		// the declared OutputSchema (a nil slice marshals to null and fails array validation).
		mcpResults := []MemoryResult{}
		for _, row := range results {
			mcpResults = append(mcpResults, MemoryResult{
				AbsolutePath: row.CWD,
				SymbolName:   row.SymbolName,
				StartLine:    row.LineStart,
				EndLine:      row.LineEnd,
				Content:      row.Content,
			})
		}

		mcpResponse := MemoryResponse{
			Results: mcpResults,
		}

		jsonBytes, err := json.MarshalIndent(mcpResponse, "", "  ")
		if err != nil {
			return nil, nil, err
		}

		// Return mcpResponse as the structured output: the SDK validates it against OutputSchema
		// and sets StructuredContent. Returning nil here omits structuredContent, which
		// schema-aware MCP clients reject ("did not return structured content").
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(jsonBytes)},
			},
		}, mcpResponse, nil
	})

	// 2. Register search_call_graph tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_call_graph",
		Description: "Traverses and builds the bidirectional call/dependency graph (callers, callees, or both) of a function or method. Use this to understand code execution flow, sequence, or dependencies up to a custom depth.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"symbol_name": map[string]any{
					"type":        "string",
					"description": "The name of the target symbol (function, method, class, struct, resource) to explore (e.g. 'SaveMemory' or 'SearchMemories').",
				},
				"cwd": map[string]any{
					"type":        "string",
					"description": "Mandatory absolute directory path of the codebase where the target symbol and its files reside.",
				},
				"direction": map[string]any{
					"type":        "string",
					"description": "Optional direction to traverse. Supported values: 'caller', 'callee', or 'both'. Defaults to 'both'.",
				},
				"depth": map[string]any{
					"type":        "integer",
					"description": "Optional maximum depth of call chain traversal. Defaults to 2.",
				},
			},
			"required": []string{"symbol_name", "cwd"},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target_node": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"symbol_name": map[string]any{
							"type":        "string",
							"description": "The name of the target block/function/symbol.",
						},
						"file_path": map[string]any{
							"type":        "string",
							"description": "The absolute file path containing the function declaration.",
						},
						"start_line": map[string]any{
							"type":        "integer",
							"description": "The 1-based start line of the function.",
						},
						"end_line": map[string]any{
							"type":        "integer",
							"description": "The 1-based end line of the function.",
						},
					},
					"required":    []string{"symbol_name", "file_path", "start_line", "end_line"},
					"description": "The metadata of the block/function that was explored.",
				},
				"callers": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":        "object",
						"description": "A node in the caller execution graph tree.",
					},
					"description": "A tree representation of functions/blocks that call or depend on the target node.",
				},
				"callees": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":        "object",
						"description": "A node in the callee execution graph tree.",
					},
					"description": "A tree representation of functions/blocks called or referenced by the target node.",
				},
			},
			"required": []string{"target_node"},
		},
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

		targetSymbol := args.SymbolName
		if targetSymbol == "" {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "Error: Mandatory 'symbol_name' parameter is missing."},
				},
				IsError: true,
			}, nil, nil
		}

		// Fast database-only lookup (since on-the-fly fallback is removed)
		targetNode, err := db.GetCallNode(targetSymbol)
		if err != nil || targetNode == nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: fmt.Sprintf("Error: Symbol %q not found in the indexed codebase. Please ensure the symbol name is spelled correctly and that the file containing it is indexed inside CWD %q.", targetSymbol, cwd)},
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
			TargetNode: report.TargetNode,
			Callers:    report.Callers,
			Callees:    report.Callees,
		}

		jsonBytes, err := json.MarshalIndent(mcpReport, "", "  ")
		if err != nil {
			return nil, nil, err
		}

		// Return mcpReport as structured output so the SDK populates StructuredContent (validated
		// against OutputSchema); a nil here would omit it and schema-aware clients reject the call.
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(jsonBytes)},
			},
		}, mcpReport, nil
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
