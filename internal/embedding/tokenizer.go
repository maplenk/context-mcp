package embedding

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// BPETokenizer implements byte-level BPE tokenization compatible with
// HuggingFace tokenizer.json (Qwen2/GPT-style). Pure Go, no CGO deps.
type BPETokenizer struct {
	vocab      map[string]int // token string → id
	merges     []bpeMerge     // ordered merge rules
	mergeRank  map[bpeMerge]int
	byteToChar [256]rune // byte → unicode char (GPT-2 byte encoding)
	charToByte map[rune]byte
	preTokenRe *regexp.Regexp
	eosID      int
	padID      int
	maxLen     int // max sequence length (0 = no limit)
}

type bpeMerge struct {
	a, b string
}

// tokenizerJSON represents the HuggingFace tokenizer.json format (subset)
type tokenizerJSON struct {
	Model struct {
		Type   string             `json:"type"`
		Vocab  map[string]int     `json:"vocab"`
		Merges []json.RawMessage  `json:"merges"`
	} `json:"model"`
	AddedTokens []struct {
		ID      int    `json:"id"`
		Content string `json:"content"`
		Special bool   `json:"special"`
	} `json:"added_tokens"`
}

// NewBPETokenizer loads a tokenizer from a HuggingFace model directory.
// Expects tokenizer.json to be present in the directory.
func NewBPETokenizer(modelDir string) (*BPETokenizer, error) {
	tokPath := filepath.Join(modelDir, "tokenizer.json")
	data, err := os.ReadFile(tokPath)
	if err != nil {
		return nil, fmt.Errorf("reading tokenizer.json: %w", err)
	}

	var tj tokenizerJSON
	if err := json.Unmarshal(data, &tj); err != nil {
		return nil, fmt.Errorf("parsing tokenizer.json: %w", err)
	}

	if tj.Model.Type != "BPE" {
		return nil, fmt.Errorf("unsupported tokenizer type: %s (expected BPE)", tj.Model.Type)
	}

	t := &BPETokenizer{
		vocab:    tj.Model.Vocab,
		maxLen:   512, // sensible default for embedding
		eosID:    151643,
		padID:    151643,
	}

	// Add special tokens to vocab
	for _, at := range tj.AddedTokens {
		t.vocab[at.Content] = at.ID
		if at.Content == "<|endoftext|>" {
			t.eosID = at.ID
			t.padID = at.ID
		}
	}

	// Parse merges — supports two formats:
	// 1. Array format: [["a", "b"], ...] (newer HuggingFace)
	// 2. String format: ["a b", ...] (older HuggingFace)
	t.merges = make([]bpeMerge, 0, len(tj.Model.Merges))
	t.mergeRank = make(map[bpeMerge]int, len(tj.Model.Merges))
	for i, raw := range tj.Model.Merges {
		var merge bpeMerge

		// Try array format first: ["a", "b"]
		var pair []string
		if err := json.Unmarshal(raw, &pair); err == nil && len(pair) == 2 {
			merge = bpeMerge{a: pair[0], b: pair[1]}
		} else {
			// Try string format: "a b"
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				parts := strings.SplitN(s, " ", 2)
				if len(parts) != 2 {
					continue
				}
				merge = bpeMerge{a: parts[0], b: parts[1]}
			} else {
				continue
			}
		}

		t.merges = append(t.merges, merge)
		t.mergeRank[merge] = i
	}

	// Build byte-to-unicode mapping (GPT-2 style)
	t.charToByte = make(map[rune]byte, 256)
	buildByteMapping(&t.byteToChar, t.charToByte)

	// Pre-tokenization regex (Qwen2/GPT-4 pattern, adapted for Go RE2)
	// Go's regexp doesn't support negative lookahead (?!\S), so we simplify
	// the trailing-whitespace alternatives to just \s+. This is functionally
	// equivalent for embedding tokenization.
	pattern := `(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+`
	t.preTokenRe = regexp.MustCompile(pattern)

	return t, nil
}

