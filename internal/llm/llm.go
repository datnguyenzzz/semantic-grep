package llm

// ponytail: use standard net/http for OpenAI-compatible LiteLLM endpoints to support any model with zero SDK dependencies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type EmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type EmbedResponse struct {
	Data []EmbedData `json:"data"`
}

type EmbedData struct {
	Embedding []float32 `json:"embedding"`
}

type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []ChatMessage   `json:"messages"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ResponseFormat struct {
	Type string `json:"type"`
}

type ChatResponse struct {
	Choices []ChatChoice `json:"choices"`
}

type ChatChoice struct {
	Message ChatMessage `json:"message"`
}

func getBaseURL() string {
	url := os.Getenv("LITELLM_BASE_URL")
	if url == "" {
		return "http://localhost:36253/v1"
	}
	return strings.TrimSuffix(url, "/")
}

func getAPIKey() string {
	return os.Getenv("LITELLM_API_KEY")
}

func getEmbeddingModel() string {
	model := os.Getenv("LITELLM_EMBEDDING_MODEL")
	if model == "" {
		return "gemini-embedding-001"
	}
	return model
}

func getChatModel() string {
	model := os.Getenv("LITELLM_CHAT_MODEL")
	if model == "" {
		return "gpt-5"
	}
	return model
}

func doRequest(method, endpoint string, reqBody any, respDest any) error {
	baseURL := getBaseURL()
	url := fmt.Sprintf("%s/%s", baseURL, strings.TrimPrefix(endpoint, "/"))

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(method, url, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	apiKey := getAPIKey()
	if apiKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("LiteLLM returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	return json.NewDecoder(resp.Body).Decode(respDest)
}

func GetEmbedding(text string) ([]float32, error) {
	reqBody := EmbedRequest{
		Model: getEmbeddingModel(),
		Input: text,
	}

	var embedResp EmbedResponse
	if err := doRequest("POST", "embeddings", reqBody, &embedResp); err != nil {
		return nil, err
	}

	if len(embedResp.Data) == 0 || len(embedResp.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("no embedding values returned by LiteLLM")
	}

	return embedResp.Data[0].Embedding, nil
}

func GenerateJSON(prompt string) (string, error) {
	reqBody := ChatRequest{
		Model: getChatModel(),
		Messages: []ChatMessage{
			{Role: "user", Content: prompt},
		},
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}

	var chatResp ChatResponse
	if err := doRequest("POST", "chat/completions", reqBody, &chatResp); err != nil {
		return "", err
	}

	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("no text content returned by LiteLLM")
	}

	return chatResp.Choices[0].Message.Content, nil
}
