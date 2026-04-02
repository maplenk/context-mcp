package gitmeta

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func initTestRepo(t *testing.T) (string, *git.Repository) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return dir, repo
}

func commitFile(t *testing.T, repo *git.Repository, dir, filename, content, message string) plumbing.Hash {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(filename); err != nil {
		t.Fatal(err)
	}
	hash, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func defaultTestConfig() Config {
	return Config{
		HistoryDepth:     500,
		PerFileCommitCap: 20,
		MaxMessageBytes:  2000,
		MaxIntentBytes:   1500,
	}
}

func TestNewExtractor_NoGitRepo(t *testing.T) {
	dir := t.TempDir()
	ext, err := NewExtractor(dir, defaultTestConfig())
	if err != nil {
		t.Fatalf("expected no error for non-git dir, got: %v", err)
	}
	if ext != nil {
		t.Fatal("expected nil extractor for non-git dir")
	}
}

func TestSnapshot_NormalBranch(t *testing.T) {
	dir, repo := initTestRepo(t)
	commitFile(t, repo, dir, "file.txt", "hello", "Initial setup")

	ext, err := NewExtractor(dir, defaultTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if ext == nil {
		t.Fatal("expected non-nil extractor")
	}

	snap, err := ext.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	if snap.IsDetached {
		t.Error("expected not detached")
	}
	if snap.HeadRef != "master" {
		t.Errorf("expected branch 'master', got %q", snap.HeadRef)
	}
	if snap.HeadCommit == "" {
		t.Error("expected non-empty HeadCommit")
	}
	if snap.IsDirty {
		t.Error("expected clean worktree")
	}
	if !strings.Contains(snap.Summary, "branch: master") {
		t.Errorf("summary should mention branch, got: %s", snap.Summary)
	}
	if !strings.Contains(snap.Summary, "clean") {
		t.Errorf("summary should say clean, got: %s", snap.Summary)
	}
}

func TestSnapshot_DetachedHEAD(t *testing.T) {
	dir, repo := initTestRepo(t)
	hash := commitFile(t, repo, dir, "file.txt", "hello", "First commit")
	commitFile(t, repo, dir, "file.txt", "world", "Second commit")

	// Detach HEAD to the first commit
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{Hash: hash}); err != nil {
		t.Fatal(err)
	}

	ext, err := NewExtractor(dir, defaultTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ext.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	if !snap.IsDetached {
		t.Error("expected detached HEAD")
	}
	if !strings.Contains(snap.Summary, "detached HEAD") {
		t.Errorf("summary should mention detached, got: %s", snap.Summary)
	}
}

func TestSnapshot_DirtyWorktree(t *testing.T) {
	dir, repo := initTestRepo(t)
	commitFile(t, repo, dir, "file.txt", "hello", "Initial")

	// Create an untracked file
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	// Modify a tracked file
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}

	ext, err := NewExtractor(dir, defaultTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ext.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	if !snap.IsDirty {
		t.Error("expected dirty worktree")
	}
	if snap.UntrackedFiles < 1 {
		t.Errorf("expected at least 1 untracked file, got %d", snap.UntrackedFiles)
	}
	if snap.ModifiedFiles < 1 {
		t.Errorf("expected at least 1 modified file, got %d", snap.ModifiedFiles)
	}
}

func TestRecentCommits(t *testing.T) {
	dir, repo := initTestRepo(t)
	for i := 0; i < 5; i++ {
		commitFile(t, repo, dir, "file.txt", strings.Repeat("x", i+1), "Commit "+string(rune('A'+i)))
	}

	ext, err := NewExtractor(dir, defaultTestConfig())
	if err != nil {
		t.Fatal(err)
	}

	commits, err := ext.RecentCommits(context.Background(),3)
	if err != nil {
		t.Fatal(err)
	}

	if len(commits) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(commits))
	}

	// Most recent first
	if commits[0].Subject != "Commit E" {
		t.Errorf("expected first commit subject 'Commit E', got %q", commits[0].Subject)
	}
	if commits[2].Subject != "Commit C" {
		t.Errorf("expected last commit subject 'Commit C', got %q", commits[2].Subject)
	}
}