// Encode tokenizes text and returns token IDs.
// Does NOT add special tokens (caller should add EOS if needed).
func (t *BPETokenizer) Encode(text string) []int {
	if text == "" {
		return nil
	}

	// Step 1: NFC normalize
	text = norm.NFC.String(text)

	// Step 2: Pre-tokenize using regex
	preTokens := t.preTokenRe.FindAllString(text, -1)
	if len(preTokens) == 0 {
		return nil
	}

	// Step 3: Byte-level encode each pre-token and apply BPE
	var ids []int
	for _, pt := range preTokens {
		// Convert to byte-level unicode representation
		byteStr := t.byteLevelEncode(pt)

		// Apply BPE
		merged := t.applyBPE(byteStr)

		// Look up token IDs
		for _, tok := range merged {
			if id, ok := t.vocab[tok]; ok {
				ids = append(ids, id)
			}
			// Unknown tokens are silently dropped (byte fallback not enabled)
		}

		if t.maxLen > 0 && len(ids) >= t.maxLen {
			ids = ids[:t.maxLen]
			break
		}
	}

	return ids
}

// EncodeWithSpecial tokenizes text and wraps with model-appropriate special tokens.
// For this causal embedding model, no BOS is used; we just return the raw tokens.
func (t *BPETokenizer) EncodeWithSpecial(text string) (inputIDs, attentionMask []int64) {
	ids := t.Encode(text)
	if len(ids) == 0 {
		// Return at least one token
		ids = []int{t.eosID}
	}

	inputIDs = make([]int64, len(ids))
	attentionMask = make([]int64, len(ids))
	for i, id := range ids {
		inputIDs[i] = int64(id)
		attentionMask[i] = 1
	}
	return inputIDs, attentionMask
}

// EOSID returns the end-of-sequence token ID
func (t *BPETokenizer) EOSID() int { return t.eosID }

// byteLevelEncode converts a string to its byte-level unicode representation.
// Each byte of the UTF-8 encoding is mapped to a Unicode character.
func (t *BPETokenizer) byteLevelEncode(s string) string {
	var buf strings.Builder
	buf.Grow(len(s) * 2) // conservative estimate
	for i := 0; i < len(s); i++ {
		buf.WriteRune(t.byteToChar[s[i]])
	}
	return buf.String()
}

// applyBPE applies BPE merges to a byte-level encoded string.
// Returns the final list of token strings after all applicable merges.
func (t *BPETokenizer) applyBPE(text string) []string {
	if text == "" {
		return nil
	}

	// Split into individual characters (runes)
	runes := []rune(text)
	tokens := make([]string, len(runes))
	for i, r := range runes {
		tokens[i] = string(r)
	}

	// Iteratively apply the highest-priority (lowest-rank) merge
	for len(tokens) > 1 {
		// Find the merge with the lowest rank among all adjacent pairs
		bestRank := len(t.merges) // sentinel: higher than any valid rank
		bestIdx := -1

		for i := 0; i < len(tokens)-1; i++ {
			m := bpeMerge{a: tokens[i], b: tokens[i+1]}
			if rank, ok := t.mergeRank[m]; ok && rank < bestRank {
				bestRank = rank
				bestIdx = i
			}
		}

		if bestIdx == -1 {
			break // no more applicable merges
		}

		// Apply the merge at bestIdx
		merged := tokens[bestIdx] + tokens[bestIdx+1]
		newTokens := make([]string, 0, len(tokens)-1)
		newTokens = append(newTokens, tokens[:bestIdx]...)
		newTokens = append(newTokens, merged)
		if bestIdx+2 < len(tokens) {
			newTokens = append(newTokens, tokens[bestIdx+2:]...)
		}
		tokens = newTokens
	}

	return tokens
}

// buildByteMapping builds the GPT-2 byte-to-unicode mapping.
// Printable ASCII and Latin-1 supplement bytes map to themselves as runes.
// Other bytes map to Unicode chars starting at U+0100.
func buildByteMapping(byteToChar *[256]rune, charToByte map[rune]byte) {
	// The "printable" byte ranges that map to themselves
	n := 0
	for b := 0; b < 256; b++ {
		if (b >= '!' && b <= '~') || (b >= 0xA1 && b <= 0xAC) || (b >= 0xAE && b <= 0xFF) {
			byteToChar[b] = rune(b)
			charToByte[rune(b)] = byte(b)
		} else {
			byteToChar[b] = rune(256 + n)
			charToByte[rune(256+n)] = byte(b)
			n++
		}
	}
}

