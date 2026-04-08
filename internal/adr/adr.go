package adr

import (
	"crypto/sha256"
	"fmt"
	"io"
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

// readDoc reads a single document, truncating to maxContentBytes.
// It opens the file first, then verifies the open fd's real path is within the
// repo root, eliminating the TOCTOU window where a symlink target could change
// between resolution and read.
func (d *Discoverer) readDoc(path string) (_ *DiscoveredDoc, err error) {
	// Open the file first — all subsequent checks operate on this fd,
	// so a symlink re-target after open cannot change what we read.
	// #nosec G304 -- path is verified against the opened fd and repo root before any content is read.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("closing %s: %w", path, closeErr)
		}
	}()

	// Resolve the real path of the open file descriptor via /dev/fd or Stat.
	// On most systems, f.Name() returns the original path, so we use Fd + readlink
	// pattern or fall back to EvalSymlinks on the original path. The key guarantee
	// is that we stat the open fd and verify before reading.
	// Use the fd's actual file info to detect the real path.
	fdInfo, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat open fd: %w", err)
	}

	// Resolve the original path to get the real target for boundary checking.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}

	// Verify the resolved path matches the open fd by comparing os.FileInfo.
	// This ensures the symlink didn't change between EvalSymlinks and our open.
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat resolved path: %w", err)
	}
	if !os.SameFile(fdInfo, resolvedInfo) {
		return nil, fmt.Errorf("symlink %s changed between open and resolve (TOCTOU detected)", path)
	}

	// H22: Return error if EvalSymlinks on repoRoot fails, instead of silently
	// falling back to the raw path which causes prefix mismatches.
	resolvedRoot, err := filepath.EvalSymlinks(d.repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving repo root symlinks: %w", err)
	}
	if !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) && resolved != resolvedRoot {
		return nil, fmt.Errorf("symlink %s resolves to %s which is outside repo root %s", path, resolved, d.repoRoot)
	}

	// Read from the already-open fd — no second open/race possible.
	data, err := io.ReadAll(f)
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

	// M65: Hash only the truncated content (what we actually store), so that
	// changes beyond maxContentBytes don't trigger unnecessary re-processing.
	hash := sha256.Sum256([]byte(content))
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
