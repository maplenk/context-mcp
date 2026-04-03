package watcher

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/crackcomm/go-gitignore"
	"github.com/fsnotify/fsnotify"
	"github.com/naman/qb-context/internal/types"
)

// deviceInode uniquely identifies a file/directory across the filesystem,
// used for symlink cycle detection.
type deviceInode struct {
	dev uint64
	ino uint64
}

// symlinkWalk walks a directory tree following symlinks, with cycle detection.
// The walkFn receives the resolved (real) path and its os.FileInfo.
// If walkFn returns filepath.SkipDir for a directory, that subtree is skipped.
func symlinkWalk(root string, walkFn func(path string, info os.FileInfo, err error) error) error {
	visited := make(map[deviceInode]bool)
	visitedPaths := make(map[string]bool)
	return symlinkWalkImpl(root, visited, visitedPaths, walkFn)
}

// symlinkWalkImpl is the recursive implementation of symlinkWalk.
func symlinkWalkImpl(dir string, visited map[deviceInode]bool, visitedPaths map[string]bool, walkFn func(path string, info os.FileInfo, err error) error) error {
	// Resolve the directory itself to handle symlinked roots
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return walkFn(dir, nil, err)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return walkFn(dir, nil, err)
	}

	// Check for cycle using device+inode (Unix) or resolved path (Windows fallback)
	di, diErr := getDeviceInode(info)
	if diErr == nil {
		if visited[di] {
			return nil // cycle detected, skip
		}
		visited[di] = true
		defer func() { delete(visited, di) }()
	} else {
		// Fallback: use resolved path for cycle detection (e.g., Windows)
		if visitedPaths[resolved] {
			return nil // cycle detected, skip
		}
		visitedPaths[resolved] = true
		defer func() { delete(visitedPaths, resolved) }()
	}

	// Call walkFn for the directory itself
	err = walkFn(dir, info, nil)
	if err == filepath.SkipDir {
		return nil
	}
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		// Report the error but continue walking
		return walkFn(dir, info, err)
	}

	for _, entry := range entries {
		childPath := filepath.Join(dir, entry.Name())

		// Always use os.Stat (not Lstat) to follow symlinks
		childInfo, err := os.Stat(childPath)
		if err != nil {
			// Report error via walkFn and continue
			if walkErr := walkFn(childPath, nil, err); walkErr != nil && walkErr != filepath.SkipDir {
				return walkErr
			}
			continue
		}

		if childInfo.IsDir() {
			if err := symlinkWalkImpl(childPath, visited, visitedPaths, walkFn); err != nil {
				return err
			}
		} else {
			if err := walkFn(childPath, childInfo, nil); err != nil && err != filepath.SkipDir {
				return err
			}
		}
	}
	return nil
}

// getDeviceInode extracts device and inode numbers from a FileInfo.
// This is platform-specific; see symlink_unix.go / symlink_windows.go.
func getDeviceInode(info os.FileInfo) (deviceInode, error) {
	return platformDeviceInode(info)
}

// gitignoreEntry pairs a compiled gitignore matcher with the directory it was found in.
type gitignoreEntry struct {
	matcher *ignore.GitIgnore
	baseDir string // relative to repoRoot, "." for root
}

// Watcher monitors filesystem changes and emits deduplicated FileEvents
type Watcher struct {
	repoRoot      string
	debounceDelay time.Duration
	excludedDirs  map[string]bool
	gitignores    []gitignoreEntry // M3: supports nested .gitignore files
	fsWatcher     *fsnotify.Watcher
	events        chan types.FileEvent
	stopCh        chan struct{}
	wg            sync.WaitGroup
	stopOnce      sync.Once // C15: prevent double-close panic

	// debounce state
	mu      sync.Mutex
	pending map[string]*debounceEntry
	stopped bool
	genSeq  uint64 // H19: monotonic generation counter for debounce
}

type debounceEntry struct {
	action     types.FileEventAction
	timer      *time.Timer
	generation uint64 // H19: invalidates stale timer callbacks
}