// PadBatch pads a batch of token sequences to the same length.
// Returns padded input_ids, attention_mask, and position_ids (all int64).
func (t *BPETokenizer) PadBatch(inputIDs, attentionMasks [][]int64) (
	paddedIDs, paddedMasks, positionIDs [][]int64,
) {
	if len(inputIDs) == 0 {
		return nil, nil, nil
	}

	// Find max length
	maxLen := 0
	for _, ids := range inputIDs {
		if len(ids) > maxLen {
			maxLen = len(ids)
		}
	}

	batchSize := len(inputIDs)
	paddedIDs = make([][]int64, batchSize)
	paddedMasks = make([][]int64, batchSize)
	positionIDs = make([][]int64, batchSize)

	for i := 0; i < batchSize; i++ {
		paddedIDs[i] = make([]int64, maxLen)
		paddedMasks[i] = make([]int64, maxLen)
		positionIDs[i] = make([]int64, maxLen)

		seqLen := len(inputIDs[i])
		copy(paddedIDs[i], inputIDs[i])
		copy(paddedMasks[i], attentionMasks[i])

		// Fill padding
		for j := seqLen; j < maxLen; j++ {
			paddedIDs[i][j] = int64(t.padID)
			paddedMasks[i][j] = 0
		}

		// Position IDs: 0, 1, 2, ... for real tokens, 0 for padding
		for j := 0; j < seqLen; j++ {
			positionIDs[i][j] = int64(j)
		}
	}

	return paddedIDs, paddedMasks, positionIDs
}

// VocabSize returns the tokenizer vocabulary size
func (t *BPETokenizer) VocabSize() int {
	return len(t.vocab)
}

// --- Utilities for testing ---

// TokenizeToStrings is like Encode but returns the token strings instead of IDs.
// Useful for debugging and testing.
func (t *BPETokenizer) TokenizeToStrings(text string) []string {
	text = norm.NFC.String(text)
	preTokens := t.preTokenRe.FindAllString(text, -1)
	if len(preTokens) == 0 {
		return nil
	}

	var result []string
	for _, pt := range preTokens {
		byteStr := t.byteLevelEncode(pt)
		merged := t.applyBPE(byteStr)
		result = append(result, merged...)
	}
	return result
}

// DecodeTokenIDs converts token IDs back to a string (best-effort).
func (t *BPETokenizer) DecodeTokenIDs(ids []int) string {
	// Build reverse vocab
	idToToken := make(map[int]string, len(t.vocab))
	for tok, id := range t.vocab {
		idToToken[id] = tok
	}

	var byteChars []rune
	for _, id := range ids {
		tok, ok := idToToken[id]
		if !ok {
			continue
		}
		byteChars = append(byteChars, []rune(tok)...)
	}

	// Convert byte-level unicode back to bytes
	var buf []byte
	for _, r := range byteChars {
		if b, ok := t.charToByte[r]; ok {
			buf = append(buf, b)
		}
	}

	// Validate UTF-8
	if utf8.Valid(buf) {
		return string(buf)
	}
	return string(buf)
}

// SortedVocab returns vocab entries sorted by ID (for debugging).
func (t *BPETokenizer) SortedVocab(limit int) []string {
	type entry struct {
		token string
		id    int
	}
	entries := make([]entry, 0, len(t.vocab))
	for tok, id := range t.vocab {
		entries = append(entries, entry{tok, id})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].id < entries[j].id
	})
	if limit > 0 && limit < len(entries) {
		entries = entries[:limit]
	}
	result := make([]string, len(entries))
	for i, e := range entries {
		result[i] = fmt.Sprintf("%d: %q", e.id, e.token)
	}
	return result
}
