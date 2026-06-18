package main

// ponytail: keep mcp server simple, use official go-sdk, map arguments cleanly, and run periodic Merkle tree index updates in background

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"agent-mem/internal/db"
	"agent-mem/internal/llm"
	"agent-mem/internal/merkle"
	"agent-mem/internal/turboquant"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SearchArgs struct {
	Query string `json:"query" jsonschema:"The semantic search query, detailed question, or coding concept to locate. Always pass the complete user question or detailed context instead of single keywords to ensure high-fidelity semantic matching."`
}

func startPeriodicIndexUpdate(index *turboquant.Index) {
	// ponytail: periodically run incremental codebase index update in background every 10 minutes, ONLY for registered codebases in DB
	ticker := time.NewTicker(10 * time.Minute)
	go func() {
		for range ticker.C {
			codebases, err := db.ListCodebases()
			if err != nil {
				continue
			}
			updated := false
			for _, c := range codebases {
				added, modified, deleted, err := merkle.UpdateIndex(c.CWD, index)
				if err == nil && (added > 0 || modified > 0 || deleted > 0) {
					updated = true
				}
			}
			if updated {
				log.Printf("Background codebase updates detected, compacting and saving TurboQuant index to disk...")
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
			}
		}
	}()
}

func main() {
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
		log.Printf("Received signal %v, saving TurboQuant index to disk...", sig)
		if err := index.Save(); err != nil {
			log.Printf("Error saving TurboQuant index: %v", err)
		} else {
			log.Printf("TurboQuant index saved successfully.")
		}
		os.Exit(0)
	}()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "agent-mem-server",
		Version: "1.0.0",
	}, nil)

	// 1. Register search_memory tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_memory",
		Description: "CRITICAL WORKFLOW DIRECTIVE: You MUST use this tool FIRST before calling any other tool, listing directories, reading files, or executing shell commands to search for files, folders, local structures, functions, or configurations in this codebase. This tool searches semantically across segments of indexed codebase files in the current workspace. Always call this tool first with your complete, detailed natural language query or question.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, any, error) {
		embedding, err := llm.GetEmbedding(args.Query)
		if err != nil {
			return nil, nil, err
		}

		cwd, _ := os.Getwd()
		results, err := db.SearchMemories(embedding, cwd, 10, index)
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

		var formatted strings.Builder
		for _, row := range results {
			codebaseName := filepath.Base(row.CWD)
			fmt.Fprintf(&formatted, "[Codebase: %s] [Path: %s] (Similarity: %.1f%%)\n%s\n\n---\n\n", codebaseName, row.CWD, row.Similarity*100, row.Content)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: formatted.String()},
			},
		}, nil, nil
	})

	// Start periodic background updates
	startPeriodicIndexUpdate(index)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("MCP Server failed to run: %v", err)
	}
}