func TestRecentCommits_HistoryDepthCap(t *testing.T) {
	dir, repo := initTestRepo(t)
	for i := 0; i < 10; i++ {
		commitFile(t, repo, dir, "file.txt", strings.Repeat("y", i+1), "Commit "+string(rune('A'+i)))
	}

	cfg := defaultTestConfig()
	cfg.HistoryDepth = 3

	ext, err := NewExtractor(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Pass 0 to use config.HistoryDepth
	commits, err := ext.RecentCommits(context.Background(),0)
	if err != nil {
		t.Fatal(err)
	}

	if len(commits) != 3 {
		t.Fatalf("expected 3 commits (capped by HistoryDepth), got %d", len(commits))
	}
}

func TestRecentCommits_MergeFlag(t *testing.T) {
	dir, repo := initTestRepo(t)
	commitFile(t, repo, dir, "file.txt", "initial", "Initial commit on master")

	ext, err := NewExtractor(dir, defaultTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	commits, err := ext.RecentCommits(context.Background(),1)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 {
		t.Fatal("expected 1 commit")
	}
	// A normal commit should not be marked as merge
	if commits[0].IsMerge {
		t.Error("expected non-merge commit")
	}
}

func TestRecentCommits_SubjectBody(t *testing.T) {
	dir, repo := initTestRepo(t)
	commitFile(t, repo, dir, "file.txt", "content", "Add feature X\n\nThis is the body.\nMore details here.")

	ext, err := NewExtractor(dir, defaultTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	commits, err := ext.RecentCommits(context.Background(),1)
	if err != nil {
		t.Fatal(err)
	}
	if commits[0].Subject != "Add feature X" {
		t.Errorf("unexpected subject: %q", commits[0].Subject)
	}
	if !strings.Contains(commits[0].Body, "This is the body.") {
		t.Errorf("expected body to contain description, got: %q", commits[0].Body)
	}
}

func TestFileHistory(t *testing.T) {
	dir, repo := initTestRepo(t)
	commitFile(t, repo, dir, "a.go", "package a", "Create a.go")
	commitFile(t, repo, dir, "b.go", "package b", "Create b.go")
	commitFile(t, repo, dir, "a.go", "package a // updated", "Update a.go")

	ext, err := NewExtractor(dir, defaultTestConfig())
	if err != nil {
		t.Fatal(err)
	}

	changes, err := ext.FileHistory(context.Background(),nil)
	if err != nil {
		t.Fatal(err)
	}

	// Should have at least 3 file-change entries
	if len(changes) < 3 {
		t.Fatalf("expected at least 3 file changes, got %d", len(changes))
	}

	// Check that a.go has 2 changes and b.go has 1
	aCount, bCount := 0, 0
	for _, c := range changes {
		switch c.FilePath {
		case "a.go":
			aCount++
		case "b.go":
			bCount++
		}
	}
	if aCount != 2 {
		t.Errorf("expected 2 changes for a.go, got %d", aCount)
	}
	if bCount != 1 {
		t.Errorf("expected 1 change for b.go, got %d", bCount)
	}
}

func TestFileHistory_PerFileCap(t *testing.T) {
	dir, repo := initTestRepo(t)
	for i := 0; i < 10; i++ {
		commitFile(t, repo, dir, "file.go", strings.Repeat("z", i+1), "Update file "+string(rune('0'+i)))
	}

	cfg := defaultTestConfig()
	cfg.PerFileCommitCap = 3

	ext, err := NewExtractor(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}

	changes, err := ext.FileHistory(context.Background(),nil)
	if err != nil {
		t.Fatal(err)
	}

	count := 0
	for _, c := range changes {
		if c.FilePath == "file.go" {
			count++
		}
	}
	if count > 3 {
		t.Errorf("expected at most 3 changes for file.go (per-file cap), got %d", count)
	}
}

func TestFileHistory_FilteredPaths(t *testing.T) {
	dir, repo := initTestRepo(t)
	commitFile(t, repo, dir, "a.go", "package a", "Create a.go")
	commitFile(t, repo, dir, "b.go", "package b", "Create b.go")
	commitFile(t, repo, dir, "c.go", "package c", "Create c.go")

	ext, err := NewExtractor(dir, defaultTestConfig())
	if err != nil {
		t.Fatal(err)
	}

	filter := map[string]bool{"a.go": true, "c.go": true}
	changes, err := ext.FileHistory(context.Background(),filter)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range changes {
		if c.FilePath == "b.go" {
			t.Error("b.go should have been filtered out")
		}
	}

	// Should have entries for a.go and c.go
	hasA, hasC := false, false
	for _, c := range changes {
		if c.FilePath == "a.go" {
			hasA = true
		}
		if c.FilePath == "c.go" {
			hasC = true
		}
	}
	if !hasA {
		t.Error("expected changes for a.go")
	}
	if !hasC {
		t.Error("expected changes for c.go")
	}
}

func TestCompactFileIntents(t *testing.T) {
	dir, repo := initTestRepo(t)
	commitFile(t, repo, dir, "main.go", "v1", "Add main entry point")
	commitFile(t, repo, dir, "main.go", "v2", "Add error handling to main")
	commitFile(t, repo, dir, "main.go", "v3", "fix lint") // low-signal, should be filtered
	commitFile(t, repo, dir, "main.go", "v4", "Add error handling to main") // duplicate, should be filtered

	ext, err := NewExtractor(dir, defaultTestConfig())
	if err != nil {
		t.Fatal(err)
	}

	changes, err := ext.FileHistory(context.Background(),nil)
	if err != nil {
		t.Fatal(err)
	}

	intents := ext.CompactFileIntents(changes)

	// Should have exactly one intent for main.go
	found := false
	for _, intent := range intents {
		if intent.FilePath == "main.go" {
			found = true
			// Should contain the meaningful commits but not the low-signal or dupe
			if !strings.Contains(intent.IntentText, "Add main entry point") {
				t.Error("intent should contain 'Add main entry point'")
			}
			if !strings.Contains(intent.IntentText, "Add error handling") {
				t.Error("intent should contain 'Add error handling'")
			}
			if strings.Contains(strings.ToLower(intent.IntentText), "fix lint") {
				t.Error("intent should not contain low-signal 'fix lint'")
			}
			if intent.CommitCount != 2 {
				t.Errorf("expected 2 meaningful commits, got %d", intent.CommitCount)
			}
			if intent.SourceHash == "" {
				t.Error("expected non-empty source hash")
			}
		}
	}
	if !found {
		t.Error("expected intent for main.go")
	}
}

func TestCompactFileIntents_MaxIntentBytes(t *testing.T) {
	dir, repo := initTestRepo(t)
	for i := 0; i < 15; i++ {
		commitFile(t, repo, dir, "big.go", strings.Repeat("x", i+1),
			"Commit with a fairly long message that should take up space: "+strings.Repeat("detail ", 20))
	}

	cfg := defaultTestConfig()
	cfg.MaxIntentBytes = 200

	ext, err := NewExtractor(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}

	changes, err := ext.FileHistory(context.Background(),nil)
	if err != nil {
		t.Fatal(err)
	}

	intents := ext.CompactFileIntents(changes)
	for _, intent := range intents {
		if intent.FilePath == "big.go" {
			if len(intent.IntentText) > 200 {
				t.Errorf("intent text exceeds MaxIntentBytes: got %d bytes", len(intent.IntentText))
			}
		}
	}
}

func TestIsLowSignalCommit(t *testing.T) {
	tests := []struct {
		subject  string
		expected bool
	}{
		{"fix lint", true},
		{"fix lint: some details", true},
		{"fix linting", true},
		{"wip", true},
		{"wip: saving progress", true},
		{"merge branch 'feature'", true},
		{"initial commit", true},
		{"bump version", true},
		{"update deps", true},
		{"add new feature for users", false},
		{"refactor database layer", false},
		{"fix critical bug in auth", false},
		{"implement caching", false},
		{"", false},
	}

	for _, tt := range tests {
		got := isLowSignalCommit(tt.subject)
		if got != tt.expected {
			t.Errorf("isLowSignalCommit(%q) = %v, want %v", tt.subject, got, tt.expected)
		}
	}
}

func TestTruncateBytes(t *testing.T) {
	tests := []struct {
		input    string
		maxBytes int
		wantLen  bool // true means check len <= maxBytes
	}{
		{"short", 100, true},
		{"hello world foo bar baz", 10, true},
		{"line1\nline2\nline3\nline4", 15, true},
		{"", 10, true},
		{"exactly10!", 10, true},
	}

	for _, tt := range tests {
		got := truncateBytes(tt.input, tt.maxBytes)
		if len(got) > tt.maxBytes {
			t.Errorf("truncateBytes(%q, %d) = %q (len %d), exceeds max",
				tt.input, tt.maxBytes, got, len(got))
		}
	}

	// Test that short strings pass through unchanged
	short := "hello"
	if truncateBytes(short, 100) != short {
		t.Error("short string should pass through unchanged")
	}
}

func TestTruncateBytes_BreakPoints(t *testing.T) {
	// Test that truncation prefers newline boundaries
	input := "first line\nsecond line\nthird line"
	result := truncateBytes(input, 20)
	if len(result) > 20 {
		t.Errorf("result too long: %d", len(result))
	}
	// Should break at a newline if possible
	if strings.Contains(result, "\nsecond line\n") {
		t.Error("should not include full second line when truncated")
	}
}

func TestRecentCommitsSummary(t *testing.T) {
	dir, repo := initTestRepo(t)
	commitFile(t, repo, dir, "file.txt", "v1", "Add initial file")
	commitFile(t, repo, dir, "file.txt", "v2", "Update file with new logic")

	ext, err := NewExtractor(dir, defaultTestConfig())
	if err != nil {
		t.Fatal(err)
	}

	summary, err := ext.RecentCommitsSummary(context.Background(),5)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(summary, "Recent commits") {
		t.Error("summary should contain 'Recent commits'")
	}
	if !strings.Contains(summary, "Add initial file") {
		t.Error("summary should contain commit subject")
	}
	if !strings.Contains(summary, "Update file with new logic") {
		t.Error("summary should contain second commit subject")
	}
	if !strings.Contains(summary, "Test") {
		t.Error("summary should contain author name 'Test'")
	}
}

func TestExtractTrailersJSON(t *testing.T) {
	tests := []struct {
		body     string
		wantJSON bool
		contains string
	}{
		{"", false, ""},
		{"Just a body with no trailers.", false, ""},
		{"Body text.\n\nSigned-off-by: Test <test@test.com>", true, "Signed-off-by"},
		{"Body.\n\nCo-Authored-By: Alice <alice@example.com>\nReviewed-By: Bob <bob@example.com>", true, "Co-Authored-By"},
	}

	for _, tt := range tests {
		result := extractTrailersJSON(tt.body)
		if tt.wantJSON && result == "" {
			t.Errorf("expected JSON trailers for body %q, got empty", tt.body)
		}
		if !tt.wantJSON && result != "" {
			t.Errorf("expected no trailers for body %q, got %q", tt.body, result)
		}
		if tt.contains != "" && !strings.Contains(result, tt.contains) {
			t.Errorf("expected trailers to contain %q, got %q", tt.contains, result)
		}
	}
}

func TestHashString(t *testing.T) {
	h1 := hashString("hello")
	h2 := hashString("hello")
	h3 := hashString("world")

	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("different inputs should produce different hashes")
	}
	if len(h1) != 16 {
		t.Errorf("expected 16 char hex hash, got %d chars", len(h1))
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"hello\nworld", "hello"},
		{"  hello  \nworld", "hello"},
		{"", ""},
	}
	for _, tt := range tests {
		got := firstLine(tt.input)
		if got != tt.want {
			t.Errorf("firstLine(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCompactFileIntents_Deterministic(t *testing.T) {
	dir, repo := initTestRepo(t)
	commitFile(t, repo, dir, "alpha.go", "a", "Add alpha")
	commitFile(t, repo, dir, "beta.go", "b", "Add beta")
	commitFile(t, repo, dir, "gamma.go", "g", "Add gamma")

	ext, err := NewExtractor(dir, defaultTestConfig())
	if err != nil {
		t.Fatal(err)
	}

	changes, err := ext.FileHistory(context.Background(),nil)
	if err != nil {
		t.Fatal(err)
	}

	intents := ext.CompactFileIntents(changes)

	// Output should be sorted by file path
	for i := 1; i < len(intents); i++ {
		if intents[i].FilePath < intents[i-1].FilePath {
			t.Errorf("intents not sorted: %s came after %s",
				intents[i].FilePath, intents[i-1].FilePath)
		}
	}
}
