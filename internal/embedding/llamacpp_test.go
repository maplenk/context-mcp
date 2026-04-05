package embedding

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLlamaCppEmbedder_Embed(t *testing.T) {
	dim := 4
	mockEmbedding := []float64{1.0, 2.0, 3.0, 4.0}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/embedding":
			var req llamaCppEmbedRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(llamaCppEmbedResponse{
				Embedding: mockEmbedding,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	emb, err := NewLlamaCppEmbedder(srv.URL, dim)
	if err != nil {
		t.Fatalf("NewLlamaCppEmbedder: %v", err)
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

func TestLlamaCppEmbedder_EmbedBatch(t *testing.T) {
	dim := 3

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/embedding":
			json.NewEncoder(w).Encode(llamaCppBatchResponse{
				Results: []llamaCppBatchItem{
					{Embedding: []float64{1.0, 0.0, 0.0}},
					{Embedding: []float64{0.0, 1.0, 0.0}},
					{Embedding: []float64{0.0, 0.0, 1.0}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	emb, err := NewLlamaCppEmbedder(srv.URL, dim)
	if err != nil {
		t.Fatalf("NewLlamaCppEmbedder: %v", err)
	}

	results, err := emb.EmbedBatch([]string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Each should be unit-length
	for i, vec := range results {
		var norm float64
		for _, v := range vec {
			norm += float64(v) * float64(v)
		}
		if math.Abs(norm-1.0) > 1e-5 {
			t.Errorf("result[%d] not unit norm: %f", i, norm)
		}
	}
}

func TestLlamaCppEmbedder_DimensionTooSmall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/embedding":
			json.NewEncoder(w).Encode(llamaCppEmbedResponse{
				Embedding: []float64{1.0, 2.0},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	emb, err := NewLlamaCppEmbedder(srv.URL, 4)
	if err != nil {
		t.Fatalf("NewLlamaCppEmbedder: %v", err)
	}

	_, err = emb.Embed("hello")
	if err == nil {
		t.Fatal("expected error for dimension mismatch")
	}
}

func TestLlamaCppEmbedder_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/embedding":
			http.Error(w, "internal error", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	emb, err := NewLlamaCppEmbedder(srv.URL, 4)
	if err != nil {
		t.Fatalf("NewLlamaCppEmbedder: %v", err)
	}

	_, err = emb.Embed("hello")
	if err == nil {
		t.Fatal("expected error for server error response")
	}
}

func TestLlamaCppEmbedder_ConnectFailure(t *testing.T) {
	_, err := NewLlamaCppEmbedder("http://127.0.0.1:1", 4)
	if err == nil {
		t.Fatal("expected error connecting to unavailable server")
	}
}

func TestLlamaCppEmbedder_Dim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	emb, err := NewLlamaCppEmbedder(srv.URL, 768)
	if err != nil {
		t.Fatalf("NewLlamaCppEmbedder: %v", err)
	}
	if emb.Dim() != 768 {
		t.Errorf("expected dim 768, got %d", emb.Dim())
	}
}

func TestLlamaCppEmbedder_BatchFallbackToSequential(t *testing.T) {
	dim := 3
	singleCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/embedding":
			var raw json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				http.Error(w, "bad", 400)
				return
			}
			// If content is a string → single request; if array → batch (return wrong count to trigger fallback)
			var req struct{ Content json.RawMessage }
			json.Unmarshal(raw, &req)
			if len(req.Content) > 0 && req.Content[0] == '"' {
				singleCalls++
				json.NewEncoder(w).Encode(llamaCppEmbedResponse{
					Embedding: []float64{1.0, 0.0, 0.0},
				})
			} else {
				// Return wrong count to trigger fallback
				json.NewEncoder(w).Encode(llamaCppBatchResponse{
					Results: []llamaCppBatchItem{
						{Embedding: []float64{1.0, 0.0, 0.0}},
					},
				})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	emb, err := NewLlamaCppEmbedder(srv.URL, dim)
	if err != nil {
		t.Fatalf("NewLlamaCppEmbedder: %v", err)
	}

	results, err := emb.EmbedBatch([]string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("EmbedBatch should fall back to sequential: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if singleCalls != 3 {
		t.Errorf("expected 3 sequential calls after batch fallback, got %d", singleCalls)
	}
}
