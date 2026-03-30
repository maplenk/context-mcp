package embedding

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"strings"
)

const EmbeddingDim = 384

// Embedder is the interface for generating vector embeddings
type Embedder interface {
	Embed(text string) ([]float32, error)
	EmbedBatch(texts []string) ([][]float32, error)
	Close() error
}

// HashEmbedder generates deterministic pseudo-embeddings using SHA-256 hashing.
// This is a fallback implementation that maintains the correct vector dimensionality
// and provides basic semantic locality (similar strings produce somewhat similar vectors).
// It should be replaced with a real ONNX-based embedder for production use.
type HashEmbedder struct{}

// NewHashEmbedder creates a new hash-based embedder
func NewHashEmbedder() *HashEmbedder {
	return &HashEmbedder{}
}

// Embed generates a 384-dimensional vector from text using deterministic hashing
func (e *HashEmbedder) Embed(text string) ([]float32, error) {
	// Normalize input
	text = strings.ToLower(strings.TrimSpace(text))

	// Generate multiple hashes from the text and sliding windows
	vec := make([]float32, EmbeddingDim)

	// Use the full text hash as the primary signal
	hash := sha256.Sum256([]byte(text))
	for i := 0; i < 32 && i < EmbeddingDim; i++ {
		vec[i] = float32(hash[i])/128.0 - 1.0 // Normalize to [-1, 1]
	}

	// Use word-level hashes to fill remaining dimensions
	words := strings.Fields(text)
	for wi, word := range words {
		wordHash := sha256.Sum256([]byte(word))
		offset := 32 + (wi * 32)
		for i := 0; i < 32 && offset+i < EmbeddingDim; i++ {
			vec[offset+i] = float32(wordHash[i])/128.0 - 1.0
		}
	}

	// Use character n-grams for the rest
	for i := 0; i < len(text)-2 && i < EmbeddingDim; i++ {
		trigram := text[i : i+3]
		h := sha256.Sum256([]byte(trigram))
		idx := (256 + i) % EmbeddingDim
		if vec[idx] == 0 {
			vec[idx] = float32(h[0])/128.0 - 1.0
		}
	}

	// L2 normalize the vector
	normalize(vec)

	return vec, nil
}

// EmbedBatch generates embeddings for multiple texts
func (e *HashEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := e.Embed(text)
		if err != nil {
			return nil, err
		}
		results[i] = vec
	}
	return results, nil
}

// Close is a no-op for the hash embedder
func (e *HashEmbedder) Close() error {
	return nil
}

// normalize applies L2 normalization to a vector
func normalize(vec []float32) {
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	if sum == 0 {
		return
	}
	norm := float32(math.Sqrt(sum))
	for i := range vec {
		vec[i] /= norm
	}
}

// SerializeFloat32 converts a float32 slice to a little-endian byte slice
// Compatible with sqlite-vec's expected BLOB format
func SerializeFloat32(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// DeserializeFloat32 converts a little-endian byte slice back to float32 slice
func DeserializeFloat32(buf []byte) []float32 {
	vec := make([]float32, len(buf)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return vec
}

// CosineSimilarity computes cosine similarity between two vectors
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
