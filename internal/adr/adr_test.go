package adr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscover_FindsArchitectureMd(t *testing.T) {
	dir := t.TempDir()

	// Create ARCHITECTURE.md
	content := "# Architecture\n\nThis is the architecture document."
	if err := os.WriteFile(filepath.Join(dir, "ARCHITECTURE.md"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d := NewDiscoverer(dir)
	docs, err := d.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(docs) == 0 {
		t.Fatal("expected at least 1 discovered doc")
	}

	found := false
	for _, doc := range docs {
		if doc.Path == "ARCHITECTURE.md" {
			found = true
			if doc.Content != content {
				t.Errorf("content mismatch: got %q", doc.Content)
			}
			if doc.SourceHash == "" {
				t.Error("expected non-empty source hash")
			}
		}
	}
	if !found {
		t.Error("ARCHITECTURE.md not found in results")
	}
}

func TestDiscover_FindsADRDirectory(t *testing.T) {
	dir := t.TempDir()

	// Create adr/ directory with a .md file
	adrDir := filepath.Join(dir, "adr")
	if err := os.MkdirAll(adrDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(adrDir, "001-use-sqlite.md"), []byte("# Use SQLite"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Also create a non-.md file that should be skipped
	if err := os.WriteFile(filepath.Join(adrDir, "README.txt"), []byte("not markdown"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d := NewDiscoverer(dir)
	docs, err := d.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(docs) != 1 {
		t.Fatalf("expected 1 doc (only .md), got %d", len(docs))
	}
	if !strings.Contains(docs[0].Path, "001-use-sqlite.md") {
		t.Errorf("expected 001-use-sqlite.md, got %q", docs[0].Path)
	}
}

func TestDiscover_EmptyRepo(t *testing.T) {
	dir := t.TempDir()

	d := NewDiscoverer(dir)
	docs, err := d.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected 0 docs for empty repo, got %d", len(docs))
	}
}

func TestDiscover_SymlinkOutsideRepo(t *testing.T) {
	repoDir := t.TempDir()
	outsideDir := t.TempDir()

	// Create a real file outside the repo
	outsideFile := filepath.Join(outsideDir, "ARCHITECTURE.md")
	if err := os.WriteFile(outsideFile, []byte("# Outside"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Create a symlink inside the repo pointing outside
	symlinkPath := filepath.Join(repoDir, "ARCHITECTURE.md")
	if err := os.Symlink(outsideFile, symlinkPath); err != nil {
		t.Skipf("Cannot create symlinks on this platform: %v", err)
	}

	d := NewDiscoverer(repoDir)
	docs, err := d.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// The symlink pointing outside the repo should be skipped
	for _, doc := range docs {
		if doc.Path == "ARCHITECTURE.md" {
			t.Error("symlink pointing outside repo should not be discovered")
		}
	}
}

func TestDiscover_MaxChars(t *testing.T) {
	dir := t.TempDir()

	// Create a large file (> 8000 chars)
	content := strings.Repeat("x", 10000)
	if err := os.WriteFile(filepath.Join(dir, "ARCHITECTURE.md"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d := NewDiscoverer(dir)
	docs, err := d.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(docs) == 0 {
		t.Fatal("expected at least 1 doc")
	}
	if len(docs[0].Content) != maxContentBytes {
		t.Errorf("expected content truncated to %d, got %d", maxContentBytes, len(docs[0].Content))
	}
}
