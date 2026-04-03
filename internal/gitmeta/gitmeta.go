package gitmeta

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Config holds Cold Start configuration limits
type Config struct {
	HistoryDepth     int
	PerFileCommitCap int
	MaxMessageBytes  int
	MaxIntentBytes   int
}

// RepoSnapshot captures current repository state
type RepoSnapshot struct {
	RepoRoot       string
	HeadRef        string // branch name or "HEAD"
	HeadCommit     string // commit hash
	IsDetached     bool
	IsDirty        bool
	AheadCount     int // 0 for now (no remote comparison in v1)
	BehindCount    int // 0 for now
	StagedFiles    int
	ModifiedFiles  int
	UntrackedFiles int
	Summary        string // human-readable summary
	UpdatedAt      time.Time
}

// CommitInfo holds normalized commit metadata
type CommitInfo struct {
	Hash         string
	AuthorName   string
	AuthorEmail  string
	AuthorTime   time.Time
	Subject      string
	Body         string
	TrailersJSON string // JSON-encoded trailers
	IsMerge      bool
	FirstParent  string
}

// FileChange represents a file's association with a commit
type FileChange struct {
	FilePath   string
	CommitHash string
	ChangeType string // added, modified, deleted, renamed
	CommitTime time.Time
	Summary    string // commit subject
}

// FileIntent is a compacted, retrieval-ready intent summary per file
type FileIntent struct {
	FilePath       string
	IntentText     string
	SourceHash     string // hash of inputs used to generate this intent
	CommitCount    int
	LastCommitHash string
	LastUpdatedAt  time.Time
}

// errStopIteration is a sentinel error used to break out of go-git ForEach loops.
var errStopIteration = fmt.Errorf("stop iteration")

// Extractor extracts Git metadata from a repository
type Extractor struct {
	repo     *git.Repository
	repoRoot string
	config   Config
}

// NewExtractor opens a git repository at the given path.
// Returns nil, nil if the path is not a git repository (graceful degradation).
// Returns an error if config values are invalid (HistoryDepth or MaxMessageBytes <= 0).
func NewExtractor(repoRoot string, cfg Config) (*Extractor, error) {
	if cfg.HistoryDepth <= 0 {
		return nil, fmt.Errorf("gitmeta: HistoryDepth must be positive, got %d", cfg.HistoryDepth)
	}
	if cfg.MaxMessageBytes <= 0 {
		return nil, fmt.Errorf("gitmeta: MaxMessageBytes must be positive, got %d", cfg.MaxMessageBytes)
	}

	repo, err := git.PlainOpenWithOptions(repoRoot, &git.PlainOpenOptions{
		DetectDotGit:          true,
		EnableDotGitCommonDir: true, // support linked worktrees
	})
	if err != nil {
		if err == git.ErrRepositoryNotExists {
			return nil, nil
		}
		return nil, fmt.Errorf("opening git repository: %w", err)
	}
	return &Extractor{repo: repo, repoRoot: repoRoot, config: cfg}, nil
}

// Snapshot captures the current repository state.
func (e *Extractor) Snapshot() (*RepoSnapshot, error) {
	head, err := e.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("reading HEAD: %w", err)
	}

	snap := &RepoSnapshot{
		RepoRoot:   e.repoRoot,
		HeadCommit: head.Hash().String(),
		UpdatedAt:  time.Now(),
	}

	// Determine branch vs detached HEAD
	if head.Name().IsBranch() {
		snap.HeadRef = head.Name().Short()
		snap.IsDetached = false
	} else {
		snap.HeadRef = head.Hash().String()[:12]
		snap.IsDetached = true
	}

	// Worktree status
	wt, err := e.repo.Worktree()
	if err != nil {
		log.Printf("gitmeta: cannot access worktree: %v", err)
	} else {
		status, err := wt.Status()
		if err != nil {
			log.Printf("gitmeta: cannot read worktree status: %v", err)
		} else {
			for _, s := range status {
				switch {
				case s.Staging != git.Unmodified && s.Staging != git.Untracked:
					snap.StagedFiles++
				case s.Worktree == git.Untracked:
					snap.UntrackedFiles++
				case s.Worktree != git.Unmodified:
					snap.ModifiedFiles++
				}
			}
			snap.IsDirty = len(status) > 0
		}
	}

	// Build summary
	var parts []string
	if snap.IsDetached {
		parts = append(parts, fmt.Sprintf("detached HEAD at %s", snap.HeadRef))
	} else {
		parts = append(parts, fmt.Sprintf("branch: %s", snap.HeadRef))
	}
	if snap.IsDirty {
		parts = append(parts, fmt.Sprintf("%d staged, %d modified, %d untracked",
			snap.StagedFiles, snap.ModifiedFiles, snap.UntrackedFiles))
	} else {
		parts = append(parts, "clean")
	}
	snap.Summary = strings.Join(parts, "; ")

	return snap, nil
}

