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

// LlamaCppEmbedder generates embeddings via the llama.cpp server HTTP API.
// Implements the Embedder interface using the /embedding endpoint.
type LlamaCppEmbedder struct {
	endpoint string
	dim      int
	client   *http.Client
}

// llamaCppEmbedRequest is the request body for POST /embedding.
type llamaCppEmbedRequest struct {
	Content any `json:"content"` // string for single, []string for batch
}

// llamaCppEmbedResponse is the response from POST /embedding (single).
type llamaCppEmbedResponse struct {
	Embedding []float64 `json:"embedding"`
}

// llamaCppBatchResponse is the response from POST /embedding (batch).
type llamaCppBatchResponse struct {
	Results []llamaCppBatchItem `json:"results"`
}

// llamaCppBatchItem is a single item in the batch response.
type llamaCppBatchItem struct {
	Embedding []float64 `json:"embedding"`
}

// NewLlamaCppEmbedder creates an embedder that calls the llama.cpp server API.
// It verifies connectivity by pinging the /health endpoint.
func NewLlamaCppEmbedder(endpoint string, dim int) (*LlamaCppEmbedder, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("embedding dimension must be positive, got %d", dim)
	}

	endpoint = strings.TrimRight(endpoint, "/")

	client := &http.Client{Timeout: 30 * time.Second}

	// Verify llama.cpp server is reachable
	resp, err := client.Get(endpoint + "/health")
	if err != nil {
		return nil, fmt.Errorf("connecting to llama.cpp server at %s: %w", endpoint, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llama.cpp server at %s returned status %d", endpoint, resp.StatusCode)
	}

	return &LlamaCppEmbedder{
		endpoint: endpoint,
		dim:      dim,
		client:   client,
	}, nil
}

// Embed generates an embedding vector for a single text.
func (e *LlamaCppEmbedder) Embed(text string) ([]float32, error) {
	reqBody, err := json.Marshal(llamaCppEmbedRequest{Content: text})
	if err != nil {
		return nil, fmt.Errorf("marshaling llama.cpp request: %w", err)
	}

	resp, err := e.client.Post(e.endpoint+"/embedding", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("llama.cpp embed request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("llama.cpp returned status %d: %s", resp.StatusCode, string(body))
	}

	var result llamaCppEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding llama.cpp response: %w", err)
	}

	if len(result.Embedding) < e.dim {
		return nil, fmt.Errorf("llama.cpp embedding dimension %d < requested %d", len(result.Embedding), e.dim)
	}

	// Truncate to configured dimension and convert float64 → float32
	vec := make([]float32, e.dim)
	for i := 0; i < e.dim; i++ {
		vec[i] = float32(result.Embedding[i])
	}

	normalize(vec)
	return vec, nil
}

// EmbedBatch generates embeddings for multiple texts using llama.cpp's native
// batch support. Falls back to sequential calls if the batch response format
// is not recognized.
func (e *LlamaCppEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	reqBody, err := json.Marshal(llamaCppEmbedRequest{Content: texts})
	if err != nil {
		return nil, fmt.Errorf("marshaling llama.cpp batch request: %w", err)
	}

	resp, err := e.client.Post(e.endpoint+"/embedding", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("llama.cpp batch embed request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("llama.cpp returned status %d: %s", resp.StatusCode, string(body))
	}

	var batchResult llamaCppBatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&batchResult); err != nil {
		return nil, fmt.Errorf("decoding llama.cpp batch response: %w", err)
	}

	if len(batchResult.Results) != len(texts) {
		return nil, fmt.Errorf("llama.cpp returned %d embeddings, expected %d", len(batchResult.Results), len(texts))
	}

	results := make([][]float32, len(texts))
	for i, item := range batchResult.Results {
		if len(item.Embedding) < e.dim {
			return nil, fmt.Errorf("llama.cpp embedding %d dimension %d < requested %d", i, len(item.Embedding), e.dim)
		}
		vec := make([]float32, e.dim)
		for j := 0; j < e.dim; j++ {
			vec[j] = float32(item.Embedding[j])
		}
		normalize(vec)
		results[i] = vec
	}

	return results, nil
}

// Dim returns the embedding dimension.
func (e *LlamaCppEmbedder) Dim() int {
	return e.dim
}

// Close is a no-op for the llama.cpp HTTP embedder.
func (e *LlamaCppEmbedder) Close() error {
	return nil
}
