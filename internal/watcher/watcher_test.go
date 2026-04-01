package watcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/naman/qb-context/internal/types"
)

// helper that drains exactly one event from the watcher within a timeout,
// failing the test if nothing arrives.
func expectEvent(t *testing.T, w *Watcher, timeout time.Duration) types.FileEvent {
	t.Helper()
	select {
	case ev := <-w.Events():
		return ev
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for file event")
		return types.FileEvent{} // unreachable
	}
}

// helper that asserts no events are emitted within a given duration.
func expectNoEvent(t *testing.T, w *Watcher, dur time.Duration) {
	t.Helper()
	select {
	case ev := <-w.Events():
		t.Fatalf("unexpected event: path=%q action=%d", ev.Path, ev.Action)
	case <-time.After(dur):
		// good — nothing arrived
	}
}

// newTestWatcher creates a watcher on dir with a short debounce.
func newTestWatcher(t *testing.T, dir string, debounce time.Duration) *Watcher {
	t.Helper()
	w, err := New(dir, debounce, []string{".git", "node_modules"})
	if err != nil {
		t.Fatalf("New watcher: %v", err)
	}
	if err := w.Start(); err != nil {
		t.Fatalf("Start watcher: %v", err)
	}
	t.Cleanup(func() {
		w.Stop()
	})
	return w
}

