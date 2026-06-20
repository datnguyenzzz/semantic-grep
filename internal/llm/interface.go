package llm

// ILLM represents the model-agnostic LLM interface for fetching embeddings and chat completions.
//
//go:generate mockery --name=ILLM
type ILLM interface {
	GetEmbedding(text string, dim int) ([]float32, error)
}
