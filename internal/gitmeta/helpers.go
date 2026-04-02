package gitmeta

import (
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
)

// diffEntry represents a single file change in a diff
type diffEntry struct {
	path       string
	changeType string
}

// diffTrees computes the file-level diff between two trees.
// If parentTree is nil, all files in tree are considered "added".
func diffTrees(parentTree, tree *object.Tree) ([]diffEntry, error) {
	if parentTree == nil {
		// Initial commit — all files are "added"
		var entries []diffEntry
		walker := object.NewTreeWalker(tree, true, nil)
		defer walker.Close()
		for {
			name, _, err := walker.Next()
			if err != nil {
				break
			}
			entries = append(entries, diffEntry{path: name, changeType: "added"})
		}
		return entries, nil
	}

	goChanges, err := parentTree.Diff(tree)
	if err != nil {
		return nil, err
	}

	var entries []diffEntry
	for _, change := range goChanges {
		from := change.From
		to := change.To

		var path, changeType string
		switch {
		case from.Name == "" && to.Name != "":
			path = to.Name
			changeType = "added"
		case from.Name != "" && to.Name == "":
			path = from.Name
			changeType = "deleted"
		case from.Name != to.Name:
			path = to.Name
			changeType = "renamed"
		default:
			path = to.Name
			changeType = "modified"
		}
		entries = append(entries, diffEntry{path: path, changeType: changeType})
	}
	return entries, nil
}

// truncateBytes truncates s to at most maxBytes, cutting at a newline or space boundary.
func truncateBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Find a good break point
	truncated := s[:maxBytes]
	if idx := strings.LastIndexByte(truncated, '\n'); idx > maxBytes/2 {
		return truncated[:idx]
	}
	if idx := strings.LastIndexByte(truncated, ' '); idx > maxBytes/2 {
		return truncated[:idx]
	}
	return truncated
}

// firstLine returns the first line of s.
func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return strings.TrimSpace(s)
}

// isLowSignalCommit returns true for commits that are unlikely to carry useful intent.
func isLowSignalCommit(subject string) bool {
	lowSignal := []string{
		"fix lint",
		"fix linting",
		"lint fix",
		"fmt",
		"go fmt",
		"format code",
		"formatting",
		"wip",
		"work in progress",
		"temp",
		"tmp",
		"test commit",
		"initial commit",
		"merge branch",
		"merge pull request",
		"merge remote",
		"fixup",
		"squash",
		"revert",
		"bump version",
		"update version",
		"update deps",
		"update dependencies",
		"nit",
	}
	for _, ls := range lowSignal {
		if subject == ls || strings.HasPrefix(subject, ls+" ") || strings.HasPrefix(subject, ls+":") {
			return true
		}
	}
	return false
}

// extractTrailersJSON extracts key: value trailers from commit body.
// Returns empty string if none found.
func extractTrailersJSON(body string) string {
	if body == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	var trailers []string

	// Trailers are at the end of the body, key: value format
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if idx := strings.Index(line, ": "); idx > 0 {
			key := line[:idx]
			// Validate key looks like a trailer (no spaces in key, or contains hyphen)
			if !strings.Contains(key, " ") || strings.Contains(key, "-") {
				trailers = append(trailers, line)
				continue
			}
		}
		break // Non-trailer line — stop
	}

	if len(trailers) == 0 {
		return ""
	}

	// Simple JSON encoding
	var sb strings.Builder
	sb.WriteByte('{')
	for i, t := range trailers {
		parts := strings.SplitN(t, ": ", 2)
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(fmt.Sprintf("%q:%q", parts[0], parts[1]))
	}
	sb.WriteByte('}')
	return sb.String()
}

// hashString returns a simple hash for content tracking.
func hashString(s string) string {
	// Use a simple FNV hash for source tracking (not cryptographic)
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}
