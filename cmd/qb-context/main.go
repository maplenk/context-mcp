package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/naman/qb-context/internal/adr"
	"github.com/naman/qb-context/internal/config"
	"github.com/naman/qb-context/internal/embedding"
	"github.com/naman/qb-context/internal/graph"
	"github.com/naman/qb-context/internal/mcp"
	"github.com/naman/qb-context/internal/parser"
	"github.com/naman/qb-context/internal/search"
	"github.com/naman/qb-context/internal/storage"
	"github.com/naman/qb-context/internal/types"
	"github.com/naman/qb-context/internal/watcher"
)

// indexMu prevents concurrent index operations (M8)
var indexMu sync.Mutex

func main() {
	// Route all logging to stderr to avoid corrupting MCP JSON-RPC on stdout
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// L3: Check for CLI subcommand BEFORE flag.Parse() — avoids fragile
	// reliance on flag stopping at non-flag arguments.
	for i, arg := range os.Args[1:] {
		if arg == "cli" {
			cfg := config.ParseFlags()
			runCLI(cfg, os.Args[i+2:]) // pass everything after "cli"
			return
		}
	}

	cfg := config.ParseFlags()

	log.Printf("qb-context daemon starting — repo: %s", cfg.RepoRoot)

	// 1. Initialize embedding engine — ONNX if configured, TFIDF fallback
	var embedder embedding.Embedder
	if cfg.ONNXModelDir != "" {
		dim := cfg.EmbeddingDim
		if dim == storage.DefaultEmbeddingDim {
			dim = 256 // default Matryoshka dim for ONNX
		}
		onnxEmb, err := embedding.NewONNXEmbedder(cfg.ONNXModelDir, dim, cfg.ONNXLibPath)
		if err != nil {
			log.Printf("ONNX embedder failed, falling back to TFIDF: %v", err)
			embedder = embedding.NewEmbedder()
		} else {
			embedder = onnxEmb
			embedding.SetEmbeddingDim(dim)
			cfg.EmbeddingDim = dim
			log.Printf("ONNX embedder initialized (dim=%d, model=%s)", dim, cfg.ONNXModelDir)
		}
	} else {
		embedder = embedding.NewEmbedder()
		log.Printf("Embedding engine initialized (TF-IDF, dim=%d)", embedding.GetEmbeddingDim())
	}

	// 2. Initialize SQLite storage
	store, err := storage.NewStore(cfg.DBPath, cfg.EmbeddingDim)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	log.Printf("Storage initialized at %s", cfg.DBPath)

	// C14: Channel to terminate the memory monitoring goroutine
	memDone := make(chan struct{})

	// Unified cleanup, safe to call multiple times (signal handler + deferred path).
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			close(memDone) // C14: stop memory monitor goroutine
			store.Close()
			embedder.Close()
		})
	}
	defer cleanup()

	// 3. Initialize parser
	p := parser.New()

	// 4. Initialize graph engine
	graphEngine := graph.New()

	// 5. Initialize hybrid search
	hybridSearch := search.New(store, embedder, graphEngine)

	// 6. Run initial indexing
	log.Printf("Starting initial index...")
	indexRepo(cfg, store, p, embedder, graphEngine)
	log.Printf("Initial index complete")

	// 7. Initialize filesystem watcher
	w, err := watcher.New(cfg.RepoRoot, cfg.DebounceInterval, cfg.ExcludedDirs)
	if err != nil {
		log.Fatalf("Failed to create watcher: %v", err)
	}
	if err := w.Start(); err != nil {
		log.Fatalf("Failed to start watcher: %v", err)
	}
	defer w.Stop() // safe: cleanup() above does not touch the watcher
	log.Printf("Filesystem watcher started")

	// H6: Periodic memory monitoring (C14: with cancellation via memDone)
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				log.Printf("Memory: Alloc=%dMB Sys=%dMB NumGC=%d",
					m.Alloc/1024/1024, m.Sys/1024/1024, m.NumGC)
				if m.Alloc > 2*1024*1024*1024 {
					log.Printf("WARNING: memory exceeds 2GB limit")
				}
			case <-memDone:
				return
			}
		}
	}()

	// 8. Start incremental update goroutine
	go handleFileEvents(w, cfg, store, p, embedder, graphEngine)

	// 9. Set up MCP server
	server := mcp.NewServer()
	indexFn := func(path string) error {
		indexMu.Lock()
		defer indexMu.Unlock()
		if path == "" {
			log.Printf("Full re-index triggered")
			indexRepo(cfg, store, p, embedder, graphEngine)
		} else {
			// C9: Validate path is within repo root (prevent path traversal)
			absPath := path
			if !filepath.IsAbs(absPath) {
				absPath = filepath.Join(cfg.RepoRoot, absPath)
			}
			absPath, err := filepath.Abs(absPath)
			if err != nil {
				return fmt.Errorf("invalid path: %w", err)
			}
			absRoot, _ := filepath.Abs(cfg.RepoRoot)
			if !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) && absPath != absRoot {
				return fmt.Errorf("path traversal detected: %s is outside repo root", path)
			}
			log.Printf("Targeted re-index triggered for: %s", path)
			indexPath(cfg, store, p, embedder, graphEngine, path)
		}
		return nil
	}
	mcp.RegisterTools(server, mcp.ToolDeps{
		Store:    store,
		Graph:    graphEngine,
		Search:   hybridSearch,
		RepoRoot: cfg.RepoRoot,
	}, indexFn)
	log.Printf("MCP server ready on stdio")

	// 10. Handle graceful shutdown
	// C7: Use a done channel instead of os.Exit(0) so deferred cleanup runs naturally.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		w.Stop()
		close(done)
	}()

	// 11. Start serving MCP requests (blocks until stdin closes or signal received)
	serveCh := make(chan error, 1)
	go func() {
		serveCh <- server.Serve()
	}()

	select {
	case err := <-serveCh:
		if err != nil {
			log.Printf("MCP server error: %v", err)
		}
	case <-done:
		log.Printf("Shutdown complete")
	}
}

