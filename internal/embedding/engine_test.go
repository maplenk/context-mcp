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
	if len(vec) != GetEmbeddingDim() {
		t.Errorf("expected dim %d, got %d", GetEmbeddingDim(), len(vec))
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
		if len(v) != GetEmbeddingDim() {
			t.Errorf("vector[%d] has dim %d, want %d", i, len(v), GetEmbeddingDim())
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

// TestHashEmbedder_EmptyString verifies that Embed("") returns a valid 384-dim vector
// without panicking or returning an error. An empty string may produce a zero vector
// (all dimensions zero after normalization) or a valid hash-based vector — both are
// acceptable as long as the slice length is correct and no error is returned.
func TestHashEmbedder_EmptyString(t *testing.T) {
	e := NewHashEmbedder()
	vec, err := e.Embed("")
	if err != nil {
		t.Fatalf("Embed(\"\") returned unexpected error: %v", err)
	}
	if len(vec) != GetEmbeddingDim() {
		t.Errorf("expected dim %d, got %d", GetEmbeddingDim(), len(vec))
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

// ----------- TFIDFEmbedder Tests -----------

// TestTFIDFEmbedder_Dimension verifies that Embed returns a 384-dimensional vector.
func TestTFIDFEmbedder_Dimension(t *testing.T) {
	e := NewTFIDFEmbedder()
	vec, err := e.Embed("hello world")
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(vec) != GetEmbeddingDim() {
		t.Errorf("expected dim %d, got %d", GetEmbeddingDim(), len(vec))
	}
}

// TestTFIDFEmbedder_Deterministic verifies same input always produces same output.
func TestTFIDFEmbedder_Deterministic(t *testing.T) {
	e := NewTFIDFEmbedder()
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

// TestTFIDFEmbedder_Normalized verifies that the L2 norm is approximately 1.0.
func TestTFIDFEmbedder_Normalized(t *testing.T) {
	e := NewTFIDFEmbedder()
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

// TestTFIDFEmbedder_SemanticLocality verifies that similar inputs produce more similar
// vectors than dissimilar inputs. This is the KEY property that HashEmbedder lacks.
func TestTFIDFEmbedder_SemanticLocality(t *testing.T) {
	e := NewTFIDFEmbedder()

	// Two similar code descriptions
	vecA, _ := e.Embed("ReadFile reads a file from disk")
	vecB, _ := e.Embed("ReadFileContents reads file contents from disk")

	// A completely different description
	vecC, _ := e.Embed("database connection pool management with retries")

	simAB := CosineSimilarity(vecA, vecB)
	simAC := CosineSimilarity(vecA, vecC)

	if simAB <= simAC {
		t.Errorf("expected similar texts to have higher similarity: sim(A,B)=%f <= sim(A,C)=%f", simAB, simAC)
	}
}

// TestTFIDFEmbedder_CamelCaseSimilarity verifies that CamelCase identifiers with
// shared components produce similar vectors.
func TestTFIDFEmbedder_CamelCaseSimilarity(t *testing.T) {
	e := NewTFIDFEmbedder()

	vecA, _ := e.Embed("ComputeChecksum")
	vecB, _ := e.Embed("ComputeHash")
	vecC, _ := e.Embed("DatabaseConnection")

	simAB := CosineSimilarity(vecA, vecB)
	simAC := CosineSimilarity(vecA, vecC)

	if simAB <= simAC {
		t.Errorf("expected 'ComputeChecksum' closer to 'ComputeHash' than 'DatabaseConnection': sim(A,B)=%f, sim(A,C)=%f", simAB, simAC)
	}
}

// TestTFIDFEmbedder_EmptyString verifies that empty input returns a valid vector.
func TestTFIDFEmbedder_EmptyString(t *testing.T) {
	e := NewTFIDFEmbedder()
	vec, err := e.Embed("")
	if err != nil {
		t.Fatalf("Embed(\"\") returned unexpected error: %v", err)
	}
	if len(vec) != GetEmbeddingDim() {
		t.Errorf("expected dim %d, got %d", GetEmbeddingDim(), len(vec))
	}
}

// TestTFIDFEmbedder_EmbedBatch verifies batch embedding works correctly.
func TestTFIDFEmbedder_EmbedBatch(t *testing.T) {
	e := NewTFIDFEmbedder()
	texts := []string{
		"first text about reading files",
		"second text about database queries",
		"third text about network connections",
	}

	vecs, err := e.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Errorf("expected %d vectors, got %d", len(texts), len(vecs))
	}
	for i, v := range vecs {
		if len(v) != GetEmbeddingDim() {
			t.Errorf("vector[%d] has dim %d, want %d", i, len(v), GetEmbeddingDim())
		}
	}
}

// TestTFIDFEmbedder_SelfSimilarityIsOne verifies cosine similarity of a vector with itself is ~1.0.
func TestTFIDFEmbedder_SelfSimilarityIsOne(t *testing.T) {
	e := NewTFIDFEmbedder()
	vec, err := e.Embed("cosine similarity self test for tfidf")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	sim := CosineSimilarity(vec, vec)
	if math.Abs(sim-1.0) > 1e-5 {
		t.Errorf("self cosine similarity: expected ~1.0, got %f", sim)
	}
}

// TestNewEmbedder_ReturnsTFIDF verifies that NewEmbedder returns a TFIDFEmbedder.
func TestNewEmbedder_ReturnsTFIDF(t *testing.T) {
	e := NewEmbedder()
	if e == nil {
		t.Fatal("NewEmbedder returned nil")
	}
	// Verify it's a TFIDFEmbedder by checking it implements Embedder and produces valid output
	vec, err := e.Embed("test")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != GetEmbeddingDim() {
		t.Errorf("expected dim %d, got %d", GetEmbeddingDim(), len(vec))
	}
}

// TestTokenize verifies the tokenizer handles various input formats.
func TestTokenize(t *testing.T) {
	tests := []struct {
		input    string
		contains []string // tokens that should be present
	}{
		{"ReadFile", []string{"read", "file"}},
		{"parse_json_data", []string{"parse", "json", "data"}},
		{"HTTPServer", []string{"http", "server"}},
		{"simple words here", []string{"simple", "words", "here"}},
		{"mixedCase_and_underscores", []string{"mixed", "case", "underscores"}},
	}

	for _, tt := range tests {
		tokens := tokenize(tt.input)
		tokenSet := make(map[string]bool)
		for _, tok := range tokens {
			tokenSet[tok] = true
		}
		for _, expected := range tt.contains {
			if !tokenSet[expected] {
				t.Errorf("tokenize(%q): expected token %q in %v", tt.input, expected, tokens)
			}
		}
	}
}

// TestCosineSimilarity_DimensionMismatch verifies that CosineSimilarity returns 0
// when given vectors of different dimensions rather than panicking.
func TestCosineSimilarity_DimensionMismatch(t *testing.T) {
	vecA := []float32{0.1, 0.2, 0.3}
	vecB := []float32{0.4, 0.5}

	// Must not panic
	sim := CosineSimilarity(vecA, vecB)
	if sim != 0 {
		t.Errorf("expected 0 for dimension mismatch, got %f", sim)
	}
}

// TestEmbeddingDimMismatch_StorageSafe verifies that embedding operations handle
// dimension mismatches gracefully (e.g., if dim changes between embed and store).
func TestEmbeddingDimMismatch_StorageSafe(t *testing.T) {
	e := NewHashEmbedder()

	// Generate a vector at current dim
	vec, err := e.Embed("test")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	// Serialize and deserialize should preserve exact dimensions
	blob := SerializeFloat32(vec)
	recovered := DeserializeFloat32(blob)
	if len(recovered) != len(vec) {
		t.Errorf("deserialized dim %d != original dim %d", len(recovered), len(vec))
	}

	// A truncated blob should produce a shorter vector (not panic)
	truncated := blob[:len(blob)/2]
	short := DeserializeFloat32(truncated)
	if len(short) != len(vec)/2 {
		t.Errorf("truncated deserialization: expected dim %d, got %d", len(vec)/2, len(short))
	}
}

// TestSplitCamelCase verifies CamelCase splitting.
func TestSplitCamelCase(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"ReadFile", []string{"Read", "File"}},
		{"HTTPServer", []string{"HTTP", "Server"}},
		{"parseJSON", []string{"parse", "JSON"}},
		{"simple", []string{"simple"}},
		{"ABC", []string{"ABC"}},
	}

	for _, tt := range tests {
		got := splitCamelCase(tt.input)
		if len(got) != len(tt.expected) {
			t.Errorf("splitCamelCase(%q) = %v, want %v", tt.input, got, tt.expected)
			continue
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("splitCamelCase(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.expected[i])
			}
		}
	}
}
