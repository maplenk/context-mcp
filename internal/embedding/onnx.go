//go:build onnx

package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"

	ort "github.com/shota3506/onnxruntime-purego/onnxruntime"
)

// onnxTokenizer is the interface both BPE and WordPiece tokenizers satisfy.
type onnxTokenizer interface {
	EncodeWithSpecial(text string) (inputIDs, attentionMask []int64)
}

// onnxModelType determines inference behavior.
type onnxModelType int

const (
	modelTypeQwen2     onnxModelType = iota // Jina: BPE, position_ids, last-token pooling
	modelTypeNomicBERT                      // CodeRankEmbed: WordPiece, sentence_embedding
)

// ONNXEmbedder runs a quantized ONNX model for embedding generation.
// Supports both Qwen2 (last-token pooling, Matryoshka dims) and
// NomicBERT (sentence_embedding output) model architectures.
type ONNXEmbedder struct {
	runtime   *ort.Runtime
	env       *ort.Env
	session   *ort.Session
	tokenizer onnxTokenizer
	modelType onnxModelType
	dim       int // output dimension (Matryoshka truncation or prefix truncation)
	mu        sync.Mutex
}

// onnxModelConfig is the subset of config.json used for model type detection.
type onnxModelConfig struct {
	ModelType  string `json:"model_type"`
	HiddenSize int    `json:"n_embd"` // NomicBERT uses n_embd
}

// Valid Matryoshka dimensions for the Qwen2 model
var validMatryoshkaDims = map[int]bool{
	64: true, 128: true, 256: true, 512: true, 896: true,
}

// DefaultDimForModel reads the model's config.json and returns the appropriate
// default embedding dimension. Returns 768 for NomicBERT (CodeRankEmbed),
// 256 for Qwen2 (Jina), and 384 (TF-IDF fallback) if detection fails.
func DefaultDimForModel(modelDir string) int {
	data, err := os.ReadFile(filepath.Join(modelDir, "config.json"))
	if err != nil {
		return 384 // TF-IDF fallback
	}
	var cfg onnxModelConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return 384
	}
	if cfg.ModelType == "nomic_bert" {
		return 768 // CodeRankEmbed full dim
	}
	return 256 // Qwen2 Matryoshka default
}

// NewONNXEmbedder creates an embedder that runs the ONNX model at modelDir.
// dim specifies the output dimension. For Qwen2 models, must be a valid
// Matryoshka dimension (64, 128, 256, 512, or 896). For NomicBERT models,
// any dim <= 768 is accepted.
// libPath is the path to the ONNX Runtime shared library.
func NewONNXEmbedder(modelDir string, dim int, libPath string) (*ONNXEmbedder, error) {
	// Auto-detect model type from config.json
	mtype := modelTypeQwen2 // default: backward compatible
	configPath := filepath.Join(modelDir, "config.json")
	if data, err := os.ReadFile(configPath); err == nil {
		var cfg onnxModelConfig
		if err := json.Unmarshal(data, &cfg); err == nil {
			if cfg.ModelType == "nomic_bert" {
				mtype = modelTypeNomicBERT
			}
		}
	}

	// Validate dimension based on model type
	switch mtype {
	case modelTypeQwen2:
		if !validMatryoshkaDims[dim] {
			return nil, fmt.Errorf("invalid Matryoshka dimension %d; valid: 64, 128, 256, 512, 896", dim)
		}
	case modelTypeNomicBERT:
		if dim <= 0 || dim > 768 {
			return nil, fmt.Errorf("invalid dimension %d for NomicBERT; must be 1..768", dim)
		}
	}

	// Initialize ONNX Runtime (purego — no CGO required)
	rt, err := ort.NewRuntime(libPath, 23)
	if err != nil {
		return nil, fmt.Errorf("initializing ONNX Runtime: %w", err)
	}

	env, err := rt.NewEnv("context-mcp", ort.LoggingLevelWarning)
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("creating ONNX environment: %w", err)
	}

	// Load tokenizer based on model type
	var tokenizer onnxTokenizer
	switch mtype {
	case modelTypeQwen2:
		tokenizer, err = NewBPETokenizer(modelDir)
	case modelTypeNomicBERT:
		tokenizer, err = NewWordPieceTokenizer(modelDir)
	}
	if err != nil {
		env.Close()
		rt.Close()
		return nil, fmt.Errorf("loading tokenizer: %w", err)
	}

	// Find model file: try onnx/model.onnx, model_quantized.onnx, model.onnx
	modelPath := ""
	candidates := []string{
		filepath.Join(modelDir, "onnx", "model.onnx"),
		filepath.Join(modelDir, "model_quantized.onnx"),
		filepath.Join(modelDir, "model.onnx"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			modelPath = c
			break
		}
	}
	if modelPath == "" {
		env.Close()
		rt.Close()
		return nil, fmt.Errorf("no ONNX model file found in %s (tried onnx/model.onnx, model_quantized.onnx, model.onnx)", modelDir)
	}

	// Create session with thread configuration
	opts := &ort.SessionOptions{
		IntraOpNumThreads: 2,
	}

	session, err := rt.NewSession(env, modelPath, opts)
	if err != nil {
		env.Close()
		rt.Close()
		return nil, fmt.Errorf("creating ONNX session: %w", err)
	}

	return &ONNXEmbedder{
		runtime:   rt,
		env:       env,
		session:   session,
		tokenizer: tokenizer,
		modelType: mtype,
		dim:       dim,
	}, nil
}