func TestWatcher_FileCreated(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(t, dir, 50*time.Millisecond)

	// Create a watchable file (.go extension)
	filePath := filepath.Join(dir, "hello.go")
	if err := os.WriteFile(filePath, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ev := expectEvent(t, w, 3*time.Second)
	if ev.Action != types.FileEventCreated {
		t.Errorf("expected FileEventCreated, got %d", ev.Action)
	}
	if ev.Path != "hello.go" {
		t.Errorf("expected path 'hello.go', got %q", ev.Path)
	}
}

func TestWatcher_FileModified(t *testing.T) {
	dir := t.TempDir()

	// Pre-create file before starting the watcher so the Create event doesn't fire.
	filePath := filepath.Join(dir, "hello.go")
	if err := os.WriteFile(filePath, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t, dir, 50*time.Millisecond)

	// Modify the file
	if err := os.WriteFile(filePath, []byte("package main\n\nfunc init() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ev := expectEvent(t, w, 3*time.Second)
	if ev.Action != types.FileEventModified {
		t.Errorf("expected FileEventModified, got %d", ev.Action)
	}
}

func TestWatcher_FileDeleted(t *testing.T) {
	dir := t.TempDir()

	filePath := filepath.Join(dir, "hello.go")
	if err := os.WriteFile(filePath, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t, dir, 50*time.Millisecond)

	// Delete the file
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}

	ev := expectEvent(t, w, 3*time.Second)
	if ev.Action != types.FileEventDeleted {
		t.Errorf("expected FileEventDeleted, got %d", ev.Action)
	}
}

func TestWatcher_Debounce(t *testing.T) {
	dir := t.TempDir()
	// Use a 200ms debounce — we'll write rapidly within this window.
	w := newTestWatcher(t, dir, 200*time.Millisecond)

	filePath := filepath.Join(dir, "hello.go")
	// Write the file once to create it
	if err := os.WriteFile(filePath, []byte("v0"), 0644); err != nil {
		t.Fatal(err)
	}

	// Rapidly overwrite the file multiple times within the debounce window
	for i := 1; i <= 10; i++ {
		if err := os.WriteFile(filePath, []byte("v"+string(rune('0'+i))), 0644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond) // well within 200ms debounce
	}

	// We should get exactly one coalesced event (the debounce window collapses them)
	ev := expectEvent(t, w, 3*time.Second)
	// The first raw event is a Create; subsequent Writes within the debounce window
	// are coalesced. The debounce logic keeps the first action (Created) if subsequent
	// actions are Writes, so this should be Created.
	if ev.Action != types.FileEventCreated {
		t.Errorf("expected FileEventCreated (coalesced), got %d", ev.Action)
	}

	// After the first event, no additional events should arrive (all writes were coalesced)
	expectNoEvent(t, w, 500*time.Millisecond)
}

func TestWatcher_NonWatchableFileIgnored(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(t, dir, 50*time.Millisecond)

	// Create a .txt file — not in the watchable set (.go, .js, .ts, etc.)
	txtPath := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(txtPath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	expectNoEvent(t, w, 500*time.Millisecond)
}

func TestWatcher_GitignoreRespected(t *testing.T) {
	dir := t.TempDir()

	// Create a .gitignore that excludes *.log.go files and a "build" directory
	gitignore := "build/\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignore), 0644); err != nil {
		t.Fatal(err)
	}

	// Create the build directory before starting the watcher
	buildDir := filepath.Join(dir, "build")
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t, dir, 50*time.Millisecond)

	// Create a .go file inside the ignored "build" directory
	ignoredFile := filepath.Join(buildDir, "gen.go")
	if err := os.WriteFile(ignoredFile, []byte("package gen\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Should NOT produce an event
	expectNoEvent(t, w, 500*time.Millisecond)

	// Now create a .go file at the root — should produce an event
	allowedFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(allowedFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ev := expectEvent(t, w, 3*time.Second)
	if ev.Path != "main.go" {
		t.Errorf("expected path 'main.go', got %q", ev.Path)
	}
}

func TestWatcher_ExcludedDirsRespected(t *testing.T) {
	dir := t.TempDir()

	// Pre-create the node_modules directory
	nmDir := filepath.Join(dir, "node_modules")
	if err := os.MkdirAll(nmDir, 0755); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t, dir, 50*time.Millisecond)

	// Create a file inside node_modules — should be excluded
	excluded := filepath.Join(nmDir, "lib.js")
	if err := os.WriteFile(excluded, []byte("module.exports = {}"), 0644); err != nil {
		t.Fatal(err)
	}

	expectNoEvent(t, w, 500*time.Millisecond)
}

func TestWatcher_StopSafety(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Stop should succeed
	if err := w.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop, the events channel is closed. Reading from it should return zero value.
	ev, ok := <-w.Events()
	if ok {
		t.Errorf("expected events channel to be closed, got event: %+v", ev)
	}

	// Creating a file after stop should not cause a panic
	filePath := filepath.Join(dir, "after_stop.go")
	if err := os.WriteFile(filePath, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Give a moment for any potential panics to manifest
	time.Sleep(100 * time.Millisecond)
}

func TestWatcher_WalkExisting(t *testing.T) {
	dir := t.TempDir()

	// Create several files with different extensions
	files := map[string]string{
		"main.go":    "package main",
		"util.js":    "function f() {}",
		"readme.md":  "# Hello",
		"style.css":  "body {}",
		"handler.ts": "export {}",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	w, err := New(dir, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	existing, err := w.WalkExisting()
	if err != nil {
		t.Fatalf("WalkExisting: %v", err)
	}

	// Should include .go, .js, .ts but NOT .md or .css
	found := make(map[string]bool)
	for _, f := range existing {
		found[f] = true
	}

	if !found["main.go"] {
		t.Error("expected main.go in walk results")
	}
	if !found["util.js"] {
		t.Error("expected util.js in walk results")
	}
	if !found["handler.ts"] {
		t.Error("expected handler.ts in walk results")
	}
	if found["readme.md"] {
		t.Error("readme.md should not be in walk results")
	}
	if found["style.css"] {
		t.Error("style.css should not be in walk results")
	}
}

func TestWatcher_FileRenamed(t *testing.T) {
	dir := t.TempDir()

	// Pre-create a file before starting the watcher
	oldPath := filepath.Join(dir, "old_name.go")
	if err := os.WriteFile(oldPath, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t, dir, 50*time.Millisecond)

	// Rename the file
	newPath := filepath.Join(dir, "new_name.go")
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}

	// A rename produces a Remove event for the old name and a Create for the new name.
	// We should get at least one event. Collect events for a short window.
	var events []types.FileEvent
	timeout := time.After(3 * time.Second)
	collecting := true
	for collecting {
		select {
		case ev := <-w.Events():
			events = append(events, ev)
			// After getting one event, briefly wait for any more
			if len(events) >= 2 {
				collecting = false
			}
		case <-timeout:
			collecting = false
		}
	}

	if len(events) == 0 {
		t.Fatal("expected at least 1 event after file rename, got 0")
	}

	// Verify we see a Deleted event for old_name.go or a Created event for new_name.go
	hasDelete := false
	hasCreate := false
	for _, ev := range events {
		if ev.Path == "old_name.go" && ev.Action == types.FileEventDeleted {
			hasDelete = true
		}
		if ev.Path == "new_name.go" && ev.Action == types.FileEventCreated {
			hasCreate = true
		}
	}
	if !hasDelete && !hasCreate {
		t.Errorf("expected Delete(old_name.go) or Create(new_name.go) event, got: %+v", events)
	}
}

// M70: .gitignore hot-reload behavior test
func TestWatcher_GitignoreHotReload(t *testing.T) {
	dir := t.TempDir()

	// Create initial .gitignore that does NOT exclude "gen/" directory
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create the gen directory before starting the watcher
	genDir := filepath.Join(dir, "gen")
	if err := os.MkdirAll(genDir, 0755); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t, dir, 50*time.Millisecond)

	// Create a .go file in gen/ — should produce an event (not ignored)
	genFile := filepath.Join(genDir, "generated.go")
	if err := os.WriteFile(genFile, []byte("package gen\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ev := expectEvent(t, w, 3*time.Second)
	if ev.Path != filepath.Join("gen", "generated.go") {
		t.Errorf("expected path 'gen/generated.go', got %q", ev.Path)
	}

	// Now modify .gitignore to exclude gen/
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\ngen/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for the watcher to detect and reload the .gitignore
	// The watcher detects .gitignore modifications and calls reloadGitignore
	time.Sleep(500 * time.Millisecond)

	// Now modify the file in gen/ — should NOT produce an event (now ignored)
	if err := os.WriteFile(genFile, []byte("package gen\n\nfunc F() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	expectNoEvent(t, w, 1*time.Second)
}

func TestWatcher_SubdirectoryEvents(t *testing.T) {
	dir := t.TempDir()

	// Pre-create a subdirectory
	subDir := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t, dir, 50*time.Millisecond)

	// Create a file in the subdirectory
	subFile := filepath.Join(subDir, "sub.go")
	if err := os.WriteFile(subFile, []byte("package pkg\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ev := expectEvent(t, w, 3*time.Second)
	if ev.Path != filepath.Join("pkg", "sub.go") {
		t.Errorf("expected path 'pkg/sub.go', got %q", ev.Path)
	}
}
