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
	// Walk directory tree and add watches, respecting exclusions
	// M3: discover nested .gitignore files during the walk
	err := filepath.Walk(w.repoRoot, func(path string, info os.FileInfo, err error) error {
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

		return w.fsWatcher.Add(path)
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

// isExcluded checks if a path should be excluded from watching
func (w *Watcher) isExcluded(path string) bool {
	// Get the base name and relative path
	base := filepath.Base(path)

	// Check hardcoded exclusions (excludedDirs is immutable after construction)
	if w.excludedDirs[base] {
		return true
	}

	// M3: Check all gitignore entries (root + nested)
	rel, err := filepath.Rel(w.repoRoot, path)
	if err != nil {
		return false
	}

	// H10: Take a snapshot of gitignores under lock to avoid races
	w.mu.Lock()
	gis := make([]gitignoreEntry, len(w.gitignores))
	copy(gis, w.gitignores)
	w.mu.Unlock()

	for _, gi := range gis {
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

// isWatchableFile returns true if the file extension is one we should parse.
// Note: .md files are not watched. ADR changes require a full re-index (L18).
// Future: watch ARCHITECTURE.md, ADR.md, DESIGN.md specifically.
func isWatchableFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go", ".js", ".jsx", ".ts", ".tsx", ".php":
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
	if filepath.Base(path) == ".gitignore" && (event.Has(fsnotify.Write) || event.Has(fsnotify.Create)) {
		w.reloadGitignore(path)
		return
	}

	// If a new directory is created, start watching it (recursive watch)
	if event.Has(fsnotify.Create) {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if !w.isExcluded(path) {
				_ = w.fsWatcher.Add(path)
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
func (w *Watcher) WalkExisting() ([]string, error) {
	var files []string

	err := filepath.Walk(w.repoRoot, func(path string, info os.FileInfo, err error) error {
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

	isExcluded := func(path string) bool {
		base := filepath.Base(path)
		if excluded[base] {
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
				} else {
					continue
				}
			}
			if gi.matcher.MatchesPath(checkPath) {
				return true
			}
		}
		return false
	}

	var files []string
	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// Discover nested .gitignore files
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
			if isExcluded(path) {
				return filepath.SkipDir
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