// runCLI handles the "cli" subcommand for direct tool invocation
func runCLI(cfg *config.Config, args []string) {
	log.SetOutput(os.Stderr)

	// Handle --list flag
	if len(args) > 0 && args[0] == "--list" {
		store, err := storage.NewStore(cfg.DBPath, cfg.EmbeddingDim)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to init storage: %v\n", err)
			os.Exit(1)
		}
		defer store.Close()

		embedder := embedding.NewEmbedder()
		defer embedder.Close()
		graphEngine := graph.New()
		hybridSearch := search.New(store, embedder, graphEngine)

		server := mcp.NewServer()
		mcp.RegisterTools(server, mcp.ToolDeps{
			Store: store, Graph: graphEngine, Search: hybridSearch, RepoRoot: cfg.RepoRoot,
		}, nil)

		fmt.Printf("%-15s %s\n", "TOOL", "DESCRIPTION")
		fmt.Printf("%-15s %s\n", "----", "-----------")
		for _, t := range server.GetTools() {
			fmt.Printf("%-15s %s\n", t.Name, t.Description)
		}
		return
	}

	// Check for --reindex flag before arg count validation
	forceReindex := false
	var filteredArgs []string
	for _, a := range args {
		if a == "--reindex" {
			forceReindex = true
		} else {
			filteredArgs = append(filteredArgs, a)
		}
	}
	args = filteredArgs

	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: qb-context cli [--reindex] <tool_name> [json_args]\n")
		fmt.Fprintf(os.Stderr, "       qb-context cli --list\n")
		fmt.Fprintf(os.Stderr, "\nFlags:\n")
		fmt.Fprintf(os.Stderr, "  --reindex   Force full re-parse and re-index of the repository\n")
		os.Exit(1)
	}

	toolName := args[0]
	argsJSON := json.RawMessage("{}")
	if len(args) >= 2 {
		if !json.Valid([]byte(args[1])) {
			fmt.Fprintf(os.Stderr, "Invalid JSON args: %s\n", args[1])
			os.Exit(1)
		}
		argsJSON = json.RawMessage(args[1])
	}

	// Boot pipeline
	store, err := storage.NewStore(cfg.DBPath, cfg.EmbeddingDim)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init storage: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	embedder := embedding.NewEmbedder()
	defer embedder.Close()
	p := parser.New()
	graphEngine := graph.New()
	hybridSearch := search.New(store, embedder, graphEngine)

	// Skip full indexing if DB already has data (use --reindex to force)
	if !forceReindex {
		nodeIDs, _ := store.GetAllNodeIDs()
		if len(nodeIDs) > 0 {
			// Rebuild in-memory graph from stored edges
			edges, edgeErr := store.GetAllEdges()
			if edgeErr != nil {
				log.Printf("Failed to load edges from DB: %v", edgeErr)
			} else {
				graphEngine.BuildFromEdges(edges)
			}
			log.Printf("Loaded %d nodes from existing index (use --reindex to force)", len(nodeIDs))
		} else {
			// Empty DB — must index
			indexRepo(cfg, store, p, embedder, graphEngine)
		}
	} else {
		log.Printf("--reindex flag set, forcing full re-index...")
		indexRepo(cfg, store, p, embedder, graphEngine)
	}

	// Register tools and look up handler
	server := mcp.NewServer()
	mcp.RegisterTools(server, mcp.ToolDeps{
		Store: store, Graph: graphEngine, Search: hybridSearch, RepoRoot: cfg.RepoRoot,
	}, func(path string) error {
		indexRepo(cfg, store, p, embedder, graphEngine)
		return nil
	})

	handler, ok := server.GetHandler(toolName)
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown tool: %s\nUse --list to see available tools.\n", toolName)
		os.Exit(1)
	}

	result, err := handler(argsJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Tool error: %v\n", err)
		os.Exit(1)
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal result: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}

