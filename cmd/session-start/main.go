package main

// ponytail: session-start hook loads local database memories, ultra-fast, zero external API queries on startup

import (
	"encoding/json"
	"fmt"
	"os"

	"agent-mem/internal/db"
)

type StartupPayload struct {
	SessionID string `json:"session_id"`
}

type StartupResponse struct {
	SystemMessage      string                 `json:"systemMessage,omitempty"`
	HookSpecificOutput *StartupSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type StartupSpecificOutput struct {
	AdditionalContext string `json:"additionalContext"`
}

func main() {
	var payload StartupPayload
	_ = json.NewDecoder(os.Stdin).Decode(&payload)

	// Ensure table is initialized
	if err := db.InitDatabase(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init database: %v\n", err)
		fmt.Println("{}")
		return
	}

	// Fetch up to 20 most recent personal memories
	memories, err := db.GetRecentPersonalMemories(20)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to retrieve personal memories: %v\n", err)
		fmt.Println("{}")
		return
	}

	if len(memories) == 0 {
		fmt.Println("{}")
		return
	}

	var formatted string
	for i, row := range memories {
		formatted += fmt.Sprintf("[%d] Saved on %s:\n%s\n\n", i+1, row.CreatedAt.Format("2006-01-02 15:04:05"), row.Content)
	}

	additionalContext := fmt.Sprintf("### RETRIEVED PERSISTENT MEMORIES\nBelow are relevant personal preferences and guidelines retrieved from past sessions. Adhere to these guidelines, preferences, and facts:\n\n%s", formatted)

	resp := StartupResponse{
		SystemMessage: fmt.Sprintf("Loaded %d memories for persistent session context.", len(memories)),
		HookSpecificOutput: &StartupSpecificOutput{
			AdditionalContext: additionalContext,
		},
	}

	outBytes, _ := json.Marshal(resp)
	fmt.Println(string(outBytes))
}