// RecentCommits returns the last N commits from HEAD (bounded by config.HistoryDepth).
// If maxCount <= 0, uses config.HistoryDepth.
func (e *Extractor) RecentCommits(ctx context.Context, maxCount int) ([]CommitInfo, error) {
	if maxCount <= 0 {
		maxCount = e.config.HistoryDepth
	}

	head, err := e.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("reading HEAD: %w", err)
	}

	iter, err := e.repo.Log(&git.LogOptions{
		From:  head.Hash(),
		Order: git.LogOrderCommitterTime,
	})
	if err != nil {
		return nil, fmt.Errorf("reading log: %w", err)
	}
	defer iter.Close()

	var commits []CommitInfo
	count := 0
	err = iter.ForEach(func(c *object.Commit) error {
		if ctx.Err() != nil {
			return errStopIteration
		}
		if count >= maxCount {
			return errStopIteration
		}
		count++

		ci := CommitInfo{
			Hash:        c.Hash.String(),
			AuthorName:  c.Author.Name,
			AuthorEmail: c.Author.Email,
			AuthorTime:  c.Author.When,
			IsMerge:     c.NumParents() > 1,
		}

		// Split subject/body
		msg := truncateBytes(c.Message, e.config.MaxMessageBytes)
		msgParts := strings.SplitN(msg, "\n", 2)
		ci.Subject = strings.TrimSpace(msgParts[0])
		if len(msgParts) > 1 {
			ci.Body = strings.TrimSpace(msgParts[1])
		}

		// First parent
		if c.NumParents() > 0 {
			ci.FirstParent = c.ParentHashes[0].String()
		}

		// Extract trailers (simple key: value parsing from body)
		ci.TrailersJSON = extractTrailersJSON(ci.Body)

		commits = append(commits, ci)
		return nil
	})
	if err != nil && err != errStopIteration {
		return nil, fmt.Errorf("iterating commits: %w", err)
	}

	return commits, nil
}

// FileHistory returns commit associations for the given file paths.
// Walks up to config.HistoryDepth commits and maps file changes.
// If filePaths is nil, returns history for all files encountered.
func (e *Extractor) FileHistory(ctx context.Context, filePaths map[string]bool) ([]FileChange, error) {
	head, err := e.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("reading HEAD: %w", err)
	}

	iter, err := e.repo.Log(&git.LogOptions{
		From:  head.Hash(),
		Order: git.LogOrderCommitterTime,
	})
	if err != nil {
		return nil, fmt.Errorf("reading log: %w", err)
	}
	defer iter.Close()

	// Track per-file commit counts for capping
	fileCommitCounts := make(map[string]int)
	var changes []FileChange
	commitCount := 0

	err = iter.ForEach(func(c *object.Commit) error {
		if ctx.Err() != nil {
			return errStopIteration
		}
		if commitCount >= e.config.HistoryDepth {
			return errStopIteration
		}
		commitCount++

		// Get the tree for this commit
		tree, err := c.Tree()
		if err != nil {
			return nil // skip this commit
		}

		// Get parent tree for diff
		var parentTree *object.Tree
		if c.NumParents() > 0 {
			parent, err := c.Parent(0)
			if err == nil {
				parentTree, _ = parent.Tree()
			}
		}

		// Compute diff between parent and this commit
		diffs, err := diffTrees(parentTree, tree)
		if err != nil {
			return nil // skip
		}

		subject := firstLine(c.Message)
		for _, d := range diffs {
			// If we have a filter, only include matching files
			if filePaths != nil && !filePaths[d.path] {
				continue
			}
			// Per-file cap
			if fileCommitCounts[d.path] >= e.config.PerFileCommitCap {
				continue
			}
			fileCommitCounts[d.path]++

			changes = append(changes, FileChange{
				FilePath:   d.path,
				CommitHash: c.Hash.String(),
				ChangeType: d.changeType,
				CommitTime: c.Author.When,
				Summary:    truncateBytes(subject, 200),
			})
		}

		return nil
	})
	if err != nil && err != errStopIteration {
		return nil, fmt.Errorf("iterating file history: %w", err)
	}

	return changes, nil
}