// indexRepo performs a full index of the repository
func indexRepo(cfg *config.Config, store *storage.Store, p *parser.Parser, embedder embedding.Embedder, graphEngine *graph.GraphEngine) {
	// L4: Use standalone walk function — no fsnotify watcher allocation needed
	files, err := watcher.WalkSourceFiles(cfg.RepoRoot, cfg.ExcludedDirs)
	if err != nil {
		log.Printf("Failed to walk repository: %v", err)
		return
	}
	log.Printf("Found %d source files to index", len(files))

	// Parse files using a worker pool
	type parseJob struct {
		relPath string
	}
	type parseResult struct {
		nodes []types.ASTNode
		edges []types.ASTEdge
	}

	jobs := make(chan parseJob, len(files))
	results := make(chan parseResult, len(files))
	var wg sync.WaitGroup

	// Launch workers
	workerCount := cfg.WorkerCount
	if workerCount > len(files) {
		workerCount = len(files)
	}
	if workerCount < 1 {
		workerCount = 1
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				absPath := filepath.Join(cfg.RepoRoot, job.relPath)
				result, err := p.ParseFile(absPath, cfg.RepoRoot)
				if err != nil {
					log.Printf("Parse error %s: %v", job.relPath, err)
					continue
				}
				results <- parseResult{nodes: result.Nodes, edges: result.Edges}
			}
		}()
	}

	// Send jobs
	go func() {
		for _, f := range files {
			jobs <- parseJob{relPath: f}
		}
		close(jobs)
	}()

	// Wait for workers and close results
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect and store results
	var allNodes []types.ASTNode
	var allEdges []types.ASTEdge
	for r := range results {
		allNodes = append(allNodes, r.nodes...)
		allEdges = append(allEdges, r.edges...)
	}

	// --- Cross-file edge resolution ---
	// Build symbol → nodeID index for class/struct/interface symbols
	symbolIndex := make(map[string]string)
	for _, n := range allNodes {
		if n.NodeType == types.NodeTypeClass || n.NodeType == types.NodeTypeStruct ||
			n.NodeType == types.NodeTypeInterface {
			if _, exists := symbolIndex[n.SymbolName]; !exists {
				symbolIndex[n.SymbolName] = n.ID
			}
		}
	}

	// Build nodeID set for resolution check
	nodeIDSet := make(map[string]bool, len(allNodes))
	for _, n := range allNodes {
		nodeIDSet[n.ID] = true
	}

	// Resolve cross-file edges using TargetSymbol
	for i, e := range allEdges {
		if !nodeIDSet[e.TargetID] && e.TargetSymbol != "" {
			if resolved, ok := symbolIndex[e.TargetSymbol]; ok {
				allEdges[i].TargetID = resolved
			}
		}
	}

	// Store nodes
	if len(allNodes) > 0 {
		if err := store.UpsertNodes(allNodes); err != nil {
			log.Printf("Failed to store nodes: %v", err)
		} else {
			log.Printf("Stored %d nodes", len(allNodes))
		}
	}

	// Store edges
	if len(allEdges) > 0 {
		if err := store.UpsertEdges(allEdges); err != nil {
			log.Printf("Failed to store edges: %v", err)
		} else {
			log.Printf("Stored %d edges", len(allEdges))
		}
	}

	// Generate embeddings in batches
	if len(allNodes) > 0 {
		batchSize := cfg.EmbeddingBatchSize
		for i := 0; i < len(allNodes); i += batchSize {
			end := i + batchSize
			if end > len(allNodes) {
				end = len(allNodes)
			}
			batch := allNodes[i:end]

			texts := make([]string, len(batch))
			for j, node := range batch {
				texts[j] = node.ContentSum
			}

			vectors, err := embedder.EmbedBatch(texts)
			if err != nil {
				// Batch failed — fall back to one-at-a-time embedding for this batch
				log.Printf("Embedding batch error (falling back to individual): %v", err)
				for j, text := range texts {
					vec, embedErr := embedder.Embed(text)
					if embedErr != nil {
						log.Printf("Individual embedding error for %s: %v", batch[j].SymbolName, embedErr)
						continue // skip this node but continue with rest
					}
					if storeErr := store.UpsertEmbedding(batch[j].ID, vec); storeErr != nil {
						log.Printf("Failed to store embedding for %s: %v", batch[j].SymbolName, storeErr)
					}
				}
				continue
			}

			for j, vec := range vectors {
				if err := store.UpsertEmbedding(batch[j].ID, vec); err != nil {
					log.Printf("Failed to store embedding for %s: %v", batch[j].SymbolName, err)
				}
			}
		}
		log.Printf("Generated embeddings for %d nodes", len(allNodes))
	}

	// Build graph from all edges in the database
	edges, err := store.GetAllEdges()
	if err != nil {
		log.Printf("Failed to load edges for graph: %v", err)
	} else {
		graphEngine.BuildFromEdges(edges)
		log.Printf("Graph built with %d nodes, %d edges", graphEngine.NodeCount(), graphEngine.EdgeCount())
	}

	// Compute and store betweenness centrality and PageRank (M7)
	betweenness := graphEngine.ComputeBetweenness()
	pageranks := graphEngine.PageRank()

	if len(betweenness) > 0 || len(pageranks) > 0 {
		nodeIDs := make(map[string]bool)
		for id := range betweenness {
			nodeIDs[id] = true
		}
		for id := range pageranks {
			nodeIDs[id] = true
		}

		var scores []types.NodeScore
		for nodeID := range nodeIDs {
			scores = append(scores, types.NodeScore{
				NodeID:      nodeID,
				Betweenness: betweenness[nodeID],
				PageRank:    pageranks[nodeID],
			})
		}
		if err := store.UpsertNodeScores(scores); err != nil {
			log.Printf("Failed to store node scores: %v", err)
		} else {
			log.Printf("Stored scores for %d nodes", len(scores))
		}
	}

	// Discover and store architecture documents
	adrDiscoverer := adr.NewDiscoverer(cfg.RepoRoot)
	docs, adrErr := adrDiscoverer.Discover()
	if adrErr != nil {
		log.Printf("ADR discovery error: %v", adrErr)
	} else if len(docs) > 0 {
		for _, doc := range docs {
			summary := types.ProjectSummary{
				Project:    doc.Path,
				Summary:    doc.Content,
				SourceHash: doc.SourceHash,
			}
			if err := store.UpsertProjectSummary(summary); err != nil {
				log.Printf("Failed to store ADR %s: %v", doc.Path, err)
			}
		}
		log.Printf("Discovered %d architecture documents", len(docs))
	}
}

