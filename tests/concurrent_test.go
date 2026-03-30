//go:build fts5

package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/naman/qb-context/internal/types"
)

// setupConcurrentEnv creates a test pipeline pre-loaded with several Go files.
func setupConcurrentEnv(t *testing.T) (*testPipeline, []string) {
	t.Helper()
	tp := newTestPipeline(t)

	// Create several source files to have a non-trivial data set
	files := []struct {
		name    string
		content string
	}{
		{"server.go", `package main

// Server handles HTTP requests
type Server struct { port int }

// Start starts the server
func (s *Server) Start() error { return nil }

// Stop stops the server
func (s *Server) Stop() error { return nil }
`},
		{"handler.go", `package main

// Handler processes requests
type Handler struct{}

// ServeHTTP handles an HTTP request
func (h *Handler) ServeHTTP() {}

// Validate validates request data
func (h *Handler) Validate() bool { return true }
`},
		{"repo.go", `package main

// Repository handles data access
type Repository struct{}

// FindByID finds a record by ID
func (r *Repository) FindByID(id string) {}

// Save persists a record
func (r *Repository) Save(data interface{}) error { return nil }

// Delete removes a record
func (r *Repository) Delete(id string) error { return nil }
`},
		{"config.go", `package main

// Config holds application configuration
type Config struct {
	Host string
	Port int
}

// Load loads configuration from disk
func Load() (*Config, error) { return nil, nil }

// Validate checks config values
func (c *Config) Validate() error { return nil }
`},
	}

	var fileNames []string
	for _, f := range files {
		writeGoFile(t, tp.repoRoot, f.name, f.content)
		tp.indexFile(t, f.name)
		fileNames = append(fileNames, f.name)
	}

	return tp, fileNames
}

func TestConcurrent_SearchDuringIndex(t *testing.T) {
	tp := newTestPipeline(t)

	// Pre-populate with one file so searches can return something
	writeGoFile(t, tp.repoRoot, "base.go", `package main

// Base type
type Base struct{}

// Run runs the base
func (b *Base) Run() {}
`)
	tp.indexFile(t, "base.go")

	var wg sync.WaitGroup
	errCh := make(chan error, 20)

	// Goroutine 1: continuously index new files
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			name := fmt.Sprintf("gen_%d.go", i)
			content := fmt.Sprintf(`package main

// Gen%d is a generated type
type Gen%d struct{}

// Do%d does something
func (g *Gen%d) Do%d() {}
`, i, i, i, i, i)
			writeGoFile(t, tp.repoRoot, name, content)
			tp.indexFile(t, name)
		}
	}()

	// Goroutine 2: continuously run searches
	wg.Add(1)
	go func() {
		defer wg.Done()
		queries := []string{"Base", "Run", "Gen", "struct", "type"}
		for i := 0; i < 10; i++ {
			q := queries[i%len(queries)]
			_, err := tp.search.Search(q, 5, nil)
			if err != nil {
				errCh <- fmt.Errorf("search %q: %w", q, err)
			}
		}
	}()

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent error: %v", err)
	}
}

