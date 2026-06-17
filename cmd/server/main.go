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
	Query    string  `json:"query" jsonschema:"The semantic search query, question, or code/text pattern you want to locate (e.g., 'database connection configuration' or 'user style preferences')."`
	Category *string `json:"category,omitempty" jsonschema:"Optional category filter. Use 'personal' to search only user-level guidelines and preferences. Use 'project' to search only relevant code segments from registered/indexed codebases. Omit to search both categories concurrently."`
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
		Description: "Searches semantically across past user interactions, session summaries, personal preferences ('personal' category), or chunked codebase files ('project' category) in the user's registered codebases. For codebase search, matching code segments are loaded dynamically on demand from local files to preserve privacy. Use this tool when you need to recall user preferences or search for relevant implementation code, functions, patterns, or documentation within the indexed projects.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, any, error) {
		embedding, err := llm.GetEmbedding(args.Query)
		if err != nil {
			return nil, nil, err
		}

		category := ""
		if args.Category != nil {
			category = *args.Category
		}

		cwd, _ := os.Getwd()
		results, err := db.SearchMemories(embedding, category, cwd, 5, tq)
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
		Description: "Lists all local codebases currently registered, indexed, and available for semantic search on the user's system, including their absolute workspace paths, cryptographic Merkle root hashes, and the timestamp of their last indexing/sync. Use this tool to discover which directories have already been indexed and are searchable.",
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
			formatted += fmt.Sprintf("- **%s**\n  Path: `%s`\n  Merkle Root: `%s`\n  Last Updated: %s\n\n", name, c.CWD, c.RootHash, c.UpdatedAt.Format("2006-01-02 15:04:05"))
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