// New creates a new Watcher for the given repo root
func New(repoRoot string, debounceDelay time.Duration, excludedDirs []string) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	excluded := make(map[string]bool)
	for _, d := range excludedDirs {
		excluded[d] = true
	}

	// Parse root .gitignore if it exists
	var gitignores []gitignoreEntry
	gitignorePath := filepath.Join(repoRoot, ".gitignore")
	if _, err := os.Stat(gitignorePath); err == nil {
		gi, err := ignore.CompileIgnoreFile(gitignorePath)
		if err != nil {
			log.Printf("Warning: failed to parse .gitignore: %v", err)
		} else {
			gitignores = append(gitignores, gitignoreEntry{matcher: gi, baseDir: "."})
		}
	}

	w := &Watcher{
		repoRoot:      repoRoot,
		debounceDelay: debounceDelay,
		excludedDirs:  excluded,
		gitignores:    gitignores,
		fsWatcher:     fsw,
		events:        make(chan types.FileEvent, 100),
		stopCh:        make(chan struct{}),
		pending:       make(map[string]*debounceEntry),
	}

	return w, nil
}

// Events returns the channel of deduplicated file events
func (w *Watcher) Events() <-chan types.FileEvent {
	return w.events
}

// Start begins watching the repository for changes
func (w *Watcher) Start() error {
	// Walk directory tree and add watches, respecting exclusions.
	// M15: Use symlinkWalk to follow symlinks with cycle detection.
	// M3: discover nested .gitignore files during the walk
	err := symlinkWalk(w.repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible paths
		}

		if !info.IsDir() {
			return nil
		}

		// Check if this directory should be excluded
		if w.isExcluded(path) {
			return filepath.SkipDir
		}

		// M3: check for nested .gitignore (H10: protect with mutex)
		nestedGI := filepath.Join(path, ".gitignore")
		if _, err := os.Stat(nestedGI); err == nil {
			rel, relErr := filepath.Rel(w.repoRoot, path)
			if relErr == nil {
				gi, parseErr := ignore.CompileIgnoreFile(nestedGI)
				if parseErr == nil {
					w.mu.Lock()
					w.gitignores = append(w.gitignores, gitignoreEntry{matcher: gi, baseDir: rel})
					w.mu.Unlock()
				}
			}
		}

		// M58: Log fsWatcher.Add failures and continue instead of aborting the entire walk
		if addErr := w.fsWatcher.Add(path); addErr != nil {
			log.Printf("Warning: failed to watch directory %s: %v", path, addErr)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Start event processing goroutine
	w.wg.Add(1)
	go w.processEvents()

	return nil
}

// Stop gracefully shuts down the watcher.
// It is safe to call Stop() multiple times — only the first call takes effect (C15).
func (w *Watcher) Stop() error {
	var err error
	w.stopOnce.Do(func() {
		// L5: Set stopped flag and cancel pending timers BEFORE closing stopCh
		w.mu.Lock()
		w.stopped = true
		for _, entry := range w.pending {
			entry.timer.Stop()
		}
		w.pending = nil
		w.mu.Unlock()

		close(w.stopCh)
		w.wg.Wait()
		close(w.events)
		err = w.fsWatcher.Close()
	})
	return err
}

// isExcluded checks if a path should be excluded from watching.
// M78: Delegates to shared checkExcluded function to avoid duplication with WalkSourceFiles.
func (w *Watcher) isExcluded(path string) bool {
	// H10: Take a snapshot of gitignores under lock to avoid races
	w.mu.Lock()
	gis := make([]gitignoreEntry, len(w.gitignores))
	copy(gis, w.gitignores)
	w.mu.Unlock()

	return checkExcluded(path, w.repoRoot, w.excludedDirs, gis)
}

// isWatchableFile returns true if the file extension is one we should parse.
// Note: .md files are not watched. ADR changes require a full re-index (L18).
// Future: watch ARCHITECTURE.md, ADR.md, DESIGN.md specifically.
func isWatchableFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go", ".js", ".jsx", ".ts", ".tsx", ".php", ".sql":
		return true
	default:
		return false
	}
}

// processEvents reads raw fsnotify events and debounces them
func (w *Watcher) processEvents() {
	defer w.wg.Done()

	for {
		select {
		case <-w.stopCh:
			return

		case event, ok := <-w.fsWatcher.Events:
			if !ok {
				return
			}
			w.handleRawEvent(event)

		case err, ok := <-w.fsWatcher.Errors:
			if !ok {
				return
			}
			log.Printf("Watcher error: %v", err)
		}
	}
}

