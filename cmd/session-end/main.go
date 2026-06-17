package main

// ponytail: session-end hook reads transcript, summarizes it via LiteLLM API, and saves to DuckDB, completely robust

import (
	"encoding/json"
	"fmt"
	"os"

	"agent-mem/internal/db"
	"agent-mem/internal/llm"
	"agent-mem/internal/turboquant"

	"github.com/google/uuid"
)

type ShutdownPayload struct {
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
}

type ShutdownResponse struct {
	SystemMessage string `json:"systemMessage,omitempty"`
}

type SessionSummary struct {
	Category string `json:"category"`
	Content  string `json:"content"`
}

func main() {
	var payload ShutdownPayload
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		fmt.Println("{}")
		return
	}

	if payload.TranscriptPath == "" {
		fmt.Fprintf(os.Stderr, "No transcript path provided\n")
		fmt.Println("{}")
		return
	}

	transcriptBytes, err := os.ReadFile(payload.TranscriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read transcript: %v\n", err)
		fmt.Println("{}")
		return
	}

	transcriptStr := string(transcriptBytes)
	if len(transcriptStr) < 100 {
		// Conversation too brief to record memories
		fmt.Println("{}")
		return
	}

	cwd := payload.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	prompt := fmt.Sprintf(`
You are a session analyzer for a developer agent.
Analyze the following end-of-session transcript and extract any critical user preferences, architectural decisions, coding patterns established, or important facts learned during this session.

Strictly format your response as JSON matching this schema:
{
  "category": "personal" | "project" | "none",
  "content": "A highly concise bulleted list of persistent preferences, architectural decisions, or facts. Limit to 3-5 high-value bullet points."
}

Use "personal" if the lessons or preferences apply universally across all coding projects (e.g. "User prefers explicit TypeScript type-guards over type-casting").
Use "project" if the decisions or facts are specific to this repository or codebase (e.g. "Project uses DuckDB Node 'Neo' client for database connectivity").
Use "none" if nothing of permanent value was discussed, or if it was just a brief/trivial conversation.

Transcript:
"""
%s
"""
`, transcriptStr)

	summaryJSON, err := llm.GenerateJSON(prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate session summary via LiteLLM: %v\n", err)
		fmt.Println("{}")
		return
	}

	var summary SessionSummary
	if err := json.Unmarshal([]byte(summaryJSON), &summary); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse session summary JSON: %v. Raw text: %s\n", err, summaryJSON)
		fmt.Println("{}")
		return
	}

	if summary.Category == "none" || summary.Content == "" || len(summary.Content) < 10 {
		fmt.Println(JSONString(ShutdownResponse{
			SystemMessage: "No new memories to persist from this session.",
		}))
		return
	}

	// Save summary to database
	embedding, err := llm.GetEmbedding(summary.Content)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get embedding for summary: %v\n", err)
		fmt.Println("{}")
		return
	}

	// Initialize TurboQuant (3072 dimension, 4-bit, seed 42)
	tq, err := turboquant.NewTurboQuant(3072, 4, 42)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize TurboQuant: %v\n", err)
		fmt.Println("{}")
		return
	}

	id := uuid.New().String()
	if err := db.SaveMemory(id, summary.Content, "personal", cwd, embedding, tq); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save memory to DuckDB: %v\n", err)
		fmt.Println("{}")
		return
	}

	resp := ShutdownResponse{
		SystemMessage: fmt.Sprintf("Auto-captured and saved session summary under %s memory.", summary.Category),
	}
	fmt.Println(JSONString(resp))
}

func JSONString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
