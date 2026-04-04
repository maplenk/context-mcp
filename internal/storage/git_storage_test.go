package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/maplenk/context-mcp/internal/gitmeta"
)

// testStore creates a Store backed by a temp-dir SQLite database for git storage tests.
func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "test.db"), 384)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertAndGetRepoSnapshot(t *testing.T) {
	s := testStore(t)
	now := time.Now().Truncate(time.Second)

	snap := gitmeta.RepoSnapshot{
		RepoRoot:       "/home/user/project",
		HeadRef:        "main",
		HeadCommit:     "abc123def456",
		IsDetached:     false,
		IsDirty:        true,
		AheadCount:     2,
		BehindCount:    1,
		StagedFiles:    3,
		ModifiedFiles:  4,
		UntrackedFiles: 5,
		Summary:        "branch: main; 3 staged, 4 modified, 5 untracked",
		UpdatedAt:      now,
	}

	if err := s.UpsertRepoSnapshot(snap); err != nil {
		t.Fatalf("UpsertRepoSnapshot: %v", err)
	}

	got, err := s.GetRepoSnapshot("/home/user/project")
	if err != nil {
		t.Fatalf("GetRepoSnapshot: %v", err)
	}

	if got.RepoRoot != snap.RepoRoot {
		t.Errorf("RepoRoot = %q, want %q", got.RepoRoot, snap.RepoRoot)
	}
	if got.HeadRef != snap.HeadRef {
		t.Errorf("HeadRef = %q, want %q", got.HeadRef, snap.HeadRef)
	}
	if got.HeadCommit != snap.HeadCommit {
		t.Errorf("HeadCommit = %q, want %q", got.HeadCommit, snap.HeadCommit)
	}
	if got.IsDetached != snap.IsDetached {
		t.Errorf("IsDetached = %v, want %v", got.IsDetached, snap.IsDetached)
	}
	if got.IsDirty != snap.IsDirty {
		t.Errorf("IsDirty = %v, want %v", got.IsDirty, snap.IsDirty)
	}
	if got.AheadCount != snap.AheadCount {
		t.Errorf("AheadCount = %d, want %d", got.AheadCount, snap.AheadCount)
	}
	if got.BehindCount != snap.BehindCount {
		t.Errorf("BehindCount = %d, want %d", got.BehindCount, snap.BehindCount)
	}
	if got.StagedFiles != snap.StagedFiles {
		t.Errorf("StagedFiles = %d, want %d", got.StagedFiles, snap.StagedFiles)
	}
	if got.ModifiedFiles != snap.ModifiedFiles {
		t.Errorf("ModifiedFiles = %d, want %d", got.ModifiedFiles, snap.ModifiedFiles)
	}
	if got.UntrackedFiles != snap.UntrackedFiles {
		t.Errorf("UntrackedFiles = %d, want %d", got.UntrackedFiles, snap.UntrackedFiles)
	}
	if got.Summary != snap.Summary {
		t.Errorf("Summary = %q, want %q", got.Summary, snap.Summary)
	}
	if !got.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, now)
	}
}

func TestUpsertRepoSnapshot_Update(t *testing.T) {
	s := testStore(t)
	now := time.Now().Truncate(time.Second)

	snap1 := gitmeta.RepoSnapshot{
		RepoRoot:   "/home/user/project",
		HeadRef:    "main",
		HeadCommit: "aaa111",
		UpdatedAt:  now,
	}
	if err := s.UpsertRepoSnapshot(snap1); err != nil {
		t.Fatalf("first UpsertRepoSnapshot: %v", err)
	}

	snap2 := gitmeta.RepoSnapshot{
		RepoRoot:   "/home/user/project",
		HeadRef:    "develop",
		HeadCommit: "bbb222",
		IsDirty:    true,
		UpdatedAt:  now.Add(time.Minute),
	}
	if err := s.UpsertRepoSnapshot(snap2); err != nil {
		t.Fatalf("second UpsertRepoSnapshot: %v", err)
	}

	got, err := s.GetRepoSnapshot("/home/user/project")
	if err != nil {
		t.Fatalf("GetRepoSnapshot: %v", err)
	}
	if got.HeadRef != "develop" {
		t.Errorf("HeadRef = %q, want %q", got.HeadRef, "develop")
	}
	if got.HeadCommit != "bbb222" {
		t.Errorf("HeadCommit = %q, want %q", got.HeadCommit, "bbb222")
	}
	if !got.IsDirty {
		t.Error("IsDirty should be true after update")
	}
}