func TestConcurrent_MultipleFileChanges(t *testing.T) {
	tp, _ := setupConcurrentEnv(t)

	// Simulate multiple file changes concurrently by running modifications in
	// parallel goroutines. Each goroutine modifies a different file.
	var wg sync.WaitGroup
	errCh := make(chan error, 10)

	modifications := []struct {
		name    string
		content string
	}{
		{"server.go", `package main

// Server handles HTTP requests
type Server struct { port int; host string }

// Start starts the server on a host
func (s *Server) Start() error { return nil }

// Restart restarts the server
func (s *Server) Restart() error { return nil }
`},
		{"handler.go", `package main

// Handler processes requests v2
type Handler struct{ timeout int }

// Handle handles a request with timeout
func (h *Handler) Handle() {}
`},
		{"repo.go", `package main

// Repository handles data access v2
type Repository struct{}

// Get retrieves a record
func (r *Repository) Get(id string) {}

// Put stores a record
func (r *Repository) Put(data interface{}) error { return nil }
`},
	}

	for _, mod := range modifications {
		mod := mod // capture
		wg.Add(1)
		go func() {
			defer wg.Done()
			writeGoFile(t, tp.repoRoot, mod.name, mod.content)
			// Sequential delete-then-index per file (simulates handleFileEvents)
			if err := tp.store.DeleteByFile(mod.name); err != nil {
				errCh <- fmt.Errorf("DeleteByFile(%s): %w", mod.name, err)
				return
			}

			absPath := filepath.Join(tp.repoRoot, mod.name)
			result, err := tp.parser.ParseFile(absPath, tp.repoRoot)
			if err != nil {
				errCh <- fmt.Errorf("ParseFile(%s): %w", mod.name, err)
				return
			}
			if len(result.Nodes) > 0 {
				if err := tp.store.UpsertNodes(result.Nodes); err != nil {
					errCh <- fmt.Errorf("UpsertNodes(%s): %w", mod.name, err)
					return
				}
			}
			// Skip edges that reference external nodes (foreign key)
			nodeIDs, err := tp.store.GetAllNodeIDs()
			if err != nil {
				errCh <- fmt.Errorf("GetAllNodeIDs: %w", err)
				return
			}
			known := make(map[string]bool, len(nodeIDs))
			for _, id := range nodeIDs {
				known[id] = true
			}
			var validEdges []types.ASTEdge
			for _, e := range result.Edges {
				if known[e.SourceID] && known[e.TargetID] {
					validEdges = append(validEdges, e)
				}
			}
			if len(validEdges) > 0 {
				if err := tp.store.UpsertEdges(validEdges); err != nil {
					errCh <- fmt.Errorf("UpsertEdges(%s): %w", mod.name, err)
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent modification error: %v", err)
	}

	// Rebuild graph after all concurrent modifications
	tp.rebuildGraph(t)

	// Verify final state is consistent
	nodeIDs, _ := tp.store.GetAllNodeIDs()
	edges, _ := tp.store.GetAllEdges()

	nodeSet := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		nodeSet[id] = true
	}
	for _, e := range edges {
		if !nodeSet[e.SourceID] {
			t.Errorf("orphan edge source: %s", e.SourceID[:16])
		}
		if !nodeSet[e.TargetID] {
			t.Errorf("orphan edge target: %s", e.TargetID[:16])
		}
	}

	t.Logf("After concurrent modifications: %d nodes, %d edges, graph: %d nodes %d edges",
		len(nodeIDs), len(edges), tp.graphEngine.NodeCount(), tp.graphEngine.EdgeCount())
}

func TestConcurrent_SearchConsistency(t *testing.T) {
	tp, _ := setupConcurrentEnv(t)

	const goroutines = 5
	const query = "Server Start"

	// Run the same search concurrently — all results should be identical
	type searchRun struct {
		results []types.SearchResult
		err     error
	}
	runs := make([]searchRun, goroutines)
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := tp.search.Search(query, 10, nil)
			runs[i] = searchRun{results: results, err: err}
		}()
	}
	wg.Wait()

	// Verify no errors
	for i, r := range runs {
		if r.err != nil {
			t.Fatalf("goroutine %d search error: %v", i, r.err)
		}
	}

	// All runs should return the same number of results and the same node IDs
	// (since the underlying data isn't changing)
	if len(runs) == 0 {
		t.Skip("no search runs")
	}
	referenceLen := len(runs[0].results)
	for i := 1; i < len(runs); i++ {
		if len(runs[i].results) != referenceLen {
			t.Errorf("goroutine %d returned %d results, expected %d", i, len(runs[i].results), referenceLen)
		}
	}

	// Compare node IDs (same order expected since scoring is deterministic)
	if referenceLen > 0 {
		for i := 1; i < len(runs); i++ {
			for j := 0; j < referenceLen && j < len(runs[i].results); j++ {
				if runs[i].results[j].Node.ID != runs[0].results[j].Node.ID {
					t.Errorf("goroutine %d result[%d] has different node ID: got %s, want %s",
						i, j, runs[i].results[j].Node.ID[:16], runs[0].results[j].Node.ID[:16])
				}
			}
		}
	}
}