// Embed generates an embedding vector from text using the ONNX model.
// For Qwen2: uses last-token pooling (causal/decoder-only) with Matryoshka truncation.
// For NomicBERT: uses the sentence_embedding output directly.
func (e *ONNXEmbedder) Embed(text string) ([]float32, error) {
	// Tokenize (interface method — works for both BPE and WordPiece)
	inputIDs, attentionMask := e.tokenizer.EncodeWithSpecial(text)
	seqLen := int64(len(inputIDs))

	// Lock covers tensor creation through session.Run — both use e.runtime/e.session
	// which are not thread-safe.
	e.mu.Lock()
	defer e.mu.Unlock()

	// Check if closed (Close() nils out session under the same mutex)
	if e.session == nil {
		return nil, fmt.Errorf("embedder is closed")
	}

	// Create common input tensors [1, seqLen]
	shape := []int64{1, seqLen}

	inputIDsTensor, err := ort.NewTensorValue(e.runtime, inputIDs, shape)
	if err != nil {
		return nil, fmt.Errorf("creating input_ids tensor: %w", err)
	}
	defer inputIDsTensor.Close()

	maskTensor, err := ort.NewTensorValue(e.runtime, attentionMask, shape)
	if err != nil {
		return nil, fmt.Errorf("creating attention_mask tensor: %w", err)
	}
	defer maskTensor.Close()

	// Branch on model type
	if e.modelType == modelTypeNomicBERT {
		return e.embedNomicBERT(inputIDsTensor, maskTensor)
	}
	return e.embedQwen2(inputIDsTensor, maskTensor, seqLen)
}

// embedNomicBERT runs inference for NomicBERT/CodeRankEmbed models.
// Uses sentence_embedding output directly (no position_ids needed).
func (e *ONNXEmbedder) embedNomicBERT(inputIDsTensor, maskTensor *ort.Value) ([]float32, error) {
	inputs := map[string]*ort.Value{
		"input_ids":      inputIDsTensor,
		"attention_mask":  maskTensor,
	}

	outputs, err := e.session.Run(context.Background(), inputs, ort.WithOutputNames("sentence_embedding"))
	if err != nil {
		return nil, fmt.Errorf("ONNX inference: %w", err)
	}
	defer func() {
		for _, o := range outputs {
			if o != nil {
				o.Close()
			}
		}
	}()

	outputValue, ok := outputs["sentence_embedding"]
	if !ok {
		return nil, fmt.Errorf("missing sentence_embedding output")
	}

	embData, _, err := ort.GetTensorData[float32](outputValue)
	if err != nil {
		return nil, fmt.Errorf("extracting sentence_embedding: %w", err)
	}

	if len(embData) < e.dim {
		return nil, fmt.Errorf("sentence_embedding dim %d < requested %d", len(embData), e.dim)
	}

	embedding := make([]float32, e.dim)
	copy(embedding, embData[:e.dim])
	normalizeVec(embedding)
	return embedding, nil
}

