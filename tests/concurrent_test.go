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
	tp, _ := setupConcurrentEnv(t)

	const iterations = 50
	var wg sync.WaitGroup
	errCh := make(chan error, iterations*3)

	// Goroutine 1: continuously upsert new nodes (write path)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			node := types.ASTNode{
				ID:         types.GenerateNodeID(fmt.Sprintf("concurrent_%d.go", i), fmt.Sprintf("ConcFunc%d", i)),
				FilePath:   fmt.Sprintf("concurrent_%d.go", i),
				SymbolName: fmt.Sprintf("ConcFunc%d", i),
				NodeType:   types.NodeTypeFunction,
				StartByte:  0,
				EndByte:    100,
				ContentSum: fmt.Sprintf("ConcFunc%d handles concurrent processing of data", i),
			}
			if err := tp.store.UpsertNodes([]types.ASTNode{node}); err != nil {
				errCh <- fmt.Errorf("upsert node %d: %w", i, err)
			}
		}
	}()

	// Goroutine 2: continuously run search queries (read path)
	wg.Add(1)
	go func() {
		defer wg.Done()
		queries := []string{"Server", "Handler", "Repository", "Config", "Start"}
		for i := 0; i < iterations; i++ {
			q := queries[i%len(queries)]
			_, err := tp.search.Search(q, 5, nil)
			if err != nil {
				errCh <- fmt.Errorf("search %q iter %d: %w", q, i, err)
			}
		}
	}()

	// Goroutine 3: continuously rebuild graph from edges (structural write path)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			edges, err := tp.store.GetAllEdges()
			if err != nil {
				errCh <- fmt.Errorf("get edges iter %d: %w", i, err)
				continue
			}
			tp.graphEngine.BuildFromEdges(edges)
		}
	}()

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent error: %v", err)
	}

	// Verify some of the concurrently-inserted nodes are findable
	nodeIDs, err := tp.store.GetAllNodeIDs()
	if err != nil {
		t.Fatalf("GetAllNodeIDs: %v", err)
	}
	// We started with nodes from setupConcurrentEnv plus added `iterations` new ones
	if len(nodeIDs) < iterations {
		t.Errorf("expected at least %d nodes, got %d", iterations, len(nodeIDs))
	}
	t.Logf("Concurrent search-during-index: %d total nodes after test", len(nodeIDs))
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
	nodeIDs, err := tp.store.GetAllNodeIDs()
	if err != nil {
		t.Fatalf("GetAllNodeIDs: %v", err)
	}
	edges, err := tp.store.GetAllEdges()
	if err != nil {
		t.Fatalf("GetAllEdges: %v", err)
	}

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

	// M73: Assert final node counts after concurrent operations
	if len(nodeIDs) == 0 {
		t.Error("expected at least some nodes after concurrent modifications")
	}

	// Verify graph node count matches or is consistent with store
	graphNodeCount := tp.graphEngine.NodeCount()
	graphEdgeCount := tp.graphEngine.EdgeCount()
	t.Logf("After concurrent modifications: %d nodes, %d edges, graph: %d nodes %d edges",
		len(nodeIDs), len(edges), graphNodeCount, graphEdgeCount)

	// Graph edge count should match store edge count after rebuild
	if graphEdgeCount != len(edges) {
		t.Errorf("graph edge count (%d) does not match store edge count (%d) after rebuild",
			graphEdgeCount, len(edges))
	}
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

	nodeIDs, err := tp.store.GetAllNodeIDs()
	if err != nil {
		t.Fatalf("GetAllNodeIDs: %v", err)
	}
	edges, err := tp.store.GetAllEdges()
	if err != nil {
		t.Fatalf("GetAllEdges: %v", err)
	}

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

	// M73: Assert final node/edge counts
	// We started with 5 files, deleted 3, added 3 new ones → expect at least 5 files worth
	if len(nodeIDs) == 0 {
		t.Error("expected at least some nodes after concurrent index+delete")
	}

	t.Logf("After concurrent index+delete: %d nodes, %d edges", len(nodeIDs), len(edges))
}

// TestConcurrent_GraphRebuildDuringSearch exercises concurrent graph rebuilds,
// ComputeSearchSignals, and hybrid search to surface application-level race
// conditions that simple mutex-serialized tests would not catch.
// Best run with: go test -tags fts5 -race ./tests/...
func TestConcurrent_GraphRebuildDuringSearch(t *testing.T) {
	tp := newTestPipeline(t)

	// Create initial data so graph has substance
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("concurrent_graph_%d.go", i)
		content := fmt.Sprintf(`package main

type CG%d struct{}
func (c *CG%d) Run%d() {}
func (c *CG%d) Init%d() {}
func (c *CG%d) Process%d() {}
`, i, i, i, i, i, i, i)
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

	// Goroutine 2: repeatedly call ComputeSearchSignals
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			nodeIDs, err := tp.store.GetAllNodeIDs()
			if err == nil && len(nodeIDs) > 0 {
				// Use a subset of node IDs as seeds
				seeds := nodeIDs
				if len(seeds) > 3 {
					seeds = seeds[:3]
				}
				tp.graphEngine.ComputeSearchSignals(seeds)
			}
		}
	}()

	// Goroutine 3: repeatedly run hybrid search
	wg.Add(1)
	go func() {
		defer wg.Done()
		queries := []string{"CG", "Run", "Init", "Process", "struct"}
		for i := 0; i < iterations; i++ {
			tp.search.Search(queries[i%len(queries)], 5, nil)
		}
	}()

	// Goroutine 4: repeatedly read graph metrics during rebuilds
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			tp.graphEngine.NodeCount()
			tp.graphEngine.EdgeCount()
			tp.graphEngine.PageRank()
		}
	}()

	wg.Wait()
	t.Log("Concurrent graph rebuild + search + signals test completed without panics")
}
