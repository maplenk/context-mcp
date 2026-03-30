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

// Watcher monitors filesystem changes and emits deduplicated FileEvents
type Watcher struct {
	repoRoot      string
	debounceDelay time.Duration
	excludedDirs  map[string]bool
	gitignore     *ignore.GitIgnore
	fsWatcher     *fsnotify.Watcher
	events        chan types.FileEvent
	stopCh        chan struct{}
	wg            sync.WaitGroup

	// debounce state
	mu      sync.Mutex
	pending map[string]*debounceEntry
}

type debounceEntry struct {
	action types.FileEventAction
	timer  *time.Timer
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

	// Parse .gitignore if it exists
	var gi *ignore.GitIgnore
	gitignorePath := filepath.Join(repoRoot, ".gitignore")
	if _, err := os.Stat(gitignorePath); err == nil {
		gi, err = ignore.CompileIgnoreFile(gitignorePath)
		if err != nil {
			log.Printf("Warning: failed to parse .gitignore: %v", err)
		}
	}

	w := &Watcher{
		repoRoot:      repoRoot,
		debounceDelay: debounceDelay,
		excludedDirs:  excluded,
		gitignore:     gi,
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

// Stop gracefully shuts down the watcher
func (w *Watcher) Stop() error {
	close(w.stopCh)
	w.wg.Wait()

	// Cancel any pending debounce timers
	w.mu.Lock()
	for _, entry := range w.pending {
		entry.timer.Stop()
	}
	w.pending = nil
	w.mu.Unlock()

	close(w.events)
	return w.fsWatcher.Close()
}

// isExcluded checks if a path should be excluded from watching
func (w *Watcher) isExcluded(path string) bool {
	// Get the base name and relative path
	base := filepath.Base(path)

	// Check hardcoded exclusions
	if w.excludedDirs[base] {
		return true
	}

	// Check .gitignore patterns
	if w.gitignore != nil {
		rel, err := filepath.Rel(w.repoRoot, path)
		if err == nil {
			if w.gitignore.MatchesPath(rel) {
				return true
			}
		}
	}

	return false
}

// isWatchableFile returns true if the file extension is one we should parse
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

// debounce coalesces rapid events for the same file into a single event
func (w *Watcher) debounce(path string, action types.FileEventAction) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// If there's already a pending event for this file, cancel its timer and update
	if entry, exists := w.pending[path]; exists {
		entry.timer.Stop()
		// Upgrade action: if we had Create and now get Write, keep Create
		// If we had anything and now get Delete, use Delete
		if action == types.FileEventDeleted {
			entry.action = types.FileEventDeleted
		} else if entry.action != types.FileEventCreated {
			entry.action = action
		}
		// Reset the timer
		entry.timer = time.AfterFunc(w.debounceDelay, func() {
			w.flushEvent(path)
		})
		return
	}

	// Create a new pending entry
	entry := &debounceEntry{
		action: action,
	}
	entry.timer = time.AfterFunc(w.debounceDelay, func() {
		w.flushEvent(path)
	})
	w.pending[path] = entry
}

// flushEvent sends a pending event to the events channel and removes it from pending
func (w *Watcher) flushEvent(path string) {
	w.mu.Lock()
	entry, exists := w.pending[path]
	if exists {
		delete(w.pending, path)
	}
	w.mu.Unlock()

	if exists {
		w.events <- types.FileEvent{
			Path:   path,
			Action: entry.action,
		}
	}
}

// WalkExisting walks the repo and emits Created events for all existing watchable files
// This is useful for initial indexing
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
