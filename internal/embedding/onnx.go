//go:build onnx

package embedding

import (
	"fmt"
	"math"
	"path/filepath"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// ONNXEmbedder runs a quantized ONNX model for embedding generation.
// Implements the Embedder interface with last-token pooling (causal LM)
// and Matryoshka dimension truncation.
type ONNXEmbedder struct {
	session   *ort.DynamicAdvancedSession
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

	// Initialize ONNX Runtime if not already done
	if !ort.IsInitialized() {
		ort.SetSharedLibraryPath(libPath)
		if err := ort.InitializeEnvironment(); err != nil {
			return nil, fmt.Errorf("initializing ONNX Runtime: %w", err)
		}
	}

	// Load tokenizer
	tokenizer, err := NewBPETokenizer(modelDir)
	if err != nil {
		return nil, fmt.Errorf("loading tokenizer: %w", err)
	}

	// Create session — only request last_hidden_state output (skip KV cache)
	modelPath := filepath.Join(modelDir, "model_quantized.onnx")
	inputNames := []string{"input_ids", "attention_mask", "position_ids"}
	outputNames := []string{"last_hidden_state"}

	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("creating session options: %w", err)
	}
	defer opts.Destroy()

	// Use single thread for inference (embedding is not latency-critical)
	opts.SetIntraOpNumThreads(2)
	opts.SetInterOpNumThreads(1)

	session, err := ort.NewDynamicAdvancedSession(modelPath,
		inputNames, outputNames, opts)
	if err != nil {
		return nil, fmt.Errorf("creating ONNX session: %w", err)
	}

	return &ONNXEmbedder{
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
	shape := ort.Shape{1, seqLen}
	inputIDsTensor, err := ort.NewTensor(shape, inputIDs)
	if err != nil {
		return nil, fmt.Errorf("creating input_ids tensor: %w", err)
	}
	defer inputIDsTensor.Destroy()

	maskTensor, err := ort.NewTensor(shape, attentionMask)
	if err != nil {
		return nil, fmt.Errorf("creating attention_mask tensor: %w", err)
	}
	defer maskTensor.Destroy()

	posIDsTensor, err := ort.NewTensor(shape, positionIDs)
	if err != nil {
		return nil, fmt.Errorf("creating position_ids tensor: %w", err)
	}
	defer posIDsTensor.Destroy()

	// Run inference (session is not thread-safe)
	e.mu.Lock()
	inputs := []ort.Value{inputIDsTensor, maskTensor, posIDsTensor}
	outputs := []ort.Value{nil} // let ONNX Runtime allocate
	err = e.session.Run(inputs, outputs)
	e.mu.Unlock()

	if err != nil {
		// Clean up any partially allocated output tensors
		for _, o := range outputs {
			if o != nil {
				o.Destroy()
			}
		}
		return nil, fmt.Errorf("ONNX inference: %w", err)
	}

	// Extract last_hidden_state [1, seqLen, hiddenDim]
	outputTensor, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		// Clean up non-nil outputs that aren't the expected type
		for _, o := range outputs {
			if o != nil {
				o.Destroy()
			}
		}
		return nil, fmt.Errorf("unexpected output type (expected float32 tensor)")
	}
	defer outputTensor.Destroy()

	hiddenData := outputTensor.GetData()

	// Derive hidden dimension from output data size instead of hardcoding.
	// Output shape is [1, seqLen, hiddenDim], so hiddenDim = len(data) / seqLen.
	hiddenDim := 896 // default fallback for Qwen2 model
	if seqLen > 0 && len(hiddenData) > 0 {
		computed := len(hiddenData) / int(seqLen)
		if computed > 0 {
			hiddenDim = computed
		}
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

// Close destroys the ONNX session and frees resources.
func (e *ONNXEmbedder) Close() error {
	if e.session != nil {
		return e.session.Destroy()
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