// embedQwen2 runs inference for Qwen2/Jina models.
// Uses position_ids, last_hidden_state output, last-token pooling, and Matryoshka truncation.
func (e *ONNXEmbedder) embedQwen2(inputIDsTensor, maskTensor *ort.Value, seqLen int64) ([]float32, error) {
	// Build position_ids: 0, 1, 2, ..., seqLen-1
	positionIDs := make([]int64, seqLen)
	for i := int64(0); i < seqLen; i++ {
		positionIDs[i] = i
	}

	shape := []int64{1, seqLen}
	posIDsTensor, err := ort.NewTensorValue(e.runtime, positionIDs, shape)
	if err != nil {
		return nil, fmt.Errorf("creating position_ids tensor: %w", err)
	}
	defer posIDsTensor.Close()

	// Run inference
	inputs := map[string]*ort.Value{
		"input_ids":      inputIDsTensor,
		"attention_mask":  maskTensor,
		"position_ids":    posIDsTensor,
	}

	outputs, err := e.session.Run(context.Background(), inputs, ort.WithOutputNames("last_hidden_state"))
	if err != nil {
		return nil, fmt.Errorf("ONNX inference: %w", err)
	}

	// Close ALL output values when done (covers all outputs, not just the one we use)
	defer func() {
		for _, o := range outputs {
			if o != nil {
				o.Close()
			}
		}
	}()

	// Extract last_hidden_state [1, seqLen, hiddenDim]
	outputValue, ok := outputs["last_hidden_state"]
	if !ok {
		return nil, fmt.Errorf("missing last_hidden_state output")
	}

	hiddenData, _, err := ort.GetTensorData[float32](outputValue)
	if err != nil {
		return nil, fmt.Errorf("extracting output tensor data: %w", err)
	}

	// Derive hidden dimension from output data size instead of hardcoding.
	// Output shape is [1, seqLen, hiddenDim], so hiddenDim = len(data) / seqLen.
	hiddenDim := 896 // default fallback for Qwen2 model
	if seqLen > 0 && len(hiddenData) > 0 {
		if len(hiddenData)%int(seqLen) != 0 {
			return nil, fmt.Errorf("hidden state size %d not divisible by sequence length %d", len(hiddenData), seqLen)
		}
		computed := len(hiddenData) / int(seqLen)
		if computed > 0 {
			hiddenDim = computed
		}
	}

	// Validate Matryoshka dim fits within hidden dimension
	if e.dim > hiddenDim {
		return nil, fmt.Errorf("requested Matryoshka dim %d exceeds model hidden dim %d", e.dim, hiddenDim)
	}

	// Last-token pooling: take the hidden state of the last token
	lastTokenIdx := int(seqLen - 1)
	offset := lastTokenIdx * hiddenDim
	if offset+hiddenDim > len(hiddenData) {
		return nil, fmt.Errorf("hidden state too small: %d, need %d", len(hiddenData), offset+hiddenDim)
	}

	// Matryoshka truncation: take first `dim` values
	embedding := make([]float32, e.dim)
	copy(embedding, hiddenData[offset:offset+e.dim])

	// L2 normalize
	normalizeVec(embedding)

	return embedding, nil
}

// EmbedBatch generates embeddings for multiple texts.
// Note: This iterates sequentially rather than using true batch inference.
// The underlying ONNX session is protected by a mutex (not thread-safe) and
// currently configured for single-sequence input tensors with shape [1, seqLen].
// True batching would require padding all inputs to the same length, creating
// tensors with shape [batchSize, maxSeqLen], and adjusting the attention mask
// accordingly. The onnxruntime-purego bindings support this in principle, but
// the model was exported with batch_size=1 and variable-length sequences
// have different padding requirements. The sequential approach is simpler and
// avoids wasting compute on padding tokens.
func (e *ONNXEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
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

// Dim returns the embedding dimension (Matryoshka truncation size).
func (e *ONNXEmbedder) Dim() int {
	return e.dim
}

// Close destroys the ONNX session, environment, and runtime, freeing resources.
// Nils out fields after closing to prevent double-close panics.
// Acquires the mutex to avoid racing with concurrent Embed() calls.
func (e *ONNXEmbedder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != nil {
		e.session.Close()
		e.session = nil
	}
	if e.env != nil {
		e.env.Close()
		e.env = nil
	}
	if e.runtime != nil {
		err := e.runtime.Close()
		e.runtime = nil
		return err
	}
	return nil
}

// normalizeVec applies L2 normalization to a vector in-place.
func normalizeVec(vec []float32) {
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
