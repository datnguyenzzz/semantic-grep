package main

// ponytail: keep mcp server simple, use official go-sdk, map arguments cleanly, and run periodic Merkle tree index updates in background

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"agent-mem/internal/db"
	"agent-mem/internal/llm"
	"agent-mem/internal/merkle"
	"agent-mem/internal/turboquant"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SearchArgs struct {
	Query    string  `json:"query" jsonschema:"The full semantic search query, detailed question, or specific coding pattern you want to locate. CRITICAL: Pass the complete user question, detailed coding concept, or natural language query (e.g. 'how long is the trace buffered time in otelcol' or 'database retry logic') instead of single keywords, symbols, or repository names to ensure high-fidelity semantic match."`
	Category *string `json:"category,omitempty" jsonschema:"Optional category filter. Use 'personal' to search only user-level guidelines and preferences. Use 'project' to search only relevant code segments from registered/indexed codebases. Omit to search both categories concurrently."`
	CWD      *string `json:"cwd,omitempty" jsonschema:"Optional absolute codebase path of the repository to search. If omitted, search_memory defaults to searching the current working directory of the workspace."`
}

func startPeriodicIndexUpdate(tq *turboquant.TurboQuant) {
	// ponytail: periodically run incremental codebase index update in background every 10 minutes, ONLY for registered codebases in DB
	ticker := time.NewTicker(10 * time.Minute)
	go func() {
		for range ticker.C {
			codebases, err := db.ListCodebases()
			if err != nil {
				continue
			}
			for _, c := range codebases {
				_, _, _, _ = merkle.UpdateIndex(c.CWD, tq)
			}
		}
	}()
}

func main() {
	// Initialize database schema on startup
	if err := db.InitDatabase(); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Initialize TurboQuant once on startup (3072 dimension, 4-bit, seed 42)
	tq, err := turboquant.NewTurboQuant(3072, 4, 42)
	if err != nil {
		log.Fatalf("Failed to initialize TurboQuant: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "agent-mem-server",
		Version: "1.0.0",
	}, nil)

	// 1. Register search_memory tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_memory",
		Description: "CRITICAL WORKFLOW:\n1. If you are not sure which codebase or repository to search, you MUST call 'list_codebases' first to return and discover all registered, indexed codebases on the system.\n2. Once the codebase is specified or chosen, use its absolute path as the 'cwd' argument (which is non-mandatory) combined with the complete, detailed user question or context as the 'query' argument to call 'search_memory' (e.g., Query: 'how long is the trace buffered time in otelcol-tail-sampling', CWD: '/Users/username/...').\n3. DO NOT read local files directly until you have run a semantic search using this tool. Only read local files manually if this tool returns no results, or if you need to perform a precise surgical edit of a specific file found during search.\n4. Searches semantically across past user preferences ('personal' category) and Go, Terraform, and YAML files in registered codebases ('project' category), loading matching code segments dynamically from disk on demand to preserve privacy.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, any, error) {
		embedding, err := llm.GetEmbedding(args.Query)
		if err != nil {
			return nil, nil, err
		}

		category := ""
		if args.Category != nil {
			category = *args.Category
		}

		cwd := ""
		if args.CWD != nil && *args.CWD != "" {
			cwd = *args.CWD
		} else {
			cwd, _ = os.Getwd()
		}
		results, err := db.SearchMemories(embedding, category, cwd, 10, tq)
		if err != nil {
			return nil, nil, err
		}

		if len(results) == 0 {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "No matching memories found."},
				},
			}, nil, nil
		}

		var formatted string
		for _, row := range results {
			codebaseName := filepath.Base(row.CWD)
			formatted += fmt.Sprintf("[Codebase: %s] [Path: %s] [Category: %s] (Similarity: %.1f%%)\n%s\n\n---\n\n", codebaseName, row.CWD, row.Category, row.Similarity*100, row.Content)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: formatted},
			},
		}, nil, nil
	})

	// 4. Register list_codebases tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_codebases",
		Description: "Lists all local codebases currently registered, indexed, and available for semantic search on the user's system, including their absolute workspace paths, and the timestamp of their last indexing/sync. Use this tool to discover which directories have already been indexed and are searchable.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		codebases, err := db.ListCodebases()
		if err != nil {
			return nil, nil, err
		}

		if len(codebases) == 0 {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "No indexed codebases found. Use /index [path] to index a codebase."},
				},
			}, nil, nil
		}

		var formatted string
		for _, c := range codebases {
			name := filepath.Base(c.CWD)
			formatted += fmt.Sprintf("- **%s**\n  Path: `%s`\n  Last Updated: %s\n\n", name, c.CWD, c.UpdatedAt.Format("2006-01-02 15:04:05"))
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "### INDEXED CODEBASES PORTFOLIO\n\n" + formatted},
			},
		}, nil, nil
	})

	// Start periodic background updates
	startPeriodicIndexUpdate(tq)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("MCP Server failed to run: %v", err)
	}
}