func TestGetRepoSnapshot_NotFound(t *testing.T) {
	s := testStore(t)

	_, err := s.GetRepoSnapshot("/nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestUpsertGitCommits(t *testing.T) {
	s := testStore(t)
	now := time.Now().Truncate(time.Second)

	commits := []gitmeta.CommitInfo{
		{
			Hash:        "commit1",
			AuthorName:  "Alice",
			AuthorEmail: "alice@test.com",
			AuthorTime:  now,
			Subject:     "First commit",
			Body:        "Detailed description",
			IsMerge:     false,
			FirstParent: "",
		},
		{
			Hash:        "commit2",
			AuthorName:  "Bob",
			AuthorEmail: "bob@test.com",
			AuthorTime:  now.Add(time.Hour),
			Subject:     "Second commit",
			Body:        "",
			IsMerge:     true,
			FirstParent: "commit1",
		},
	}

	if err := s.UpsertGitCommits(commits); err != nil {
		t.Fatalf("UpsertGitCommits: %v", err)
	}

	// Verify with direct query
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM git_commits`).Scan(&count); err != nil {
		t.Fatalf("counting commits: %v", err)
	}
	if count != 2 {
		t.Errorf("commit count = %d, want 2", count)
	}

	// Verify specific commit
	var subject string
	var isMerge int
	if err := s.db.QueryRow(`SELECT subject, is_merge FROM git_commits WHERE commit_hash = ?`, "commit2").
		Scan(&subject, &isMerge); err != nil {
		t.Fatalf("querying commit2: %v", err)
	}
	if subject != "Second commit" {
		t.Errorf("subject = %q, want %q", subject, "Second commit")
	}
	if isMerge != 1 {
		t.Errorf("is_merge = %d, want 1", isMerge)
	}
}

func TestUpsertGitCommits_Dedup(t *testing.T) {
	s := testStore(t)
	now := time.Now().Truncate(time.Second)

	commit := gitmeta.CommitInfo{
		Hash:       "dedup_hash",
		AuthorName: "Alice",
		AuthorTime: now,
		Subject:    "Original subject",
	}

	if err := s.UpsertGitCommits([]gitmeta.CommitInfo{commit}); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Insert again with different subject — ON CONFLICT DO NOTHING should keep original
	commit.Subject = "Updated subject"
	if err := s.UpsertGitCommits([]gitmeta.CommitInfo{commit}); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	var subject string
	if err := s.db.QueryRow(`SELECT subject FROM git_commits WHERE commit_hash = ?`, "dedup_hash").Scan(&subject); err != nil {
		t.Fatalf("querying: %v", err)
	}
	if subject != "Original subject" {
		t.Errorf("subject = %q, want %q (DO NOTHING should preserve original)", subject, "Original subject")
	}
}

func TestUpsertGitCommits_Empty(t *testing.T) {
	s := testStore(t)
	if err := s.UpsertGitCommits(nil); err != nil {
		t.Fatalf("UpsertGitCommits(nil): %v", err)
	}
}

func TestUpsertFileHistory(t *testing.T) {
	s := testStore(t)
	now := time.Now().Truncate(time.Second)

	changes := []gitmeta.FileChange{
		{
			FilePath:   "main.go",
			CommitHash: "aaa",
			ChangeType: "modified",
			CommitTime: now,
			Summary:    "Fix bug in main",
		},
		{
			FilePath:   "util.go",
			CommitHash: "bbb",
			ChangeType: "added",
			CommitTime: now.Add(time.Hour),
			Summary:    "Add utility functions",
		},
		{
			FilePath:   "main.go",
			CommitHash: "bbb",
			ChangeType: "modified",
			CommitTime: now.Add(time.Hour),
			Summary:    "Update main for utils",
		},
	}

	if err := s.UpsertFileHistory(changes); err != nil {
		t.Fatalf("UpsertFileHistory: %v", err)
	}

	// Verify count
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM git_file_history`).Scan(&count); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != 3 {
		t.Errorf("file history count = %d, want 3", count)
	}

	// Verify specific entry
	var changeType string
	if err := s.db.QueryRow(`SELECT change_type FROM git_file_history WHERE file_path = ? AND commit_hash = ?`,
		"util.go", "bbb").Scan(&changeType); err != nil {
		t.Fatalf("querying: %v", err)
	}
	if changeType != "added" {
		t.Errorf("change_type = %q, want %q", changeType, "added")
	}
}

func TestUpsertFileHistory_Empty(t *testing.T) {
	s := testStore(t)
	if err := s.UpsertFileHistory(nil); err != nil {
		t.Fatalf("UpsertFileHistory(nil): %v", err)
	}
}

