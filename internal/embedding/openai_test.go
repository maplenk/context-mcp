package embedding

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIEmbedder_Embed(t *testing.T) {
	dim := 4
	mockEmbedding := []float64{1.0, 2.0, 3.0, 4.0}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.WriteHeader(http.StatusOK)
			if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{}}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		case "/v1/embeddings":
			var req openAIEmbedRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("failed to decode request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if req.Model != "test-model" {
				t.Errorf("expected model test-model, got %s", req.Model)
			}
			if err := json.NewEncoder(w).Encode(openAIEmbedResponse{
				Data: []openAIEmbedData{
					{Embedding: mockEmbedding, Index: 0},
				},
			}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	emb, err := NewOpenAIEmbedder(srv.URL, "test-model", dim)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	vec, err := emb.Embed("hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(vec) != dim {
		t.Fatalf("expected dim %d, got %d", dim, len(vec))
	}

	// Verify L2 normalized
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if math.Abs(norm-1.0) > 1e-5 {
		t.Errorf("expected unit norm, got %f", norm)
	}
}

func TestOpenAIEmbedder_EmbedBatch(t *testing.T) {
	dim := 3
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.WriteHeader(http.StatusOK)
			if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{}}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		case "/v1/embeddings":
			callCount++
			// Return batch with out-of-order indices to test sorting
			if err := json.NewEncoder(w).Encode(openAIEmbedResponse{
				Data: []openAIEmbedData{
					{Embedding: []float64{0.0, 0.0, 1.0}, Index: 2},
					{Embedding: []float64{1.0, 0.0, 0.0}, Index: 0},
					{Embedding: []float64{0.0, 1.0, 0.0}, Index: 1},
				},
			}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	emb, err := NewOpenAIEmbedder(srv.URL, "test-model", dim)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	results, err := emb.EmbedBatch([]string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// OpenAI supports native batching — single API call
	if callCount != 1 {
		t.Errorf("expected 1 API call (native batch), got %d", callCount)
	}
	// Verify index sorting: result[0] should be [1,0,0] normalized
	if results[0][0] < 0.99 {
		t.Errorf("expected results[0] to be [1,0,0] (sorted by index), got %v", results[0])
	}
}

func TestOpenAIEmbedder_DimensionTooSmall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.WriteHeader(http.StatusOK)
			if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{}}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		case "/v1/embeddings":
			if err := json.NewEncoder(w).Encode(openAIEmbedResponse{
				Data: []openAIEmbedData{
					{Embedding: []float64{1.0, 2.0}, Index: 0},
				},
			}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	emb, err := NewOpenAIEmbedder(srv.URL, "test-model", 4)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	_, err = emb.Embed("hello")
	if err == nil {
		t.Fatal("expected error for dimension mismatch")
	}
}

func TestOpenAIEmbedder_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.WriteHeader(http.StatusOK)
			if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{}}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		case "/v1/embeddings":
			http.Error(w, "internal server error", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	emb, err := NewOpenAIEmbedder(srv.URL, "test-model", 4)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	_, err = emb.Embed("hello")
	if err == nil {
		t.Fatal("expected error for server error response")
	}
}

func TestOpenAIEmbedder_ConnectFailure(t *testing.T) {
	_, err := NewOpenAIEmbedder("http://127.0.0.1:1", "test-model", 4)
	if err == nil {
		t.Fatal("expected error connecting to unavailable server")
	}
}

func TestOpenAIEmbedder_Dim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{}}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	emb, err := NewOpenAIEmbedder(srv.URL, "test-model", 768)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}
	if emb.Dim() != 768 {
		t.Errorf("expected dim 768, got %d", emb.Dim())
	}
}

func TestOpenAIEmbedder_Determinism(t *testing.T) {
	dim := 4
	mockEmbedding := []float64{1.0, 2.0, 3.0, 4.0}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.WriteHeader(http.StatusOK)
			if err := json.NewEncoder(w).Encode(map[string]any{"data": []any{}}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		case "/v1/embeddings":
			if err := json.NewEncoder(w).Encode(openAIEmbedResponse{
				Data: []openAIEmbedData{
					{Embedding: mockEmbedding, Index: 0},
				},
			}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	emb, err := NewOpenAIEmbedder(srv.URL, "test-model", dim)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	vec1, _ := emb.Embed("test input")
	vec2, _ := emb.Embed("test input")

	for i := range vec1 {
		if vec1[i] != vec2[i] {
			t.Fatalf("non-deterministic: vec1[%d]=%f != vec2[%d]=%f", i, vec1[i], i, vec2[i])
		}
	}
}
