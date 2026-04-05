//go:build onnx

package embedding

import (
	"math"
	"os"
	"testing"
)

func getONNXLibPath() string {
	if p := os.Getenv("ONNX_LIB_PATH"); p != "" {
		return p
	}
	// Default macOS path
	return "/Library/Frameworks/Python.framework/Versions/3.12/lib/python3.12/site-packages/onnxruntime/capi/libonnxruntime.1.24.4.dylib"
}

var onnxLibPath = getONNXLibPath()

func hasONNXRuntime() bool {
	_, err := os.Stat(onnxLibPath)
	return err == nil
}

func TestONNXEmbedder_Basic(t *testing.T) {
	if !hasTestModel() || !hasONNXRuntime() {
		t.Skip("ONNX model or runtime not available")
	}

	emb, err := NewONNXEmbedder(testModelDir, 256, onnxLibPath)
	if err != nil {
		t.Fatalf("NewONNXEmbedder: %v", err)
	}
	defer emb.Close()

	if emb.Dim() != 256 {
		t.Errorf("Dim() = %d, want 256", emb.Dim())
	}

	vec, err := emb.Embed("func ReadFile(path string) error {")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 256 {
		t.Errorf("embedding dim = %d, want 256", len(vec))
	}

	// Check L2 normalized
	var sumSq float64
	for _, v := range vec {
		sumSq += float64(v) * float64(v)
	}
	norm := math.Sqrt(sumSq)
	if math.Abs(norm-1.0) > 1e-3 {
		t.Errorf("L2 norm = %f, want ~1.0", norm)
	}
}

func TestONNXEmbedder_Similarity(t *testing.T) {
	if !hasTestModel() || !hasONNXRuntime() {
		t.Skip("ONNX model or runtime not available")
	}

	emb, err := NewONNXEmbedder(testModelDir, 256, onnxLibPath)
	if err != nil {
		t.Fatalf("NewONNXEmbedder: %v", err)
	}
	defer emb.Close()

	// Similar code snippets should be more similar than unrelated ones
	vecA, _ := emb.Embed("func ReadFile(path string) error {")
	vecB, _ := emb.Embed("func ReadFileContents(filePath string) ([]byte, error) {")
	vecC, _ := emb.Embed("SELECT * FROM users WHERE id = ?")

	simAB := CosineSimilarity(vecA, vecB)
	simAC := CosineSimilarity(vecA, vecC)

	t.Logf("sim(ReadFile, ReadFileContents) = %.4f", simAB)
	t.Logf("sim(ReadFile, SQL) = %.4f", simAC)

	if simAB <= simAC {
		t.Errorf("expected similar code to be more similar: sim(A,B)=%.4f <= sim(A,C)=%.4f", simAB, simAC)
	}
}

func TestONNXEmbedder_InvalidDim(t *testing.T) {
	_, err := NewONNXEmbedder(testModelDir, 100, onnxLibPath)
	if err == nil {
		t.Error("expected error for invalid Matryoshka dimension 100")
	}
}

const codeRankModelDir = "../../models/CodeRankEmbed-onnx-int8"

func hasCodeRankModel() bool {
	_, err := os.Stat(codeRankModelDir + "/tokenizer.json")
	return err == nil
}

func TestONNXEmbedder_CodeRankEmbed_Basic(t *testing.T) {
	if !hasCodeRankModel() || !hasONNXRuntime() {
		t.Skip("CodeRankEmbed model or ONNX runtime not available")
	}

	emb, err := NewONNXEmbedder(codeRankModelDir, 768, onnxLibPath)
	if err != nil {
		t.Fatalf("NewONNXEmbedder: %v", err)
	}
	defer emb.Close()

	if emb.Dim() != 768 {
		t.Errorf("Dim() = %d, want 768", emb.Dim())
	}

	vec, err := emb.Embed("func ReadFile(path string) error {")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 768 {
		t.Errorf("embedding dim = %d, want 768", len(vec))
	}

	var sumSq float64
	for _, v := range vec {
		sumSq += float64(v) * float64(v)
	}
	norm := math.Sqrt(sumSq)
	if math.Abs(norm-1.0) > 1e-3 {
		t.Errorf("L2 norm = %f, want ~1.0", norm)
	}
}

func TestONNXEmbedder_CodeRankEmbed_Similarity(t *testing.T) {
	if !hasCodeRankModel() || !hasONNXRuntime() {
		t.Skip("CodeRankEmbed model or ONNX runtime not available")
	}

	emb, err := NewONNXEmbedder(codeRankModelDir, 768, onnxLibPath)
	if err != nil {
		t.Fatalf("NewONNXEmbedder: %v", err)
	}
	defer emb.Close()

	vecA, _ := emb.Embed("func ReadFile(path string) error {")
	vecB, _ := emb.Embed("func ReadFileContents(filePath string) ([]byte, error) {")
	vecC, _ := emb.Embed("SELECT * FROM users WHERE id = ?")

	simAB := CosineSimilarity(vecA, vecB)
	simAC := CosineSimilarity(vecA, vecC)

	t.Logf("sim(ReadFile, ReadFileContents) = %.4f", simAB)
	t.Logf("sim(ReadFile, SQL) = %.4f", simAC)

	if simAB <= simAC {
		t.Errorf("expected similar code to be more similar: sim(A,B)=%.4f <= sim(A,C)=%.4f", simAB, simAC)
	}
}