// indexPath performs a targeted re-index of files under the given path
func indexPath(cfg *config.Config, store *storage.Store, p *parser.Parser, embedder embedding.Embedder, graphEngine *graph.GraphEngine, targetPath string) {
	absTarget := targetPath
	if !filepath.IsAbs(absTarget) {
		absTarget = filepath.Join(cfg.RepoRoot, absTarget)
	}

	// Determine if path is a file or directory
	info, err := os.Stat(absTarget)
	if err != nil {
		log.Printf("Cannot stat path %s: %v", targetPath, err)
		return
	}

	var files []string
	if info.IsDir() {
		// H20: Walk the directory respecting excluded dirs and gitignore
		filepath.Walk(absTarget, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if fi.IsDir() {
				base := filepath.Base(path)
				for _, excl := range cfg.ExcludedDirs {
					if base == excl {
						return filepath.SkipDir
					}
				}
				return nil
			}
			// Only index files with supported extensions
			if !parser.IsSupported(path) {
				return nil
			}
			relPath, err := filepath.Rel(cfg.RepoRoot, path)
			if err != nil {
				return nil
			}
			files = append(files, relPath)
			return nil
		})
	} else {
		relPath, err := filepath.Rel(cfg.RepoRoot, absTarget)
		if err != nil {
			log.Printf("Cannot compute relative path for %s: %v", absTarget, err)
			return
		}
		files = []string{relPath}
	}

	log.Printf("Targeted re-index: %d files under %s", len(files), targetPath)

	for _, relPath := range files {
		absPath := filepath.Join(cfg.RepoRoot, relPath)

		// Delete old data for this file
		if err := store.DeleteByFile(relPath); err != nil {
			log.Printf("Failed to delete stale data for %s: %v", relPath, err)
		}

		// Re-parse the file
		result, err := p.ParseFile(absPath, cfg.RepoRoot)
		if err != nil {
			log.Printf("Parse error %s: %v", relPath, err)
			continue
		}

		// Store nodes and edges
		if len(result.Nodes) > 0 {
			if err := store.UpsertNodes(result.Nodes); err != nil {
				log.Printf("Failed to store nodes for %s: %v", relPath, err)
			}
		}
		if len(result.Edges) > 0 {
			if err := store.UpsertEdges(result.Edges); err != nil {
				log.Printf("Failed to store edges for %s: %v", relPath, err)
			}
		}

		// Generate embeddings
		for _, node := range result.Nodes {
			vec, err := embedder.Embed(node.ContentSum)
			if err != nil {
				log.Printf("Embedding error for %s: %v", node.SymbolName, err)
				continue
			}
			store.UpsertEmbedding(node.ID, vec)
		}
	}

	// Rebuild graph after targeted re-index
	edges, err := store.GetAllEdges()
	if err == nil {
		graphEngine.BuildFromEdges(edges)
		log.Printf("Graph rebuilt with %d nodes, %d edges", graphEngine.NodeCount(), graphEngine.EdgeCount())

		// M17: Recompute and store betweenness/PageRank after targeted re-index
		betweenness := graphEngine.ComputeBetweenness()
		pageranks := graphEngine.PageRank()
		if len(betweenness) > 0 || len(pageranks) > 0 {
			nodeIDs := make(map[string]bool)
			for id := range betweenness {
				nodeIDs[id] = true
			}
			for id := range pageranks {
				nodeIDs[id] = true
			}
			var scores []types.NodeScore
			for nodeID := range nodeIDs {
				scores = append(scores, types.NodeScore{
					NodeID:      nodeID,
					Betweenness: betweenness[nodeID],
					PageRank:    pageranks[nodeID],
				})
			}
			if err := store.UpsertNodeScores(scores); err != nil {
				log.Printf("Failed to store node scores after targeted re-index: %v", err)
			} else {
				log.Printf("Stored scores for %d nodes after targeted re-index", len(scores))
			}
		}
	}
}