// handleRawEvent translates a raw fsnotify event into a debounced FileEvent
func (w *Watcher) handleRawEvent(event fsnotify.Event) {
	path := event.Name

	// M10: Detect .gitignore modifications and reload
	if filepath.Base(path) == ".gitignore" {
		if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
			w.reloadGitignore(path)
			return
		}
		// M62: Handle .gitignore deletion — clear cached rules for that directory
		if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
			w.clearGitignore(path)
			return
		}
	}

	// If a new directory is created (or a symlink to a directory), start watching
	// it and all its subdirectories recursively. M15: use symlinkWalk for symlink support.
	if event.Has(fsnotify.Create) {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if !w.isExcluded(path) {
				_ = symlinkWalk(path, func(p string, fi os.FileInfo, walkErr error) error {
					if walkErr != nil {
						return nil
					}
					if fi.IsDir() {
						if w.isExcluded(p) {
							return filepath.SkipDir
						}
						_ = w.fsWatcher.Add(p)
					}
					return nil
				})
			}
			return
		}
	}

	// Only process watchable files
	if !isWatchableFile(path) {
		return
	}

	// Skip excluded paths
	if w.isExcluded(path) {
		return
	}

	// Determine the action
	var action types.FileEventAction
	switch {
	case event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename):
		action = types.FileEventDeleted
	case event.Has(fsnotify.Create):
		action = types.FileEventCreated
	case event.Has(fsnotify.Write):
		action = types.FileEventModified
	default:
		return // Ignore chmod-only events
	}

	// Get path relative to repo root
	relPath, err := filepath.Rel(w.repoRoot, path)
	if err != nil {
		relPath = path
	}

	w.debounce(relPath, action)
}

// reloadGitignore re-parses a .gitignore file when it is modified at runtime (M10)
func (w *Watcher) reloadGitignore(path string) {
	rel, err := filepath.Rel(w.repoRoot, filepath.Dir(path))
	if err != nil {
		return
	}
	gi, err := ignore.CompileIgnoreFile(path)
	if err != nil {
		log.Printf("Warning: failed to parse updated .gitignore %s: %v", path, err)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// Replace existing entry for this directory, or add new one
	for i, entry := range w.gitignores {
		baseDir := entry.baseDir
		if baseDir == "." {
			baseDir = ""
		}
		relDir := rel
		if relDir == "." {
			relDir = ""
		}
		if baseDir == relDir {
			w.gitignores[i].matcher = gi
			log.Printf("Reloaded .gitignore: %s", path)
			return
		}
	}
	w.gitignores = append(w.gitignores, gitignoreEntry{matcher: gi, baseDir: rel})
	log.Printf("Loaded new .gitignore: %s", path)
}

// clearGitignore removes cached gitignore rules for a deleted .gitignore file (M62)
func (w *Watcher) clearGitignore(path string) {
	rel, err := filepath.Rel(w.repoRoot, filepath.Dir(path))
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, entry := range w.gitignores {
		baseDir := entry.baseDir
		if baseDir == "." {
			baseDir = ""
		}
		relDir := rel
		if relDir == "." {
			relDir = ""
		}
		if baseDir == relDir {
			// Remove this entry by swapping with the last element
			w.gitignores[i] = w.gitignores[len(w.gitignores)-1]
			w.gitignores = w.gitignores[:len(w.gitignores)-1]
			log.Printf("Cleared .gitignore rules for deleted: %s", path)
			return
		}
	}
}

// debounce coalesces rapid events for the same file into a single event
// M1: Improved coalescing — CREATE+WRITE→CREATE, anything+DELETE→DELETE
func (w *Watcher) debounce(path string, action types.FileEventAction) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// If there's already a pending event for this file, cancel its timer and update
	if entry, exists := w.pending[path]; exists {
		// H19: If Stop returns false the timer already fired; drain the channel
		// so the old callback won't interfere with the new timer.
		if !entry.timer.Stop() {
			select {
			case <-entry.timer.C:
			default:
			}
		}
		// Coalescing rules:
		// - Delete always wins (file is gone)
		// - Don't downgrade Create to Modified (CREATE+WRITE = new file being written)
		if action == types.FileEventDeleted {
			entry.action = types.FileEventDeleted
		} else if entry.action != types.FileEventCreated {
			entry.action = action
		}
		// H19: Bump generation so any in-flight old callback becomes a no-op
		w.genSeq++
		gen := w.genSeq
		entry.generation = gen
		// Reset the timer
		entry.timer = time.AfterFunc(w.debounceDelay, func() {
			w.flushEventIfCurrent(path, gen)
		})
		return
	}

	// Create a new pending entry
	w.genSeq++
	gen := w.genSeq
	entry := &debounceEntry{
		action:     action,
		generation: gen,
	}
	entry.timer = time.AfterFunc(w.debounceDelay, func() {
		w.flushEventIfCurrent(path, gen)
	})
	w.pending[path] = entry
}

