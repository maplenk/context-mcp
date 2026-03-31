//go:build onnx

package embedding

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sync"

	ort "github.com/shota3506/onnxruntime-purego/onnxruntime"
)

// ONNXEmbedder runs a quantized ONNX model for embedding generation.
// Implements the Embedder interface with last-token pooling (causal LM)
// and Matryoshka dimension truncation.
type ONNXEmbedder struct {
	runtime   *ort.Runtime
	env       *ort.Env
	session   *ort.Session
	tokenizer *BPETokenizer
	dim       int // output dimension (Matryoshka truncation)
	mu        sync.Mutex
}

// Valid Matryoshka dimensions for the Qwen2 model
var validMatryoshkaDims = map[int]bool{
	64: true, 128: true, 256: true, 512: true, 896: true,
}

// NewONNXEmbedder creates an embedder that runs the ONNX model at modelDir.
// dim specifies the Matryoshka truncation dimension (64, 128, 256, 512, or 896).
// libPath is the path to the ONNX Runtime shared library.
func NewONNXEmbedder(modelDir string, dim int, libPath string) (*ONNXEmbedder, error) {
	if !validMatryoshkaDims[dim] {
		return nil, fmt.Errorf("invalid Matryoshka dimension %d; valid: 64, 128, 256, 512, 896", dim)
	}

	// Initialize ONNX Runtime (purego — no CGO required)
	rt, err := ort.NewRuntime(libPath, 23)
	if err != nil {
		return nil, fmt.Errorf("initializing ONNX Runtime: %w", err)
	}

	env, err := rt.NewEnv("qb-context", ort.LoggingLevelWarning)
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("creating ONNX environment: %w", err)
	}

	// Load tokenizer
	tokenizer, err := NewBPETokenizer(modelDir)
	if err != nil {
		env.Close()
		rt.Close()
		return nil, fmt.Errorf("loading tokenizer: %w", err)
	}

	// Create session with thread configuration
	modelPath := filepath.Join(modelDir, "model_quantized.onnx")
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
		dim:       dim,
	}, nil
}

// Embed generates an embedding vector from text using the ONNX model.
// Uses last-token pooling (appropriate for causal/decoder-only models).
func (e *ONNXEmbedder) Embed(text string) ([]float32, error) {
	// Tokenize
	inputIDs, attentionMask := e.tokenizer.EncodeWithSpecial(text)
	seqLen := int64(len(inputIDs))

	// Build position_ids: 0, 1, 2, ..., seqLen-1
	positionIDs := make([]int64, seqLen)
	for i := int64(0); i < seqLen; i++ {
		positionIDs[i] = i
	}

	// Create input tensors [1, seqLen]
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

	posIDsTensor, err := ort.NewTensorValue(e.runtime, positionIDs, shape)
	if err != nil {
		return nil, fmt.Errorf("creating position_ids tensor: %w", err)
	}
	defer posIDsTensor.Close()

	// Run inference (session is not thread-safe)
	inputs := map[string]*ort.Value{
		"input_ids":      inputIDsTensor,
		"attention_mask":  maskTensor,
		"position_ids":    posIDsTensor,
	}

	e.mu.Lock()
	outputs, err := e.session.Run(context.Background(), inputs, ort.WithOutputNames("last_hidden_state"))
	e.mu.Unlock()

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
func (e *ONNXEmbedder) Close() error {
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