func TestUpsertFileIntents(t *testing.T) {
	s := testStore(t)
	now := time.Now().Truncate(time.Second)

	intents := []gitmeta.FileIntent{
		{
			FilePath:       "main.go",
			IntentText:     "Main entry point with CLI parsing",
			SourceHash:     "hash1",
			CommitCount:    5,
			LastCommitHash: "commit5",
			LastUpdatedAt:  now,
		},
		{
			FilePath:       "util.go",
			IntentText:     "Utility functions for string processing",
			SourceHash:     "hash2",
			CommitCount:    3,
			LastCommitHash: "commit3",
			LastUpdatedAt:  now,
		},
	}

	if err := s.UpsertFileIntents(intents); err != nil {
		t.Fatalf("UpsertFileIntents: %v", err)
	}

	// Get back via GetFileIntent
	got, err := s.GetFileIntent("main.go")
	if err != nil {
		t.Fatalf("GetFileIntent: %v", err)
	}
	if got.FilePath != "main.go" {
		t.Errorf("FilePath = %q, want %q", got.FilePath, "main.go")
	}
	if got.IntentText != "Main entry point with CLI parsing" {
		t.Errorf("IntentText = %q, want %q", got.IntentText, "Main entry point with CLI parsing")
	}
	if got.CommitCount != 5 {
		t.Errorf("CommitCount = %d, want 5", got.CommitCount)
	}
	if got.LastCommitHash != "commit5" {
		t.Errorf("LastCommitHash = %q, want %q", got.LastCommitHash, "commit5")
	}
	if !got.LastUpdatedAt.Equal(now) {
		t.Errorf("LastUpdatedAt = %v, want %v", got.LastUpdatedAt, now)
	}
}

func TestUpsertFileIntents_Empty(t *testing.T) {
	s := testStore(t)
	if err := s.UpsertFileIntents(nil); err != nil {
		t.Fatalf("UpsertFileIntents(nil): %v", err)
	}
}

func TestGetFileIntent_NotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.GetFileIntent("nonexistent.go")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestGetFileIntentsByPaths(t *testing.T) {
	s := testStore(t)
	now := time.Now().Truncate(time.Second)

	intents := []gitmeta.FileIntent{
		{FilePath: "a.go", IntentText: "File A", SourceHash: "h1", CommitCount: 1, LastCommitHash: "c1", LastUpdatedAt: now},
		{FilePath: "b.go", IntentText: "File B", SourceHash: "h2", CommitCount: 2, LastCommitHash: "c2", LastUpdatedAt: now},
		{FilePath: "c.go", IntentText: "File C", SourceHash: "h3", CommitCount: 3, LastCommitHash: "c3", LastUpdatedAt: now},
	}
	if err := s.UpsertFileIntents(intents); err != nil {
		t.Fatalf("UpsertFileIntents: %v", err)
	}

	// Lookup a.go and c.go (skip b.go)
	got, err := s.GetFileIntentsByPaths([]string{"a.go", "c.go", "nonexistent.go"})
	if err != nil {
		t.Fatalf("GetFileIntentsByPaths: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	if got["a.go"] == nil || got["a.go"].IntentText != "File A" {
		t.Errorf("a.go intent mismatch: %+v", got["a.go"])
	}
	if got["c.go"] == nil || got["c.go"].IntentText != "File C" {
		t.Errorf("c.go intent mismatch: %+v", got["c.go"])
	}
	if got["nonexistent.go"] != nil {
		t.Errorf("nonexistent.go should be nil, got %+v", got["nonexistent.go"])
	}
}

func TestGetFileIntentsByPaths_Empty(t *testing.T) {
	s := testStore(t)
	got, err := s.GetFileIntentsByPaths(nil)
	if err != nil {
		t.Fatalf("GetFileIntentsByPaths(nil): %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestGetLatestStoredCommitHash(t *testing.T) {
	s := testStore(t)
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	commits := []gitmeta.CommitInfo{
		{Hash: "old_commit", AuthorName: "A", AuthorTime: base, Subject: "old"},
		{Hash: "mid_commit", AuthorName: "B", AuthorTime: base.Add(time.Hour), Subject: "mid"},
		{Hash: "new_commit", AuthorName: "C", AuthorTime: base.Add(2 * time.Hour), Subject: "new"},
	}

	if err := s.UpsertGitCommits(commits); err != nil {
		t.Fatalf("UpsertGitCommits: %v", err)
	}

	hash, err := s.GetLatestStoredCommitHash()
	if err != nil {
		t.Fatalf("GetLatestStoredCommitHash: %v", err)
	}
	if hash != "new_commit" {
		t.Errorf("latest hash = %q, want %q", hash, "new_commit")
	}
}

func TestGetLatestStoredCommitHash_Empty(t *testing.T) {
	s := testStore(t)

	hash, err := s.GetLatestStoredCommitHash()
	if err != nil {
		t.Fatalf("GetLatestStoredCommitHash: %v", err)
	}
	if hash != "" {
		t.Errorf("expected empty string, got %q", hash)
	}
}
