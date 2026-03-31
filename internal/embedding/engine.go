package embedding

import (
	"crypto/sha256"
	"encoding/binary"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// EmbeddingDim is the default embedding dimension.
// This can be overridden at runtime when using an ONNX model with Matryoshka dims.
var EmbeddingDim = 384

// Embedder is the interface for generating vector embeddings
type Embedder interface {
	Embed(text string) ([]float32, error)
	EmbedBatch(texts []string) ([][]float32, error)
	Dim() int
	Close() error
}

// NewEmbedder returns the best available embedder implementation.
// Currently returns a TFIDFEmbedder which provides real semantic locality
// (similar code identifiers produce similar vectors) using TF-IDF weighted
// word and character n-gram features projected to 384 dimensions.
// Falls back to HashEmbedder only if explicitly requested via NewHashEmbedder.
func NewEmbedder() Embedder {
	return NewTFIDFEmbedder()
}

// ---------------------------------------------------------------------------
// TFIDFEmbedder — TF-IDF weighted n-gram embedder (pure Go, no deps)
// ---------------------------------------------------------------------------

// TFIDFEmbedder generates semantically meaningful embeddings by:
//  1. Tokenizing input into words and subword pieces (camelCase split, underscore split)
//  2. Generating character trigrams for subword coverage
//  3. Using TF-IDF-like weighting: rare/long tokens get more weight
//  4. Projecting token hashes into a fixed 384-dim space using multiple hash functions
//  5. L2 normalizing the result
//
// This gives real semantic locality: "ReadFile" and "ReadFileContents" will
// produce similar vectors because they share tokens and trigrams.
type TFIDFEmbedder struct{}

// NewTFIDFEmbedder creates a new TF-IDF based embedder
func NewTFIDFEmbedder() *TFIDFEmbedder {
	return &TFIDFEmbedder{}
}

// Embed generates a 384-dimensional vector from text using TF-IDF n-gram features
func (e *TFIDFEmbedder) Embed(text string) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		vec := make([]float32, EmbeddingDim)
		// Return a deterministic vector for empty input
		vec[0] = 1.0
		normalize(vec)
		return vec, nil
	}

	vec := make([]float32, EmbeddingDim)

	// Tokenize: split on whitespace, punctuation, camelCase, underscores
	tokens := tokenize(text)

	// Count token frequencies (TF component)
	tf := make(map[string]int)
	for _, tok := range tokens {
		tf[tok]++
	}

	// Generate weighted features from word-level tokens
	totalTokens := float64(len(tokens))
	for tok, count := range tf {
		// TF-IDF-like weight: tf * inverse-frequency-proxy
		// Longer tokens are more specific (higher IDF proxy)
		idfProxy := math.Log(1.0 + float64(len(tok)))
		weight := (float64(count) / totalTokens) * idfProxy

		// Project this token into multiple dimensions using different hash seeds
		projectToken(vec, tok, float32(weight))
	}

	// Generate character trigrams for subword-level similarity
	lower := strings.ToLower(text)
	trigrams := make(map[string]int)
	for i := 0; i <= len(lower)-3; i++ {
		tri := lower[i : i+3]
		trigrams[tri]++
	}

	totalTrigrams := float64(len(lower) - 2)
	if totalTrigrams < 1 {
		totalTrigrams = 1
	}
	for tri, count := range trigrams {
		weight := float32(float64(count) / totalTrigrams * 0.5) // trigrams contribute less
		projectToken(vec, "tri:"+tri, weight)
	}

	// Generate character bigrams for additional overlap
	bigrams := make(map[string]int)
	for i := 0; i <= len(lower)-2; i++ {
		bi := lower[i : i+2]
		bigrams[bi]++
	}
	totalBigrams := float64(len(lower) - 1)
	if totalBigrams < 1 {
		totalBigrams = 1
	}
	for bi, count := range bigrams {
		weight := float32(float64(count) / totalBigrams * 0.3) // bigrams contribute less than trigrams
		projectToken(vec, "bi:"+bi, weight)
	}

	normalize(vec)
	return vec, nil
}

