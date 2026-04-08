package embedding

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// WordPieceTokenizer implements WordPiece tokenization for BERT-style models.
// Loads from HuggingFace tokenizer.json format.
type WordPieceTokenizer struct {
	vocab    map[string]int
	unkID    int
	clsID    int
	sepID    int
	padID    int
	maxLen   int
	prefix   string // continuation subword prefix, typically "##"
	maxChars int    // max characters per word before treating as [UNK]
}

type wordPieceTokenizerJSON struct {
	Model struct {
		Type                    string         `json:"type"`
		Vocab                   map[string]int `json:"vocab"`
		UNKToken                string         `json:"unk_token"`
		ContinuingSubwordPrefix string         `json:"continuing_subword_prefix"`
		MaxInputCharsPerWord    int            `json:"max_input_chars_per_word"`
	} `json:"model"`
	AddedTokens []struct {
		ID      int    `json:"id"`
		Content string `json:"content"`
		Special bool   `json:"special"`
	} `json:"added_tokens"`
}

// NewWordPieceTokenizer loads a WordPiece tokenizer from a HuggingFace model directory.
func NewWordPieceTokenizer(modelDir string) (*WordPieceTokenizer, error) {
	tokPath := filepath.Join(modelDir, "tokenizer.json")
	data, err := readLocalModelFile(tokPath)
	if err != nil {
		return nil, fmt.Errorf("reading tokenizer.json: %w", err)
	}

	var tj wordPieceTokenizerJSON
	if err := json.Unmarshal(data, &tj); err != nil {
		return nil, fmt.Errorf("parsing tokenizer.json: %w", err)
	}

	if tj.Model.Type != "WordPiece" {
		return nil, fmt.Errorf("unsupported tokenizer type: %s (expected WordPiece)", tj.Model.Type)
	}

	t := &WordPieceTokenizer{
		vocab:    tj.Model.Vocab,
		maxLen:   512,
		prefix:   "##",
		maxChars: 100,
	}

	// Override defaults from config
	if tj.Model.ContinuingSubwordPrefix != "" {
		t.prefix = tj.Model.ContinuingSubwordPrefix
	}
	if tj.Model.MaxInputCharsPerWord > 0 {
		t.maxChars = tj.Model.MaxInputCharsPerWord
	}

	// Add special tokens from added_tokens
	for _, at := range tj.AddedTokens {
		t.vocab[at.Content] = at.ID
	}

	// Resolve special token IDs
	t.unkID = t.lookupOrDefault("[UNK]", 100)
	t.clsID = t.lookupOrDefault("[CLS]", 101)
	t.sepID = t.lookupOrDefault("[SEP]", 102)
	t.padID = t.lookupOrDefault("[PAD]", 0)

	return t, nil
}

func (t *WordPieceTokenizer) lookupOrDefault(token string, defaultID int) int {
	if id, ok := t.vocab[token]; ok {
		return id
	}
	return defaultID
}

// Encode tokenizes text into token IDs without special tokens.
func (t *WordPieceTokenizer) Encode(text string) []int {
	if text == "" {
		return nil
	}

	// NFC normalize and lowercase (BERT uncased)
	text = strings.ToLower(norm.NFC.String(text))

	// Basic tokenization: split on whitespace and punctuation
	words := basicTokenize(text)

	var ids []int
	for _, word := range words {
		wordIDs := t.wordPieceTokenize(word)
		ids = append(ids, wordIDs...)
		if t.maxLen > 0 && len(ids) >= t.maxLen-2 { // reserve room for [CLS] and [SEP]
			ids = ids[:t.maxLen-2]
			break
		}
	}

	return ids
}

// EncodeWithSpecial tokenizes text and wraps with [CLS] ... [SEP].
func (t *WordPieceTokenizer) EncodeWithSpecial(text string) (inputIDs, attentionMask []int64) {
	ids := t.Encode(text)

	// Frame: [CLS] tokens... [SEP]
	framed := make([]int, 0, len(ids)+2)
	framed = append(framed, t.clsID)
	framed = append(framed, ids...)
	framed = append(framed, t.sepID)

	// Truncate to maxLen
	if t.maxLen > 0 && len(framed) > t.maxLen {
		framed = framed[:t.maxLen]
		framed[len(framed)-1] = t.sepID // ensure [SEP] at end
	}

	inputIDs = make([]int64, len(framed))
	attentionMask = make([]int64, len(framed))
	for i, id := range framed {
		inputIDs[i] = int64(id)
		attentionMask[i] = 1
	}
	return inputIDs, attentionMask
}

// wordPieceTokenize splits a single word into WordPiece subword tokens.
func (t *WordPieceTokenizer) wordPieceTokenize(word string) []int {
	runes := []rune(word)
	if len(runes) > t.maxChars {
		return []int{t.unkID}
	}

	var ids []int
	start := 0
	for start < len(runes) {
		end := len(runes)
		found := false
		for end > start {
			substr := string(runes[start:end])
			if start > 0 {
				substr = t.prefix + substr
			}
			if id, ok := t.vocab[substr]; ok {
				ids = append(ids, id)
				found = true
				start = end
				break
			}
			end--
		}
		if !found {
			return []int{t.unkID}
		}
	}

	return ids
}

// basicTokenize splits text on whitespace and punctuation.
// Each punctuation character becomes its own token.
func basicTokenize(text string) []string {
	var tokens []string
	var current strings.Builder

	for _, r := range text {
		if unicode.IsSpace(r) {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		} else if unicode.IsPunct(r) || isChinesePunct(r) {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			tokens = append(tokens, string(r))
		} else {
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	return tokens
}

// isChinesePunct returns true for CJK punctuation and symbols.
func isChinesePunct(r rune) bool {
	return (r >= 0x3000 && r <= 0x303F) || // CJK Symbols and Punctuation
		(r >= 0xFF00 && r <= 0xFFEF) // Halfwidth and Fullwidth Forms
}

// CLSID returns the [CLS] token ID.
func (t *WordPieceTokenizer) CLSID() int { return t.clsID }

// SEPID returns the [SEP] token ID.
func (t *WordPieceTokenizer) SEPID() int { return t.sepID }

// PadID returns the [PAD] token ID.
func (t *WordPieceTokenizer) PadID() int { return t.padID }

// VocabSize returns the vocabulary size.
func (t *WordPieceTokenizer) VocabSize() int { return len(t.vocab) }