// flushEventIfCurrent sends a pending event only if the generation matches the
// current entry. This prevents stale timer callbacks from emitting duplicate events (H19).
func (w *Watcher) flushEventIfCurrent(path string, gen uint64) {
	w.mu.Lock()
	entry, exists := w.pending[path]
	if exists && entry.generation != gen {
		// Stale callback — a newer debounce superseded this one.
		w.mu.Unlock()
		return
	}
	if exists {
		delete(w.pending, path)
	}
	stopped := w.stopped
	w.mu.Unlock()

	if exists && !stopped {
		select {
		case w.events <- types.FileEvent{
			Path:   path,
			Action: entry.action,
		}:
		case <-w.stopCh:
			// Watcher is shutting down; discard the event safely.
		}
	}
}

// WalkExisting walks the repo and returns all existing watchable file paths.
// This is useful for initial indexing.
// M15: follows symlinks with cycle detection for consistent symlink handling.
func (w *Watcher) WalkExisting() ([]string, error) {
	var files []string

	err := symlinkWalk(w.repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if info.IsDir() {
			if w.isExcluded(path) {
				return filepath.SkipDir
			}
			return nil
		}

		if !isWatchableFile(path) {
			return nil
		}

		if w.isExcluded(path) {
			return nil
		}

		rel, err := filepath.Rel(w.repoRoot, path)
		if err != nil {
			rel = path
		}
		files = append(files, rel)
		return nil
	})

	return files, err
}

// checkExcluded is the shared exclusion logic used by both Watcher.isExcluded
// and WalkSourceFiles (M78: extracted to avoid duplication).
func checkExcluded(path string, repoRoot string, excludedDirs map[string]bool, gitignores []gitignoreEntry) bool {
	base := filepath.Base(path)
	if excludedDirs[base] {
		return true
	}
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return false
	}
	for _, gi := range gitignores {
		if gi.matcher == nil {
			continue
		}
		var checkPath string
		if gi.baseDir == "." || gi.baseDir == "" {
			checkPath = rel
		} else {
			prefix := gi.baseDir + string(filepath.Separator)
			if strings.HasPrefix(rel, prefix) {
				checkPath, _ = filepath.Rel(gi.baseDir, rel)
			} else if rel == gi.baseDir {
				continue // the directory itself, not something inside it
			} else {
				continue // this gitignore doesn't apply
			}
		}
		if gi.matcher.MatchesPath(checkPath) {
			return true
		}
	}
	return false
}

// WalkSourceFiles walks the repo root and returns relative paths of all
// watchable source files, respecting excluded dirs and .gitignore rules.
// Unlike WalkExisting(), this does NOT require an fsnotify watcher allocation. (L4)
func WalkSourceFiles(repoRoot string, excludedDirs []string) ([]string, error) {
	excluded := make(map[string]bool)
	for _, d := range excludedDirs {
		excluded[d] = true
	}

	// Parse root .gitignore
	var gitignores []gitignoreEntry
	gitignorePath := filepath.Join(repoRoot, ".gitignore")
	if _, err := os.Stat(gitignorePath); err == nil {
		gi, err := ignore.CompileIgnoreFile(gitignorePath)
		if err == nil {
			gitignores = append(gitignores, gitignoreEntry{matcher: gi, baseDir: "."})
		}
	}

	// M78: Use shared exclusion logic
	isExcluded := func(path string) bool {
		return checkExcluded(path, repoRoot, excluded, gitignores)
	}

	// M15: Use symlinkWalk to follow symlinks with cycle detection.
	var files []string
	err := symlinkWalk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// M64: Check exclusion BEFORE loading .gitignore to avoid processing
			// nested .gitignore files inside already-excluded directories
			if isExcluded(path) {
				return filepath.SkipDir
			}
			// Discover nested .gitignore files (only in non-excluded dirs)
			nestedGI := filepath.Join(path, ".gitignore")
			if _, statErr := os.Stat(nestedGI); statErr == nil {
				rel, relErr := filepath.Rel(repoRoot, path)
				if relErr == nil {
					gi, parseErr := ignore.CompileIgnoreFile(nestedGI)
					if parseErr == nil {
						gitignores = append(gitignores, gitignoreEntry{matcher: gi, baseDir: rel})
					}
				}
			}
			return nil
		}
		if !isWatchableFile(path) {
			return nil
		}
		if isExcluded(path) {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files, err
}