func TestRace_GraphRebuildDuringSearch(t *testing.T) {
	// This test is most useful with -race flag: go test -tags fts5 -race ./...
	// All goroutines run bounded iterations to ensure deterministic completion.
	tp := newTestPipeline(t)

	// Create some initial data
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("mod%d.go", i)
		content := fmt.Sprintf(`package main

type Mod%d struct{}
func (m *Mod%d) Run%d() {}
func (m *Mod%d) Init%d() {}
`, i, i, i, i, i)
		writeGoFile(t, tp.repoRoot, name, content)
		tp.indexFile(t, name)
	}

	const iterations = 100
	var wg sync.WaitGroup

	// Goroutine 1: repeatedly rebuild graph from store edges
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			edges, err := tp.store.GetAllEdges()
			if err == nil {
				tp.graphEngine.BuildFromEdges(edges)
			}
		}
	}()

	// Goroutine 2: repeatedly run search queries
	wg.Add(1)
	go func() {
		defer wg.Done()
		queries := []string{"Mod", "Run", "Init", "struct"}
		for i := 0; i < iterations; i++ {
			tp.search.Search(queries[i%len(queries)], 5, nil)
		}
	}()

	// Goroutine 3: repeatedly read graph state (NodeCount, EdgeCount, BlastRadius)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			tp.graphEngine.NodeCount()
			tp.graphEngine.EdgeCount()
			tp.graphEngine.BlastRadius("nonexistent", 2)
		}
	}()

	// Goroutine 4: repeatedly read from store
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			tp.store.GetAllNodeIDs()
			tp.store.GetAllEdges()
		}
	}()

	wg.Wait()
	t.Log("Race stress test completed without panics or data races")
}

func TestConcurrent_IndexAndDeleteSimultaneous(t *testing.T) {
	tp := newTestPipeline(t)

	// Create initial files
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("file%d.go", i)
		content := fmt.Sprintf(`package main

func Func%d() {}
`, i)
		writeGoFile(t, tp.repoRoot, name, content)
		tp.indexFile(t, name)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 20)

	// Goroutine 1: delete some files
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 3; i++ {
			name := fmt.Sprintf("file%d.go", i)
			if err := tp.store.DeleteByFile(name); err != nil {
				errCh <- fmt.Errorf("delete %s: %w", name, err)
			}
		}
	}()

	// Goroutine 2: add new files
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 5; i < 8; i++ {
			name := fmt.Sprintf("file%d.go", i)
			content := fmt.Sprintf(`package main

func NewFunc%d() {}
`, i)
			path := filepath.Join(tp.repoRoot, name)
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				errCh <- fmt.Errorf("write %s: %w", name, err)
				continue
			}
			absPath := filepath.Join(tp.repoRoot, name)
			result, err := tp.parser.ParseFile(absPath, tp.repoRoot)
			if err != nil {
				errCh <- fmt.Errorf("parse %s: %w", name, err)
				continue
			}
			if len(result.Nodes) > 0 {
				if err := tp.store.UpsertNodes(result.Nodes); err != nil {
					errCh <- fmt.Errorf("upsert %s: %w", name, err)
				}
			}
		}
	}()

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent error: %v", err)
	}

	// Rebuild graph and verify consistency
	tp.rebuildGraph(t)

	nodeIDs, _ := tp.store.GetAllNodeIDs()
	edges, _ := tp.store.GetAllEdges()

	nodeSet := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		nodeSet[id] = true
	}
	for _, e := range edges {
		if !nodeSet[e.SourceID] {
			t.Errorf("orphan edge source: %s", e.SourceID[:16])
		}
		if !nodeSet[e.TargetID] {
			t.Errorf("orphan edge target: %s", e.TargetID[:16])
		}
	}

	t.Logf("After concurrent index+delete: %d nodes, %d edges", len(nodeIDs), len(edges))
}
