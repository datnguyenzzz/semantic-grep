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

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SearchArgs struct {
	Query    string  `json:"query" jsonschema:"description=The search query or phrase describing what you want to recall."`
	Category *string `json:"category,omitempty" jsonschema:"description=Filter search by 'personal' (user preferences) or 'project' (repository notes).,enum=personal,enum=project"`
}

type AddArgs struct {
	Content  string `json:"content" jsonschema:"description=The actual knowledge, preference, decision, or fact to save."`
	Category string `json:"category" jsonschema:"description=Category of the memory: 'personal' for user preferences/learnings, 'project' for codebase architecture/conventions.,enum=personal,enum=project"`
}

func startPeriodicIndexUpdate(tq *turboquant.TurboQuant) {
	// ponytail: periodically run incremental codebase index update in background every 10 minutes
	ticker := time.NewTicker(10 * time.Minute)
	go func() {
		for range ticker.C {
			cwd, err := os.Getwd()
			if err != nil {
				continue
			}
			_, _, _, _ = merkle.UpdateIndex(cwd, tq)
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
		Description: "Searches through past personal memories, session summaries, or project knowledge using semantic search.",
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

	// 2. Register add_memory tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_memory",
		Description: "Saves a new personal memory (preference, decision, user fact) or project memory (guidelines, conventions) to persistent storage.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args AddArgs) (*mcp.CallToolResult, any, error) {
		embedding, err := llm.GetEmbedding(args.Content)
		if err != nil {
			return nil, nil, err
		}

		id := uuid.New().String()
		cwd, _ := os.Getwd()

		if err := db.SaveMemory(id, args.Content, args.Category, cwd, embedding, tq); err != nil {
			return nil, nil, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Successfully saved %s memory: \"%s\"", args.Category, args.Content)},
			},
		}, nil, nil
	})

	// 3. Register update_index tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_index",
		Description: "Manually triggers an incremental update of the codebase's Merkle tree index, re-indexing only added/modified files and purging deleted ones.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get current working directory: %w", err)
		}

		added, modified, deleted, err := merkle.UpdateIndex(cwd, tq)
		if err != nil {
			return nil, nil, err
		}

		msg := fmt.Sprintf("Codebase index updated: %d files added, %d modified, %d deleted.", added, modified, deleted)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: msg},
			},
		}, nil, nil
	})

	// 4. Register list_codebases tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_codebases",
		Description: "Lists all indexed codebases on the user's system, including their paths, Merkle root hashes, and when they were last updated.",
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
