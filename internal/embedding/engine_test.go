package embedding

import (
	"math"
	"testing"
)

// TestHashEmbedder_Embed_Dimension verifies that Embed returns a 384-dimensional vector.
func TestHashEmbedder_Embed_Dimension(t *testing.T) {
	e := NewHashEmbedder()
	vec, err := e.Embed("hello world")
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(vec) != EmbeddingDim {
		t.Errorf("expected dim %d, got %d", EmbeddingDim, len(vec))
	}
}

// TestHashEmbedder_Embed_Deterministic verifies that the same input always produces the same output.
func TestHashEmbedder_Embed_Deterministic(t *testing.T) {
	e := NewHashEmbedder()
	const text = "deterministic embedding test"

	vec1, err := e.Embed(text)
	if err != nil {
		t.Fatalf("first Embed: %v", err)
	}
	vec2, err := e.Embed(text)
	if err != nil {
		t.Fatalf("second Embed: %v", err)
	}

	for i := range vec1 {
		if vec1[i] != vec2[i] {
			t.Errorf("dimension %d differs: %v vs %v", i, vec1[i], vec2[i])
		}
	}
}

// TestHashEmbedder_Embed_DifferentInputs verifies that different inputs produce different vectors.
func TestHashEmbedder_Embed_DifferentInputs(t *testing.T) {
	e := NewHashEmbedder()

	vec1, err := e.Embed("function add numbers")
	if err != nil {
		t.Fatalf("Embed 1: %v", err)
	}
	vec2, err := e.Embed("completely different text about databases")
	if err != nil {
		t.Fatalf("Embed 2: %v", err)
	}

	same := true
	for i := range vec1 {
		if vec1[i] != vec2[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("different inputs produced identical vectors")
	}
}

// TestHashEmbedder_Embed_Normalized verifies that the L2 norm of the output is approximately 1.0.
func TestHashEmbedder_Embed_Normalized(t *testing.T) {
	e := NewHashEmbedder()
	vec, err := e.Embed("test normalization of embedding vector")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	var sumSq float64
	for _, v := range vec {
		sumSq += float64(v) * float64(v)
	}
	norm := math.Sqrt(sumSq)
	if math.Abs(norm-1.0) > 1e-5 {
		t.Errorf("expected L2 norm ~1.0, got %f", norm)
	}
}

// TestHashEmbedder_EmbedBatch_Count verifies that EmbedBatch returns the correct number of vectors.
func TestHashEmbedder_EmbedBatch_Count(t *testing.T) {
	e := NewHashEmbedder()
	texts := []string{
		"first text",
		"second text",
		"third text",
		"fourth text",
	}

	vecs, err := e.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Errorf("expected %d vectors, got %d", len(texts), len(vecs))
	}
	for i, v := range vecs {
		if len(v) != EmbeddingDim {
			t.Errorf("vector[%d] has dim %d, want %d", i, len(v), EmbeddingDim)
		}
	}
}

// TestSerializeDeserialize_Roundtrip verifies that serialization/deserialization is lossless.
func TestSerializeDeserialize_Roundtrip(t *testing.T) {
	e := NewHashEmbedder()
	original, err := e.Embed("serialize deserialize roundtrip")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	blob := SerializeFloat32(original)
	recovered := DeserializeFloat32(blob)

	if len(recovered) != len(original) {
		t.Fatalf("length mismatch: got %d, want %d", len(recovered), len(original))
	}
	for i := range original {
		if recovered[i] != original[i] {
			t.Errorf("dim %d: got %v, want %v", i, recovered[i], original[i])
		}
	}
}

// TestCosineSimilarity_SelfIsOne verifies that the cosine similarity of a vector with itself is ~1.0.
func TestCosineSimilarity_SelfIsOne(t *testing.T) {
	e := NewHashEmbedder()
	vec, err := e.Embed("cosine similarity self test")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	sim := CosineSimilarity(vec, vec)
	if math.Abs(sim-1.0) > 1e-5 {
		t.Errorf("self cosine similarity: expected ~1.0, got %f", sim)
	}
}

// TestCosineSimilarity_DifferentIsLessThanOne verifies that two different vectors have similarity < 1.0.
func TestCosineSimilarity_DifferentIsLessThanOne(t *testing.T) {
	e := NewHashEmbedder()

	vec1, err := e.Embed("function to compute checksum of file contents")
	if err != nil {
		t.Fatalf("Embed 1: %v", err)
	}
	vec2, err := e.Embed("database connection pool management")
	if err != nil {
		t.Fatalf("Embed 2: %v", err)
	}

	sim := CosineSimilarity(vec1, vec2)
	if sim >= 1.0 {
		t.Errorf("expected similarity < 1.0 for different vectors, got %f", sim)
	}
}