// CompactFileIntents generates compressed intent summaries per file from file changes.
// Groups changes by file, prioritizes recent and meaningful commits, and produces bounded text.
func (e *Extractor) CompactFileIntents(changes []FileChange) []FileIntent {
	// Group by file
	byFile := make(map[string][]FileChange)
	for _, c := range changes {
		byFile[c.FilePath] = append(byFile[c.FilePath], c)
	}

	var intents []FileIntent
	for filePath, fileChanges := range byFile {
		// Sort by time descending (most recent first)
		sort.Slice(fileChanges, func(i, j int) bool {
			return fileChanges[i].CommitTime.After(fileChanges[j].CommitTime)
		})

		// Filter low-signal commits
		var meaningful []FileChange
		seen := make(map[string]bool)
		for _, fc := range fileChanges {
			// Skip duplicate subjects
			normSubject := strings.ToLower(strings.TrimSpace(fc.Summary))
			if seen[normSubject] {
				continue
			}
			// Skip low-signal subjects
			if isLowSignalCommit(normSubject) {
				continue
			}
			seen[normSubject] = true
			meaningful = append(meaningful, fc)
		}

		if len(meaningful) == 0 {
			continue
		}

		// Build intent text
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("File: %s\n", filePath))
		sb.WriteString(fmt.Sprintf("Recent changes (%d commits):\n", len(meaningful)))

		for i, fc := range meaningful {
			if i >= 10 { // cap at 10 entries in text
				sb.WriteString(fmt.Sprintf("... and %d more commits\n", len(meaningful)-10))
				break
			}
			sb.WriteString(fmt.Sprintf("- [%s] %s (%s)\n",
				fc.ChangeType,
				fc.Summary,
				fc.CommitTime.Format("2006-01-02"),
			))
		}

		intentText := truncateBytes(sb.String(), e.config.MaxIntentBytes)

		// Source hash from latest commit
		lastHash := meaningful[0].CommitHash

		intents = append(intents, FileIntent{
			FilePath:       filePath,
			IntentText:     intentText,
			SourceHash:     hashString(intentText),
			CommitCount:    len(meaningful),
			LastCommitHash: lastHash,
			LastUpdatedAt:  time.Now(),
		})
	}

	// Sort for deterministic output
	sort.Slice(intents, func(i, j int) bool {
		return intents[i].FilePath < intents[j].FilePath
	})

	return intents
}

// RecentCommitsSummary returns a bounded human-readable summary of recent commits.
func (e *Extractor) RecentCommitsSummary(ctx context.Context, count int) (string, error) {
	commits, err := e.RecentCommits(ctx, count)
	if err != nil {
		return "", err
	}
	if len(commits) == 0 {
		return "No commits found.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Recent commits (%d):\n", len(commits)))
	for _, c := range commits {
		prefix := ""
		if c.IsMerge {
			prefix = "[merge] "
		}
		sb.WriteString(fmt.Sprintf("- %s %s%s (%s, %s)\n",
			c.Hash[:8],
			prefix,
			c.Subject,
			c.AuthorName,
			c.AuthorTime.Format("2006-01-02"),
		))
	}
	return sb.String(), nil
}
