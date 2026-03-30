package adr

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxContentBytes = 8000

// DiscoveredDoc represents a discovered architecture document
type DiscoveredDoc struct {
	Path       string
	Content    string
	SourceHash string
}

// Discoverer finds and reads architecture decision records in a repository
type Discoverer struct {
	repoRoot string
}

// NewDiscoverer creates a new ADR discoverer for the given repo root
func NewDiscoverer(repoRoot string) *Discoverer {
	return &Discoverer{repoRoot: repoRoot}
}

// knownFiles are the file names we look for at the repo root
var knownFiles = []string{
	"ARCHITECTURE.md",
	"ADR.md",
	"DESIGN.md",
	"architecture.md",
	"adr.md",
	"design.md",
}

// knownDirs are the directory names we scan for ADR files
var knownDirs = []string{
	"adr",
	"ADR",
	"docs/adr",
	"docs/ADR",
	"doc/adr",
	"doc/architecture",
}

// Discover walks the repository looking for architecture documents.
// It checks known file names at the root and known directories for .md files.
func (d *Discoverer) Discover() ([]DiscoveredDoc, error) {
	var docs []DiscoveredDoc
	seen := make(map[string]bool)

	// Check known files at root
	for _, name := range knownFiles {
		path := filepath.Join(d.repoRoot, name)
		doc, err := d.readDoc(path)
		if err != nil {
			continue // file doesn't exist or can't be read
		}
		if seen[doc.Path] {
			continue
		}
		seen[doc.Path] = true
		docs = append(docs, *doc)
	}

	// scannedDirInfos holds os.FileInfo for directories we've already scanned,
	// used with os.SameFile to detect duplicates on case-insensitive filesystems.
	var scannedDirInfos []os.FileInfo

	// Check known directories
	for _, dir := range knownDirs {
		dirPath := filepath.Join(d.repoRoot, dir)
		info, err := os.Stat(dirPath)
		if err != nil || !info.IsDir() {
			continue
		}

		// Skip if we've already scanned a directory that is the same inode
		// (handles case-insensitive filesystems where "adr" and "ADR" are the same dir).
		alreadyScanned := false
		for _, prev := range scannedDirInfos {
			if os.SameFile(prev, info) {
				alreadyScanned = true
				break
			}
		}
		if alreadyScanned {
			continue
		}
		scannedDirInfos = append(scannedDirInfos, info)

		entries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
				continue
			}
			path := filepath.Join(dirPath, entry.Name())
			doc, err := d.readDoc(path)
			if err != nil {
				continue
			}
			if seen[doc.Path] {
				continue
			}
			seen[doc.Path] = true
			docs = append(docs, *doc)
		}
	}

	return docs, nil
}

// readDoc reads a single document, truncating to maxContentBytes
func (d *Discoverer) readDoc(path string) (*DiscoveredDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	content := string(data)
	if len(content) > maxContentBytes {
		content = content[:maxContentBytes]
		// Ensure we don't break a multi-byte UTF-8 character
		for len(content) > 0 && !utf8.Valid([]byte(content)) {
			content = content[:len(content)-1]
		}
	}

	hash := sha256.Sum256(data)
	relPath, err := filepath.Rel(d.repoRoot, path)
	if err != nil {
		relPath = path
	}

	return &DiscoveredDoc{
		Path:       relPath,
		Content:    content,
		SourceHash: fmt.Sprintf("%x", hash),
	}, nil
}
