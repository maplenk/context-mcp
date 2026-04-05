//go:build !onnx

package embedding

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// onnxModelConfigStub mirrors the config.json fields needed for model detection.
type onnxModelConfigStub struct {
	ModelType string `json:"model_type"`
}

// DefaultDimForModel reads the model's config.json and returns the appropriate
// default embedding dimension. Returns 768 for NomicBERT (CodeRankEmbed),
// 256 for Qwen2 (Jina), and 384 (TF-IDF fallback) if detection fails.
func DefaultDimForModel(modelDir string) int {
	data, err := os.ReadFile(filepath.Join(modelDir, "config.json"))
	if err != nil {
		return 384 // TF-IDF fallback
	}
	var cfg onnxModelConfigStub
	if err := json.Unmarshal(data, &cfg); err != nil {
		return 384
	}
	if cfg.ModelType == "nomic_bert" {
		return 768 // CodeRankEmbed full dim
	}
	return 256 // Qwen2 Matryoshka default
}

// NewONNXEmbedder is a stub for builds without the onnx tag.
// Build with -tags "onnx" to enable ONNX model support.
func NewONNXEmbedder(modelDir string, dim int, libPath string) (*ONNXEmbedderStub, error) {
	return nil, fmt.Errorf("ONNX embedder not available: build with -tags \"onnx\" to enable")
}

// ONNXEmbedderStub is a placeholder type for non-ONNX builds.
type ONNXEmbedderStub struct{}

func (e *ONNXEmbedderStub) Embed(text string) ([]float32, error) {
	return nil, fmt.Errorf("ONNX embedder not available")
}
func (e *ONNXEmbedderStub) EmbedBatch(texts []string) ([][]float32, error) {
	return nil, fmt.Errorf("ONNX embedder not available")
}
func (e *ONNXEmbedderStub) Dim() int      { return 0 }
func (e *ONNXEmbedderStub) Close() error   { return nil }
