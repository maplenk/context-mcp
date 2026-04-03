package parser

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/naman/qb-context/internal/types"
)

// maxFileDocBytes is the maximum number of bytes to include in a file document node's
// content summary. Larger files are truncated to prevent memory bloat.
const maxFileDocBytes = 4096

// IsFileDocCandidate returns true if the file path matches one of the
// file document indexing patterns. This function does NOT check if the
// file is already handled by tree-sitter — the caller should check that.
func IsFileDocCandidate(relPath string) bool {
	lower := strings.ToLower(relPath)

	// Config PHP files (config/app.php, config/session.php, etc.)
	if strings.HasPrefix(lower, "config/") && strings.HasSuffix(lower, ".php") {
		return true
	}

	// Blade templates (resources/views/**/*.blade.php)
	if strings.HasSuffix(lower, ".blade.php") {
		return true
	}

	// SQL schema files (cloud_schema/**/*.sql, or any .sql in schema dirs)
	if strings.HasSuffix(lower, ".sql") {
		parts := strings.Split(lower, string(filepath.Separator))
		for _, part := range parts {
			if part == "cloud_schema" || part == "schema" {
				return true
			}
		}
	}

	return false
}

// CreateFileDocNode reads a file and creates a searchable document node
// with the file's content as a bounded text snippet.
func CreateFileDocNode(filePath string, relPath string) (*types.ASTNode, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	// Build content summary: file name tokens + bounded content snippet
	baseName := filepath.Base(relPath)

	// Take up to maxFileDocBytes of content
	snippet := string(content)
	if len(snippet) > maxFileDocBytes {
		// Truncate at a UTF-8 boundary
		snippet = snippet[:maxFileDocBytes]
		// Find last valid UTF-8 boundary
		for !utf8.ValidString(snippet) && len(snippet) > 0 {
			snippet = snippet[:len(snippet)-1]
		}
		snippet += "..."
	}

	// Clean up: remove excessive whitespace, normalize line endings
	snippet = strings.ReplaceAll(snippet, "\r\n", "\n")
	snippet = strings.ReplaceAll(snippet, "\t", " ")

	// Content sum = filename tokens + content snippet for FTS indexing
	contentSum := baseName + " " + snippet

	node := &types.ASTNode{
		ID:         types.GenerateNodeID(relPath, relPath),
		FilePath:   relPath,
		SymbolName: relPath,
		NodeType:   types.NodeTypeFile,
		StartByte:  0,
		EndByte:    uint32(len(content)),
		ContentSum: contentSum,
	}

	return node, nil
}