// betweennessRefreshThreshold is the number of incremental graph changes that
// trigger an async betweenness centrality recomputation.
const betweennessRefreshThreshold = 20

// handleFileEvents processes incremental file change events from the watcher.
// Uses incremental graph updates (H3) instead of full rebuilds, and triggers
// async betweenness recomputation after a threshold of changes (H4).
// C16: Acquires indexMu per event to prevent races with the MCP index tool.
func handleFileEvents(w *watcher.Watcher, cfg *config.Config, store *storage.Store, p *parser.Parser, embedder embedding.Embedder, graphEngine *graph.GraphEngine) {
	for event := range w.Events() {
		indexMu.Lock()
		processFileEvent(event, cfg, store, p, embedder, graphEngine)

		// C6: Check threshold while still holding indexMu to prevent lost-update race.
		// Two rapid events could both see threshold exceeded, both reset, and both spawn
		// redundant betweenness goroutines if this check were outside the lock.
		shouldRefresh := graphEngine.ChangeCount() >= betweennessRefreshThreshold
		if shouldRefresh {
			graphEngine.ResetChangeCount()
		}
		indexMu.Unlock()

		// H4: Trigger async betweenness + PageRank recomputation after threshold changes (M7)
		// H12: The async goroutine also acquires indexMu to prevent races with the event loop
		if shouldRefresh {
			go func() {
				indexMu.Lock()
				defer indexMu.Unlock()

				betweenness := graphEngine.ComputeBetweenness()
				pageranks := graphEngine.PageRank()

				if len(betweenness) > 0 || len(pageranks) > 0 {
					nodeIDs := make(map[string]bool)
					for id := range betweenness {
						nodeIDs[id] = true
					}
					for id := range pageranks {
						nodeIDs[id] = true
					}

					var scores []types.NodeScore
					for nodeID := range nodeIDs {
						scores = append(scores, types.NodeScore{
							NodeID:      nodeID,
							Betweenness: betweenness[nodeID],
							PageRank:    pageranks[nodeID],
						})
					}
					if err := store.UpsertNodeScores(scores); err != nil {
						log.Printf("Failed to update node scores: %v", err)
					} else {
						log.Printf("Async score recomputation complete (%d nodes)", len(scores))
					}
				}
			}()
		}
	}
}

