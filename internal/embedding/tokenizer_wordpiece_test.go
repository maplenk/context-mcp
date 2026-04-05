package embedding

import (
	"os"
	"testing"
)

func TestWordPieceTokenizer_Load(t *testing.T) {
	modelDir := "../../models/CodeRankEmbed-onnx-int8"
	if _, err := os.Stat(modelDir + "/tokenizer.json"); os.IsNotExist(err) {
		t.Skip("CodeRankEmbed model not downloaded")
	}

	tok, err := NewWordPieceTokenizer(modelDir)
	if err != nil {
		t.Fatalf("NewWordPieceTokenizer: %v", err)
	}

	if tok.VocabSize() < 30000 {
		t.Errorf("expected vocab size >= 30000, got %d", tok.VocabSize())
	}
	if tok.CLSID() != 101 {
		t.Errorf("expected CLS=101, got %d", tok.CLSID())
	}
	if tok.SEPID() != 102 {
		t.Errorf("expected SEP=102, got %d", tok.SEPID())
	}
	if tok.PadID() != 0 {
		t.Errorf("expected PAD=0, got %d", tok.PadID())
	}
}

func TestWordPieceTokenizer_Encode(t *testing.T) {
	modelDir := "../../models/CodeRankEmbed-onnx-int8"
	if _, err := os.Stat(modelDir + "/tokenizer.json"); os.IsNotExist(err) {
		t.Skip("CodeRankEmbed model not downloaded")
	}

	tok, err := NewWordPieceTokenizer(modelDir)
	if err != nil {
		t.Fatalf("NewWordPieceTokenizer: %v", err)
	}

	ids := tok.Encode("hello world")
	if len(ids) == 0 {
		t.Fatal("Encode returned empty")
	}
	t.Logf("'hello world' -> %d tokens: %v", len(ids), ids)

	// Test code input
	ids = tok.Encode("func ReadFile(path string) error {")
	if len(ids) == 0 {
		t.Fatal("Encode returned empty for code")
	}
	t.Logf("code -> %d tokens: %v", len(ids), ids)
}

func TestWordPieceTokenizer_EncodeWithSpecial(t *testing.T) {
	modelDir := "../../models/CodeRankEmbed-onnx-int8"
	if _, err := os.Stat(modelDir + "/tokenizer.json"); os.IsNotExist(err) {
		t.Skip("CodeRankEmbed model not downloaded")
	}

	tok, err := NewWordPieceTokenizer(modelDir)
	if err != nil {
		t.Fatalf("NewWordPieceTokenizer: %v", err)
	}

	inputIDs, mask := tok.EncodeWithSpecial("hello world")

	// Should start with [CLS] and end with [SEP]
	if inputIDs[0] != 101 {
		t.Errorf("expected first token [CLS]=101, got %d", inputIDs[0])
	}
	if inputIDs[len(inputIDs)-1] != 102 {
		t.Errorf("expected last token [SEP]=102, got %d", inputIDs[len(inputIDs)-1])
	}

	// All attention mask should be 1
	for i, m := range mask {
		if m != 1 {
			t.Errorf("attention_mask[%d] = %d, expected 1", i, m)
		}
	}

	t.Logf("'hello world' with special -> %d tokens", len(inputIDs))
}

func TestWordPieceTokenizer_Empty(t *testing.T) {
	modelDir := "../../models/CodeRankEmbed-onnx-int8"
	if _, err := os.Stat(modelDir + "/tokenizer.json"); os.IsNotExist(err) {
		t.Skip("CodeRankEmbed model not downloaded")
	}

	tok, err := NewWordPieceTokenizer(modelDir)
	if err != nil {
		t.Fatalf("NewWordPieceTokenizer: %v", err)
	}

	ids := tok.Encode("")
	if len(ids) != 0 {
		t.Errorf("expected empty for empty string, got %v", ids)
	}

	inputIDs, mask := tok.EncodeWithSpecial("")
	// Should still have [CLS] [SEP]
	if len(inputIDs) != 2 {
		t.Errorf("expected 2 tokens for empty with special, got %d", len(inputIDs))
	}
	if len(mask) != 2 {
		t.Errorf("expected 2 mask values, got %d", len(mask))
	}
}
