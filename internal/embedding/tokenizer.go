package embedding

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// unknownTokenCount tracks total unknown tokens dropped across all Encode calls.
// Used for periodic warning logging to avoid excessive log spam.
var unknownTokenCount atomic.Int64

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

	// M49: Lazily-built reverse vocab map (id → token string), cached after first use.
	reverseVocab     map[int]string
	reverseVocabOnce sync.Once
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
		vocab:  tj.Model.Vocab,
		maxLen: 512, // sensible default for embedding
		// M55: Defaults are Qwen2-specific (151643). Overridden below if
		// tokenizer.json contains <|endoftext|> or <|padding|> in added_tokens.
		eosID: 151643,
		padID: 151643,
	}

	// Add special tokens to vocab and detect EOS/PAD token IDs from config
	for _, at := range tj.AddedTokens {
		t.vocab[at.Content] = at.ID
		switch at.Content {
		case "<|endoftext|>":
			t.eosID = at.ID
			// Default PAD to EOS unless a dedicated pad token is found
			t.padID = at.ID
		case "<|padding|>", "<pad>", "[PAD]":
			t.padID = at.ID
		}
	}

	// Parse merges — supports two formats:
	// 1. Array format: [["a", "b"], ...] (newer HuggingFace)
	// 2. String format: ["a b", ...] (older HuggingFace)
	t.merges = make([]bpeMerge, 0, len(tj.Model.Merges))
	t.mergeRank = make(map[bpeMerge]int, len(tj.Model.Merges))
	var skippedMerges int
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
					// M53: Log warning for unparseable merge rules
					skippedMerges++
					log.Printf("Warning: skipping unparseable merge rule at index %d: %s", i, string(raw))
					continue
				}
				merge = bpeMerge{a: parts[0], b: parts[1]}
			} else {
				// M53: Log warning for unparseable merge rules
				skippedMerges++
				log.Printf("Warning: skipping unparseable merge rule at index %d: %s", i, string(raw))
				continue
			}
		}

		t.merges = append(t.merges, merge)
		t.mergeRank[merge] = i
	}
	if skippedMerges > 0 {
		log.Printf("Warning: skipped %d unparseable merge rules out of %d total", skippedMerges, len(tj.Model.Merges))
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
			} else {
				// Unknown token: count and periodically warn (avoid log spam)
				count := unknownTokenCount.Add(1)
				if count == 1 || count%1000 == 0 {
					log.Printf("Warning: BPE tokenizer dropped unknown token (total dropped: %d)", count)
				}
			}
		}

		if t.maxLen > 0 && len(ids) >= t.maxLen {
			ids = ids[:t.maxLen]
			break
		}
	}

	return ids
}

// EncodeWithSpecial tokenizes text and wraps with model-appropriate special tokens.
// For this causal embedding model, no BOS is used. EOS is always appended because
// the Qwen2 model produces embeddings at the EOS position during last-token pooling.
func (t *BPETokenizer) EncodeWithSpecial(text string) (inputIDs, attentionMask []int64) {
	ids := t.Encode(text)
	if len(ids) == 0 {
		// Return at least one token (EOS only)
		ids = []int{t.eosID}
	} else {
		// Ensure room for EOS token within maxLen
		if t.maxLen > 0 && len(ids) >= t.maxLen {
			ids = ids[:t.maxLen-1]
		}
		ids = append(ids, t.eosID) // Append EOS for last-token pooling
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

	// M51: Iteratively apply the highest-priority (lowest-rank) merge.
	// Merge ALL occurrences of the best pair per iteration to reduce
	// complexity from O(n^2) to O(n*k) where k = number of merge rounds.
	for len(tokens) > 1 {
		// Find the merge with the lowest rank among all adjacent pairs
		bestRank := len(t.merges) // sentinel: higher than any valid rank
		var bestMerge bpeMerge

		for i := 0; i < len(tokens)-1; i++ {
			m := bpeMerge{a: tokens[i], b: tokens[i+1]}
			if rank, ok := t.mergeRank[m]; ok && rank < bestRank {
				bestRank = rank
				bestMerge = m
			}
		}

		if bestRank == len(t.merges) {
			break // no more applicable merges
		}

		// Apply the merge at ALL positions where the best pair occurs
		newTokens := make([]string, 0, len(tokens))
		i := 0
		for i < len(tokens) {
			if i < len(tokens)-1 && tokens[i] == bestMerge.a && tokens[i+1] == bestMerge.b {
				newTokens = append(newTokens, tokens[i]+tokens[i+1])
				i += 2
			} else {
				newTokens = append(newTokens, tokens[i])
				i++
			}
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
// M49: The reverse vocab map is built lazily on first call and cached.
func (t *BPETokenizer) DecodeTokenIDs(ids []int) string {
	t.reverseVocabOnce.Do(func() {
		t.reverseVocab = make(map[int]string, len(t.vocab))
		for tok, id := range t.vocab {
			t.reverseVocab[id] = tok
		}
	})

	var byteChars []rune
	for _, id := range ids {
		tok, ok := t.reverseVocab[id]
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

	// Validate UTF-8; replace invalid bytes with U+FFFD replacement character
	if !utf8.Valid(buf) {
		return strings.ToValidUTF8(string(buf), "\uFFFD")
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