// EmbedBatch generates embeddings for multiple texts
func (e *TFIDFEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
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

// Dim returns the embedding dimension (384).
func (e *TFIDFEmbedder) Dim() int {
	return EmbeddingDim
}

// Close is a no-op for the TF-IDF embedder
func (e *TFIDFEmbedder) Close() error {
	return nil
}

// projectToken hashes a token into multiple dimensions of the vector using
// several independent hash functions (FNV-based with different seeds).
// Each hash function selects a dimension and adds/subtracts the weight,
// producing a sparse random projection (similar to random indexing / SimHash).
func projectToken(vec []float32, token string, weight float32) {
	// Use 4 independent projections per token for good coverage
	for seed := uint32(0); seed < 4; seed++ {
		h := fnv.New32a()
		// Mix seed into the hash
		seedBytes := [4]byte{}
		binary.LittleEndian.PutUint32(seedBytes[:], seed)
		h.Write(seedBytes[:])
		h.Write([]byte(token))
		hash := h.Sum32()

		dim := hash % uint32(EmbeddingDim)
		// Use bit 31 to decide sign (random +/- projection)
		if hash&(1<<31) != 0 {
			vec[dim] += weight
		} else {
			vec[dim] -= weight
		}
	}
}

// tokenize splits text into lowercase tokens, handling camelCase,
// underscore_case, and general punctuation splitting.
func tokenize(text string) []string {
	var tokens []string

	// First split on whitespace and common delimiters
	words := splitOnDelimiters(text)

	for _, word := range words {
		if word == "" {
			continue
		}
		// Split camelCase words
		parts := splitCamelCase(word)
		for _, part := range parts {
			lower := strings.ToLower(part)
			if lower != "" && len(lower) >= 2 {
				tokens = append(tokens, lower)
			}
		}
	}

	return tokens
}

// splitOnDelimiters splits text on whitespace, underscores, dots, slashes, and other common delimiters
func splitOnDelimiters(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || r == '_' || r == '.' || r == '/' ||
			r == '\\' || r == '-' || r == '(' || r == ')' || r == '{' ||
			r == '}' || r == '[' || r == ']' || r == ':' || r == ';' ||
			r == ',' || r == '"' || r == '\'' || r == '`' || r == '=' ||
			r == '+' || r == '*' || r == '&' || r == '|' || r == '<' ||
			r == '>' || r == '#' || r == '@' || r == '!' || r == '?'
	})
}

// splitCamelCase splits a CamelCase or camelCase identifier into its component words.
// E.g., "ReadFileContents" -> ["Read", "File", "Contents"]
// E.g., "parseJSON" -> ["parse", "JSON"]
// E.g., "HTTPServer" -> ["HTTP", "Server"]
func splitCamelCase(s string) []string {
	if s == "" {
		return nil
	}

	var parts []string
	runes := []rune(s)
	start := 0

	for i := 1; i < len(runes); i++ {
		// Split on transitions: lower->upper or upper->upper+lower
		if unicode.IsUpper(runes[i]) && unicode.IsLower(runes[i-1]) {
			parts = append(parts, string(runes[start:i]))
			start = i
		} else if i+1 < len(runes) && unicode.IsUpper(runes[i-1]) && unicode.IsUpper(runes[i]) && unicode.IsLower(runes[i+1]) {
			parts = append(parts, string(runes[start:i]))
			start = i
		}
	}
	parts = append(parts, string(runes[start:]))

	return parts
}

// ---------------------------------------------------------------------------
// HashEmbedder — last-resort fallback (SHA-256 hashing, no semantic meaning)
// ---------------------------------------------------------------------------

// HashEmbedder generates deterministic pseudo-embeddings using SHA-256 hashing.
// This is a last-resort fallback that maintains the correct vector dimensionality
// but provides NO real semantic similarity. Use TFIDFEmbedder instead.
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

// Dim returns the embedding dimension (384).
func (e *HashEmbedder) Dim() int {
	return EmbeddingDim
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