// processFileEvent handles a single file event (extracted from handleFileEvents for C16).
// Caller must hold indexMu.
func processFileEvent(event types.FileEvent, cfg *config.Config, store *storage.Store, p *parser.Parser, embedder embedding.Embedder, graphEngine *graph.GraphEngine) {
	absPath := filepath.Join(cfg.RepoRoot, event.Path)

	switch event.Action {
	case types.FileEventDeleted:
		log.Printf("File deleted: %s", event.Path)

		// Get node IDs for this file BEFORE deleting from storage
		oldNodeIDs, err := store.GetNodeIDsByFile(event.Path)
		if err != nil {
			log.Printf("Failed to get node IDs for %s: %v", event.Path, err)
		}

		if err := store.DeleteByFile(event.Path); err != nil {
			log.Printf("Failed to delete file data: %v", err)
		}

		// Incremental graph update: remove old nodes instead of full rebuild
		for _, nodeID := range oldNodeIDs {
			graphEngine.RemoveNode(nodeID)
		}

	case types.FileEventCreated, types.FileEventModified:
		log.Printf("File changed: %s", event.Path)

		// Get old node IDs for this file BEFORE deleting from storage
		oldNodeIDs, err := store.GetNodeIDsByFile(event.Path)
		if err != nil {
			log.Printf("Failed to get old node IDs for %s: %v", event.Path, err)
		}

		// Delete old data for this file
		if err := store.DeleteByFile(event.Path); err != nil {
			log.Printf("Failed to delete stale data for %s: %v", event.Path, err)
		}

		// Remove old nodes from graph incrementally
		for _, nodeID := range oldNodeIDs {
			graphEngine.RemoveNode(nodeID)
		}

		// Re-parse the file
		result, err := p.ParseFile(absPath, cfg.RepoRoot)
		if err != nil {
			log.Printf("Parse error %s: %v", event.Path, err)
			return
		}

		// Store new nodes and edges
		if len(result.Nodes) > 0 {
			if err := store.UpsertNodes(result.Nodes); err != nil {
				log.Printf("Failed to store nodes: %v", err)
			}
		}
		if len(result.Edges) > 0 {
			if err := store.UpsertEdges(result.Edges); err != nil {
				log.Printf("Failed to store edges: %v", err)
			}
		}

		// Generate embeddings for new/updated nodes
		for _, node := range result.Nodes {
			vec, err := embedder.Embed(node.ContentSum)
			if err != nil {
				log.Printf("Embedding error for %s: %v", node.SymbolName, err)
				continue
			}
			store.UpsertEmbedding(node.ID, vec)
		}

		// Add new edges to graph incrementally (nodes are created automatically
		// by AddEdge via ensureNode; isolated nodes without edges don't affect
		// graph signals like PPR/betweenness/in-degree)
		for _, edge := range result.Edges {
			graphEngine.AddEdge(edge)
		}
	}
}
