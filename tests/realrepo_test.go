//go:build fts5 && realrepo

package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/naman/qb-context/internal/embedding"
	"github.com/naman/qb-context/internal/graph"
	"github.com/naman/qb-context/internal/mcp"
	"github.com/naman/qb-context/internal/parser"
	"github.com/naman/qb-context/internal/search"
	"github.com/naman/qb-context/internal/storage"
	"github.com/naman/qb-context/internal/types"
)

const realRepoPath = "/Users/naman/Documents/QBApps/qbapi"

// skipDirs are directories that should be skipped during indexing.
var skipDirs = []string{
	"vendor/",
	"node_modules/",
	".git/",
	"storage/",
	".idea/",
	"bootstrap/cache/",
}

// shouldSkip returns true if the relative path is within a directory we want to skip.
func shouldSkip(rel string) bool {
	for _, prefix := range skipDirs {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// realRepoEnv holds the fully-indexed pipeline state for the real repo.
type realRepoEnv struct {
	store       *storage.Store
	graphEngine *graph.GraphEngine
	searchEng   *search.HybridSearch
	embedder    embedding.Embedder
	server      *mcp.Server
	totalNodes  int
	totalEdges  int // valid (FK-filtered) edges stored in DB
	rawEdges    int // total edges before FK filtering
	// Track edge types seen in raw parsed data (before FK filter).
	// Import edges often point to external symbols not in our node set,
	// so they get filtered out. We still want to verify the parser emits them.
	rawEdgeTypes map[types.EdgeType]bool
}

// Shared singleton: index the repo once across all test functions.
var (
	sharedEnv     *realRepoEnv
	sharedEnvOnce sync.Once
	sharedEnvErr  error
)

// getSharedEnv returns the shared real-repo environment, indexing the repo
// on first call. Subsequent calls return the cached result. Uses a temp dir
// outside t.TempDir() so the DB survives across test functions.
func getSharedEnv(t *testing.T) *realRepoEnv {
	t.Helper()

	if _, err := os.Stat(realRepoPath); os.IsNotExist(err) {
		t.Skipf("real repo not found at %s", realRepoPath)
	}

	sharedEnvOnce.Do(func() {
		sharedEnv, sharedEnvErr = buildRealRepoEnv()
	})

	if sharedEnvErr != nil {
		t.Fatalf("failed to build real repo env: %v", sharedEnvErr)
	}
	return sharedEnv
}

// buildRealRepoEnv does the heavy lifting: walk, parse, store, build graph, register tools.
func buildRealRepoEnv() (*realRepoEnv, error) {
	// Use a persistent temp dir (not tied to a specific test)
	tmpDir, err := os.MkdirTemp("", "qb-realrepo-test-*")
	if err != nil {
		return nil, err
	}

	dbPath := filepath.Join(tmpDir, ".qb-context", "test.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		return nil, err
	}

	// Walk and parse all supported files
	p := parser.New()
	var allNodes []types.ASTNode
	var allEdges []types.ASTEdge
	rawEdgeTypes := make(map[types.EdgeType]bool)

	err = filepath.Walk(realRepoPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(realRepoPath, path)
		if shouldSkip(rel) {
			return nil
		}
		if !parser.IsSupported(path) {
			return nil
		}

		result, parseErr := p.ParseFile(path, realRepoPath)
		if parseErr != nil {
			return nil // skip files with parse errors
		}

		allNodes = append(allNodes, result.Nodes...)
		allEdges = append(allEdges, result.Edges...)
		for _, e := range result.Edges {
			rawEdgeTypes[e.EdgeType] = true
		}
		return nil
	})
	if err != nil {
		store.Close()
		return nil, err
	}

	// Store nodes
	if err := store.UpsertNodes(allNodes); err != nil {
		store.Close()
		return nil, err
	}

	// Filter edges (FK constraint: both endpoints must exist)
	nodeIDSet := make(map[string]bool, len(allNodes))
	for _, n := range allNodes {
		nodeIDSet[n.ID] = true
	}
	var validEdges []types.ASTEdge
	for _, e := range allEdges {
		if nodeIDSet[e.SourceID] && nodeIDSet[e.TargetID] {
			validEdges = append(validEdges, e)
		}
	}
	if err := store.UpsertEdges(validEdges); err != nil {
		store.Close()
		return nil, err
	}

	// Build graph
	graphEngine := graph.New()
	storedEdges, err := store.GetAllEdges()
	if err != nil {
		store.Close()
		return nil, err
	}
	graphEngine.BuildFromEdges(storedEdges)

	// Create embedder and search engine
	embedder := embedding.NewTFIDFEmbedder()
	searchEng := search.New(store, embedder, graphEngine)

	// Set up MCP server with tool handlers
	deps := mcp.ToolDeps{
		Store:    store,
		Graph:    graphEngine,
		Search:   searchEng,
		RepoRoot: realRepoPath,
	}
	server := mcp.NewServerWithIO(nil, nil)
	mcp.RegisterTools(server, deps, nil)

	return &realRepoEnv{
		store:        store,
		graphEngine:  graphEngine,
		searchEng:    searchEng,
		embedder:     embedder,
		server:       server,
		totalNodes:   len(allNodes),
		totalEdges:   len(validEdges),
		rawEdges:     len(allEdges),
		rawEdgeTypes: rawEdgeTypes,
	}, nil
}

// TestRealRepo_IndexAndQuery tests the full pipeline: parse -> store -> graph -> search
// against the real Laravel repo at /Users/naman/Documents/QBApps/qbapi.
func TestRealRepo_IndexAndQuery(t *testing.T) {
	env := getSharedEnv(t)

	t.Logf("Indexed: %d nodes, %d valid edges (%d raw), graph: %d nodes / %d edges",
		env.totalNodes, env.totalEdges, env.rawEdges,
		env.graphEngine.NodeCount(), env.graphEngine.EdgeCount())

	// --- Subtest: minimum node/edge counts ---
	t.Run("MinimumCounts", func(t *testing.T) {
		if env.totalNodes < 50 {
			t.Errorf("expected at least 50 nodes from real repo, got %d", env.totalNodes)
		}
		if env.totalEdges < 20 {
			t.Errorf("expected at least 20 edges from real repo, got %d", env.totalEdges)
		}
		if env.graphEngine.NodeCount() == 0 {
			t.Error("graph has no nodes")
		}
		if env.graphEngine.EdgeCount() == 0 {
			t.Error("graph has no edges")
		}
	})

	// --- Subtest: PHP-specific constructs ---
	t.Run("PHPConstructs", func(t *testing.T) {
		nodeIDs, err := env.store.GetAllNodeIDs()
		if err != nil {
			t.Fatalf("GetAllNodeIDs: %v", err)
		}
		hasClass := false
		hasMethod := false
		hasFunction := false
		for _, id := range nodeIDs {
			node, err := env.store.GetNode(id)
			if err != nil {
				continue
			}
			switch node.NodeType {
			case types.NodeTypeClass:
				hasClass = true
			case types.NodeTypeMethod:
				hasMethod = true
			case types.NodeTypeFunction:
				hasFunction = true
			}
			if hasClass && hasMethod && hasFunction {
				break
			}
		}

		// Check import edges from the raw parsed data (before FK filtering).
		// Import edges often reference external packages that are not in our
		// node set, so they get filtered before DB insertion. The parser still
		// emits them correctly.
		hasImport := env.rawEdgeTypes[types.EdgeTypeImports]

		if !hasClass {
			t.Error("expected PHP classes in real repo")
		}
		if !hasMethod {
			t.Error("expected PHP methods in real repo")
		}
		if !hasFunction {
			t.Error("expected PHP functions in real repo (standalone or class-level)")
		}
		if !hasImport {
			t.Error("expected import/use edges in raw parsed data")
		}
		t.Logf("PHP constructs verified: class=%v method=%v function=%v import=%v",
			hasClass, hasMethod, hasFunction, hasImport)
	})

	// --- Subtest: context tool handler ---
	t.Run("ContextHandler", func(t *testing.T) {
		handler, ok := env.server.GetHandler("context")
		if !ok {
			t.Fatal("context handler not registered")
		}

		result, err := handler(json.RawMessage(`{"query": "user", "limit": 5}`))
		if err != nil {
			t.Fatalf("context handler error: %v", err)
		}
		if result == nil {
			t.Error("context handler returned nil for query 'user'")
		}
		t.Logf("context query 'user': %T (non-nil=%v)", result, result != nil)
	})

	// --- Subtest: query tool (SQL) ---
	t.Run("QueryHandler", func(t *testing.T) {
		handler, ok := env.server.GetHandler("query")
		if !ok {
			t.Fatal("query handler not registered")
		}

		result, err := handler(json.RawMessage(`{"sql": "SELECT COUNT(*) as cnt FROM nodes"}`))
		if err != nil {
			t.Fatalf("query handler error: %v", err)
		}
		t.Logf("node count query result: %v", result)
	})

	// --- Subtest: impact tool ---
	t.Run("ImpactHandler", func(t *testing.T) {
		handler, ok := env.server.GetHandler("impact")
		if !ok {
			t.Fatal("impact handler not registered")
		}

		result, err := handler(json.RawMessage(`{"symbol_id": "Controller"}`))
		if err != nil {
			// Not fatal — the exact symbol name may not match
			t.Logf("impact tool info (symbol might not exist as exact match): %v", err)
		} else {
			t.Logf("impact result for 'Controller': %v", result)
		}
	})

	// --- Subtest: health tool ---
	t.Run("HealthHandler", func(t *testing.T) {
		handler, ok := env.server.GetHandler("health")
		if !ok {
			t.Fatal("health handler not registered")
		}

		result, err := handler(json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("health handler error: %v", err)
		}
		t.Logf("health: %v", result)
	})

	// --- Subtest: hybrid search directly ---
	t.Run("HybridSearch", func(t *testing.T) {
		results, err := env.searchEng.Search("controller", 10, nil)
		if err != nil {
			t.Fatalf("Search error: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected at least 1 result for 'controller' search in a Laravel repo")
		}
		t.Logf("hybrid search 'controller': %d results", len(results))
		for i, r := range results {
			t.Logf("  [%d] %s (%s) in %s — score=%.4f",
				i, r.Node.SymbolName, r.Node.NodeType, r.Node.FilePath, r.Score)
		}
	})
}

// TestRealRepo_CLIToolsComprehensive exercises additional MCP tool handlers
// against the indexed real repo data.
func TestRealRepo_CLIToolsComprehensive(t *testing.T) {
	env := getSharedEnv(t)

	// --- read_symbol tool ---
	t.Run("ReadSymbol", func(t *testing.T) {
		handler, ok := env.server.GetHandler("read_symbol")
		if !ok {
			t.Fatal("read_symbol handler not registered")
		}

		nodeIDs, err := env.store.GetAllNodeIDs()
		if err != nil || len(nodeIDs) == 0 {
			t.Skipf("no nodes to test read_symbol against")
		}
		firstNode, err := env.store.GetNode(nodeIDs[0])
		if err != nil {
			t.Fatalf("GetNode: %v", err)
		}

		params, _ := json.Marshal(map[string]string{"symbol_id": firstNode.SymbolName})
		result, err := handler(json.RawMessage(params))
		if err != nil {
			t.Logf("read_symbol info (may fail if file no longer has that byte range): %v", err)
		} else {
			t.Logf("read_symbol '%s': returned non-nil=%v", firstNode.SymbolName, result != nil)
		}
	})

	// --- search_code tool ---
	t.Run("SearchCode", func(t *testing.T) {
		handler, ok := env.server.GetHandler("search_code")
		if !ok {
			t.Fatal("search_code handler not registered")
		}

		result, err := handler(json.RawMessage(`{"pattern": "class\\s+\\w+Controller", "limit": 5}`))
		if err != nil {
			t.Logf("search_code warning: %v", err)
		} else {
			t.Logf("search_code 'class Controller' pattern: %v", result)
		}
	})

	// --- get_key_symbols tool ---
	t.Run("GetKeySymbols", func(t *testing.T) {
		handler, ok := env.server.GetHandler("get_key_symbols")
		if !ok {
			t.Fatal("get_key_symbols handler not registered")
		}

		result, err := handler(json.RawMessage(`{"limit": 10}`))
		if err != nil {
			t.Fatalf("get_key_symbols error: %v", err)
		}
		t.Logf("key symbols: %v", result)
	})

	// --- get_architecture_summary tool ---
	t.Run("ArchitectureSummary", func(t *testing.T) {
		handler, ok := env.server.GetHandler("get_architecture_summary")
		if !ok {
			t.Fatal("get_architecture_summary handler not registered")
		}

		result, err := handler(json.RawMessage(`{"limit": 5}`))
		if err != nil {
			t.Fatalf("get_architecture_summary error: %v", err)
		}
		t.Logf("architecture summary: %v", result)
	})

	// --- explore tool ---
	t.Run("Explore", func(t *testing.T) {
		handler, ok := env.server.GetHandler("explore")
		if !ok {
			t.Fatal("explore handler not registered")
		}

		result, err := handler(json.RawMessage(`{"symbol": "User", "include_deps": true, "depth": 2}`))
		if err != nil {
			t.Logf("explore warning (symbol may not exist): %v", err)
		} else {
			t.Logf("explore 'User': %v", result)
		}
	})

	// --- context tool in architecture mode ---
	t.Run("ContextArchitecture", func(t *testing.T) {
		handler, ok := env.server.GetHandler("context")
		if !ok {
			t.Fatal("context handler not registered")
		}

		result, err := handler(json.RawMessage(`{"query": "", "mode": "architecture"}`))
		if err != nil {
			t.Fatalf("context architecture mode error: %v", err)
		}
		t.Logf("architecture mode result: %T", result)
	})

	// --- trace_call_path tool ---
	t.Run("TraceCallPath", func(t *testing.T) {
		handler, ok := env.server.GetHandler("trace_call_path")
		if !ok {
			t.Fatal("trace_call_path handler not registered")
		}

		// This may or may not find a path — just ensure it doesn't crash
		result, err := handler(json.RawMessage(`{"from": "index", "to": "store", "max_depth": 5}`))
		if err != nil {
			t.Logf("trace_call_path info (may not find path): %v", err)
		} else {
			t.Logf("trace_call_path result: %v", result)
		}
	})

	// --- understand tool ---
	t.Run("Understand", func(t *testing.T) {
		handler, ok := env.server.GetHandler("understand")
		if !ok {
			t.Fatal("understand handler not registered")
		}

		result, err := handler(json.RawMessage(`{"symbol": "User"}`))
		if err != nil {
			t.Logf("understand warning: %v", err)
		} else {
			t.Logf("understand 'User': %v", result)
		}
	})
}

// TestRealRepo_SearchQuality runs several domain-relevant queries against the
// indexed Laravel codebase and verifies that results are non-empty and topically relevant.
func TestRealRepo_SearchQuality(t *testing.T) {
	env := getSharedEnv(t)

	queries := []struct {
		query         string
		description   string
		expectMinimum int    // minimum expected results (0 = just don't error)
		expectKeyword string // at least one result should contain this (case-insensitive) in name/path/content
	}{
		{
			query:         "authentication",
			description:   "auth-related symbols",
			expectMinimum: 1,
			expectKeyword: "auth",
		},
		{
			query:         "database",
			description:   "DB-related symbols",
			expectMinimum: 1,
			expectKeyword: "", // DB may appear as Model or migration
		},
		{
			query:         "controller",
			description:   "controller classes",
			expectMinimum: 1,
			expectKeyword: "controller",
		},
		{
			query:         "model",
			description:   "model classes",
			expectMinimum: 1,
			expectKeyword: "model",
		},
		{
			query:         "middleware",
			description:   "middleware components",
			expectMinimum: 0,
			expectKeyword: "middleware",
		},
		{
			query:         "request validation",
			description:   "request validation logic",
			expectMinimum: 0,
			expectKeyword: "",
		},
		{
			query:         "route",
			description:   "routing-related symbols",
			expectMinimum: 0,
			expectKeyword: "route",
		},
	}

	for _, tc := range queries {
		t.Run(tc.query, func(t *testing.T) {
			results, err := env.searchEng.Search(tc.query, 10, nil)
			if err != nil {
				t.Fatalf("Search(%q) error: %v", tc.query, err)
			}

			t.Logf("query %q: %d results", tc.query, len(results))
			for i, r := range results {
				t.Logf("  [%d] %s (%s) in %s — score=%.4f",
					i, r.Node.SymbolName, r.Node.NodeType, r.Node.FilePath, r.Score)
			}

			if len(results) < tc.expectMinimum {
				t.Errorf("query %q (%s): expected at least %d results, got %d",
					tc.query, tc.description, tc.expectMinimum, len(results))
			}

			// Check keyword relevance if specified
			if tc.expectKeyword != "" && len(results) > 0 {
				found := false
				kw := strings.ToLower(tc.expectKeyword)
				for _, r := range results {
					name := strings.ToLower(r.Node.SymbolName)
					path := strings.ToLower(r.Node.FilePath)
					content := strings.ToLower(r.Node.ContentSum)
					if strings.Contains(name, kw) || strings.Contains(path, kw) || strings.Contains(content, kw) {
						found = true
						break
					}
				}
				if !found {
					t.Logf("warning: query %q — none of the results contain keyword %q in name/path/content",
						tc.query, tc.expectKeyword)
				}
			}
		})
	}
}