// WalkSourceFilesUnder walks a specific directory (which must be inside repoRoot)
// and returns relative paths (relative to repoRoot) of all watchable source files,
// respecting excluded dirs and .gitignore rules.
// Unlike WalkSourceFiles, this only walks the subtree rooted at walkDir.
func WalkSourceFilesUnder(repoRoot string, walkDir string, excludedDirs []string) ([]string, error) {
	excluded := make(map[string]bool)
	for _, d := range excludedDirs {
		excluded[d] = true
	}

	// Parse root .gitignore
	var gitignores []gitignoreEntry
	gitignorePath := filepath.Join(repoRoot, ".gitignore")
	if _, err := os.Stat(gitignorePath); err == nil {
		gi, err := ignore.CompileIgnoreFile(gitignorePath)
		if err == nil {
			gitignores = append(gitignores, gitignoreEntry{matcher: gi, baseDir: "."})
		}
	}

	// Also parse .gitignore files between repoRoot and walkDir
	rel, err := filepath.Rel(repoRoot, walkDir)
	if err == nil && rel != "." {
		parts := strings.Split(rel, string(filepath.Separator))
		for i := range parts {
			ancestor := filepath.Join(parts[:i+1]...)
			nestedGI := filepath.Join(repoRoot, ancestor, ".gitignore")
			if _, statErr := os.Stat(nestedGI); statErr == nil {
				gi, parseErr := ignore.CompileIgnoreFile(nestedGI)
				if parseErr == nil {
					gitignores = append(gitignores, gitignoreEntry{matcher: gi, baseDir: ancestor})
				}
			}
		}
	}

	isExcluded := func(path string) bool {
		return checkExcluded(path, repoRoot, excluded, gitignores)
	}

	var files []string
	walkErr := symlinkWalk(walkDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if isExcluded(path) {
				return filepath.SkipDir
			}
			// Discover nested .gitignore files (only in non-excluded dirs)
			nestedGI := filepath.Join(path, ".gitignore")
			if _, statErr := os.Stat(nestedGI); statErr == nil {
				dirRel, relErr := filepath.Rel(repoRoot, path)
				if relErr == nil {
					gi, parseErr := ignore.CompileIgnoreFile(nestedGI)
					if parseErr == nil {
						gitignores = append(gitignores, gitignoreEntry{matcher: gi, baseDir: dirRel})
					}
				}
			}
			return nil
		}
		if !isWatchableFile(path) {
			return nil
		}
		if isExcluded(path) {
			return nil
		}
		fileRel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return nil
		}
		files = append(files, fileRel)
		return nil
	})
	return files, walkErr
}

// WalkAllFiles walks the repository and returns relative paths of ALL files,
// respecting excluded dirs and .gitignore rules, but NOT filtering by extension.
// This is used by the file document indexer to find config, Blade, and SQL files
// that are not covered by tree-sitter parsing.
func WalkAllFiles(repoRoot string, excludedDirs []string) ([]string, error) {
	excluded := make(map[string]bool)
	for _, d := range excludedDirs {
		excluded[d] = true
	}

	// Parse root .gitignore
	var gitignores []gitignoreEntry
	gitignorePath := filepath.Join(repoRoot, ".gitignore")
	if _, err := os.Stat(gitignorePath); err == nil {
		gi, err := ignore.CompileIgnoreFile(gitignorePath)
		if err == nil {
			gitignores = append(gitignores, gitignoreEntry{matcher: gi, baseDir: "."})
		}
	}

	// M78: Use shared exclusion logic
	isExcluded := func(path string) bool {
		return checkExcluded(path, repoRoot, excluded, gitignores)
	}

	var files []string
	err := symlinkWalk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if isExcluded(path) {
				return filepath.SkipDir
			}
			// Discover nested .gitignore files (only in non-excluded dirs)
			nestedGI := filepath.Join(path, ".gitignore")
			if _, statErr := os.Stat(nestedGI); statErr == nil {
				rel, relErr := filepath.Rel(repoRoot, path)
				if relErr == nil {
					gi, parseErr := ignore.CompileIgnoreFile(nestedGI)
					if parseErr == nil {
						gitignores = append(gitignores, gitignoreEntry{matcher: gi, baseDir: rel})
					}
				}
			}
			return nil
		}
		if isExcluded(path) {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files, err
}
