package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// OpenAIEmbedder generates embeddings via any OpenAI-compatible /v1/embeddings API
// (LM Studio, vLLM, text-embeddings-inference, etc.).
type OpenAIEmbedder struct {
	endpoint string
	model    string
	dim      int
	client   *http.Client
}

type openAIEmbedRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"` // string for single, []string for batch
}

type openAIEmbedResponse struct {
	Data []openAIEmbedData `json:"data"`
}

type openAIEmbedData struct {
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

// NewOpenAIEmbedder creates an embedder that calls an OpenAI-compatible API.
// It verifies connectivity by hitting GET /v1/models.
func NewOpenAIEmbedder(endpoint, model string, dim int) (*OpenAIEmbedder, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("embedding dimension must be positive, got %d", dim)
	}

	endpoint = strings.TrimRight(endpoint, "/")

	client := &http.Client{Timeout: 120 * time.Second}

	// Verify server is reachable
	resp, err := client.Get(endpoint + "/v1/models")
	if err != nil {
		return nil, fmt.Errorf("connecting to OpenAI-compatible server at %s: %w", endpoint, err)
	}
	if err := discardAndClose("OpenAI-compatible models", resp); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAI-compatible server at %s returned status %d", endpoint, resp.StatusCode)
	}

	return &OpenAIEmbedder{
		endpoint: endpoint,
		model:    model,
		dim:      dim,
		client:   client,
	}, nil
}

func (e *OpenAIEmbedder) Embed(text string) ([]float32, error) {
	reqBody, err := json.Marshal(openAIEmbedRequest{Model: e.model, Input: text})
	if err != nil {
		return nil, fmt.Errorf("marshaling OpenAI request: %w", err)
	}

	resp, err := e.client.Post(e.endpoint+"/v1/embeddings", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("OpenAI embed request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("OpenAI server returned status %d: %s", resp.StatusCode, string(body))
	}

	var result openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding OpenAI response: %w", err)
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("OpenAI server returned no embeddings")
	}

	raw := result.Data[0].Embedding
	if len(raw) < e.dim {
		return nil, fmt.Errorf("OpenAI embedding dimension %d < requested %d", len(raw), e.dim)
	}

	vec := make([]float32, e.dim)
	for i := 0; i < e.dim; i++ {
		vec[i] = float32(raw[i])
	}

	normalize(vec)
	return vec, nil
}

// EmbedBatch generates embeddings for multiple texts using native batch support.
// The OpenAI /v1/embeddings API accepts an array of strings as input.
func (e *OpenAIEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	reqBody, err := json.Marshal(openAIEmbedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("marshaling OpenAI batch request: %w", err)
	}

	resp, err := e.client.Post(e.endpoint+"/v1/embeddings", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("OpenAI batch embed request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("OpenAI server returned status %d: %s", resp.StatusCode, string(body))
	}

	var result openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding OpenAI batch response: %w", err)
	}

	if len(result.Data) != len(texts) {
		return nil, fmt.Errorf("OpenAI returned %d embeddings, expected %d", len(result.Data), len(texts))
	}

	// Sort by index to ensure correct ordering
	sort.Slice(result.Data, func(i, j int) bool {
		return result.Data[i].Index < result.Data[j].Index
	})

	results := make([][]float32, len(texts))
	for i, item := range result.Data {
		if len(item.Embedding) < e.dim {
			return nil, fmt.Errorf("OpenAI embedding %d dimension %d < requested %d", i, len(item.Embedding), e.dim)
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

func (e *OpenAIEmbedder) Dim() int { return e.dim }

func (e *OpenAIEmbedder) Close() error { return nil }
