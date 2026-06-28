package llm

// use standard net/http for OpenAI-compatible LiteLLM endpoints to support any model with zero SDK dependencies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
)

type EmbedRequest struct {
	Model      string `json:"model"`
	Input      string `json:"input"`
	Dimensions *int   `json:"dimensions,omitempty"`
}

type EmbedResponse struct {
	Data []EmbedData `json:"data"`
}

type EmbedData struct {
	Embedding []float32 `json:"embedding"`
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

func doRequest(method, endpoint string, reqBody any, respDest any) error {
	baseURL := getBaseURL()
	url := fmt.Sprintf("%s/%s", baseURL, strings.TrimPrefix(endpoint, "/"))

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 10 * time.Second}

	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.InitialInterval = 50 * time.Millisecond
	expBackoff.MaxInterval = 2 * time.Second
	expBackoff.MaxElapsedTime = 5 * time.Second

	bo := backoff.WithMaxRetries(expBackoff, 3)

	operation := func() error {
		req, err := http.NewRequest(method, url, bytes.NewBuffer(jsonBytes))
		if err != nil {
			return backoff.Permanent(err) // Non-retriable request setup error
		}

		req.Header.Set("Content-Type", "application/json")
		apiKey := getAPIKey()
		if apiKey != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
		}

		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return json.NewDecoder(resp.Body).Decode(respDest)
		}

		// Read error response body
		respBytes, _ := io.ReadAll(resp.Body)
		errResp := fmt.Errorf("LiteLLM returned status %d: %s", resp.StatusCode, string(respBytes))

		// Only retry on retriable status codes: 429 (Rate Limit) or 5xx (Server Errors)
		if resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode >= 500 && resp.StatusCode <= 599) {
			return errResp
		}

		return backoff.Permanent(errResp)
	}

	return backoff.Retry(operation, bo)
}

// DefaultClient is the default global ILLM instance (implementing LiteLLM)
var DefaultClient ILLM = &LiteLLM{}

// Package-level delegations to maintain 100% backward-compatibility with all existing callers
func GetEmbedding(text string, dim int) ([]float32, error) {
	return DefaultClient.GetEmbedding(text, dim)
}

// LiteLLM implements the ILLM interface using HTTP REST client calls
type LiteLLM struct{}

func (l *LiteLLM) GetEmbedding(text string, dim int) ([]float32, error) {
	reqBody := EmbedRequest{
		Model:      getEmbeddingModel(),
		Input:      text,
		Dimensions: &dim,
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
