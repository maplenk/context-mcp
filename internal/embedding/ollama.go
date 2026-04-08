package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OllamaEmbedder generates embeddings via the Ollama HTTP API.
// Implements the Embedder interface using the /api/embed endpoint.
type OllamaEmbedder struct {
	endpoint string
	model    string
	dim      int
	client   *http.Client
}

// ollamaEmbedRequest is the request body for POST /api/embed.
type ollamaEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// ollamaEmbedResponse is the response from POST /api/embed.
type ollamaEmbedResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
}

// NewOllamaEmbedder creates an embedder that calls the Ollama API.
// It verifies connectivity by pinging the /api/tags endpoint.
func NewOllamaEmbedder(endpoint, model string, dim int) (*OllamaEmbedder, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("embedding dimension must be positive, got %d", dim)
	}

	endpoint = strings.TrimRight(endpoint, "/")

	client := &http.Client{Timeout: 30 * time.Second}

	// Verify Ollama is reachable
	resp, err := client.Get(endpoint + "/api/tags")
	if err != nil {
		return nil, fmt.Errorf("connecting to Ollama at %s: %w", endpoint, err)
	}
	if err := discardAndClose("Ollama tags", resp); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Ollama at %s returned status %d", endpoint, resp.StatusCode)
	}

	return &OllamaEmbedder{
		endpoint: endpoint,
		model:    model,
		dim:      dim,
		client:   client,
	}, nil
}

// Embed generates an embedding vector for a single text.
func (e *OllamaEmbedder) Embed(text string) (_ []float32, err error) {
	reqBody, err := json.Marshal(ollamaEmbedRequest{
		Model: e.model,
		Input: text,
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling Ollama request: %w", err)
	}

	resp, err := e.client.Post(e.endpoint+"/api/embed", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("Ollama embed request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("closing Ollama embed response body: %w", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("Ollama returned status %d: %s", resp.StatusCode, string(body))
	}

	var result ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding Ollama response: %w", err)
	}

	if len(result.Embeddings) == 0 {
		return nil, fmt.Errorf("Ollama returned no embeddings")
	}

	raw := result.Embeddings[0]
	if len(raw) < e.dim {
		return nil, fmt.Errorf("Ollama embedding dimension %d < requested %d", len(raw), e.dim)
	}

	// Truncate to configured dimension and convert float64 → float32
	vec := make([]float32, e.dim)
	for i := 0; i < e.dim; i++ {
		vec[i] = float32(raw[i])
	}

	normalize(vec)
	return vec, nil
}

// EmbedBatch generates embeddings for multiple texts sequentially.
// Ollama's /api/embed endpoint does not support native batching.
func (e *OllamaEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := e.Embed(text)
		if err != nil {
			return nil, fmt.Errorf("embedding text %d: %w", i, err)
		}
		results[i] = vec
	}
	return results, nil
}

// Dim returns the embedding dimension.
func (e *OllamaEmbedder) Dim() int {
	return e.dim
}

// Close is a no-op for the Ollama embedder.
func (e *OllamaEmbedder) Close() error {
	return nil
}
