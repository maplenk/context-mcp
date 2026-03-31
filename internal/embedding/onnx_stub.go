//go:build !onnx

package embedding

import "fmt"

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
