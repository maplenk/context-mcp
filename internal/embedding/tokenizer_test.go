package embedding

import (
	"os"
	"path/filepath"
	"testing"
)

func getModelDir() string {
	if p := os.Getenv("QB_ONNX_MODEL"); p != "" {
		return p
	}
	if p := os.Getenv("ONNX_MODEL_DIR"); p != "" {
		return p
	}
	return ""
}

var testModelDir = getModelDir()

func hasTestModel() bool {
	if testModelDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(testModelDir, "tokenizer.json"))
	return err == nil
}

func TestBPETokenizer_Load(t *testing.T) {
	if !hasTestModel() {
		t.Skip("ONNX model not available at " + testModelDir)
	}

	tok, err := NewBPETokenizer(testModelDir)
	if err != nil {
		t.Fatalf("NewBPETokenizer: %v", err)
	}

	if tok.VocabSize() < 100000 {
		t.Errorf("vocab size too small: %d", tok.VocabSize())
	}
}

func TestBPETokenizer_Encode(t *testing.T) {
	if !hasTestModel() {
		t.Skip("ONNX model not available at " + testModelDir)
	}

	tok, err := NewBPETokenizer(testModelDir)
	if err != nil {
		t.Fatalf("NewBPETokenizer: %v", err)
	}

	tests := []struct {
		input    string
		minToks  int
		maxToks  int
	}{
		{"hello world", 1, 5},
		{"func ReadFile(path string) error {", 3, 15},
		{"class MyController extends BaseController", 3, 12},
		{"", 0, 0},
	}

	for _, tt := range tests {
		ids := tok.Encode(tt.input)
		if len(ids) < tt.minToks || len(ids) > tt.maxToks {
			t.Errorf("Encode(%q): got %d tokens, want [%d, %d]; ids=%v",
				tt.input, len(ids), tt.minToks, tt.maxToks, ids)
		}
	}
}

func TestBPETokenizer_EncodeWithSpecial(t *testing.T) {
	if !hasTestModel() {
		t.Skip("ONNX model not available at " + testModelDir)
	}

	tok, err := NewBPETokenizer(testModelDir)
	if err != nil {
		t.Fatalf("NewBPETokenizer: %v", err)
	}

	inputIDs, mask := tok.EncodeWithSpecial("hello world")
	if len(inputIDs) == 0 {
		t.Fatal("EncodeWithSpecial returned empty input_ids")
	}
	if len(inputIDs) != len(mask) {
		t.Errorf("input_ids length (%d) != mask length (%d)", len(inputIDs), len(mask))
	}
	for i, m := range mask {
		if m != 1 {
			t.Errorf("mask[%d] = %d, want 1 (no padding expected)", i, m)
		}
	}
}

func TestBPETokenizer_RoundTrip(t *testing.T) {
	if !hasTestModel() {
		t.Skip("ONNX model not available at " + testModelDir)
	}

	tok, err := NewBPETokenizer(testModelDir)
	if err != nil {
		t.Fatalf("NewBPETokenizer: %v", err)
	}

	text := "func ReadFile(path string) error {"
	ids := tok.Encode(text)
	decoded := tok.DecodeTokenIDs(ids)

	if decoded != text {
		t.Errorf("round-trip failed:\n  input:   %q\n  decoded: %q", text, decoded)
	}
}
