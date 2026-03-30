package main

import (
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

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

func main() {
	// Route all logging to stderr to avoid corrupting MCP JSON-RPC on stdout
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg := config.ParseFlags()
	log.Printf("qb-context daemon starting — repo: %s", cfg.RepoRoot)

	// 1. Initialize SQLite storage
	store, err := storage.NewStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	defer store.Close()
	log.Printf("Storage initialized at %s", cfg.DBPath)

	// 2. Initialize embedding engine
	embedder := embedding.NewHashEmbedder()
	defer embedder.Close()
	log.Printf("Embedding engine initialized (hash-based fallback)")

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
	defer w.Stop()
	log.Printf("Filesystem watcher started")

	// 8. Start incremental update goroutine
	go handleFileEvents(w, cfg, store, p, embedder, graphEngine)

	// 9. Set up MCP server
	server := mcp.NewServer()
	indexFn := func(path string) error {
		log.Printf("Re-indexing triggered for: %s", path)
		indexRepo(cfg, store, p, embedder, graphEngine)
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
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		w.Stop()
		store.Close()
		embedder.Close()
		os.Exit(0)
	}()

	// 11. Start serving MCP requests (blocks until stdin closes)
	if err := server.Serve(); err != nil {
		log.Fatalf("MCP server error: %v", err)
	}
}

// indexRepo performs a full index of the repository
func indexRepo(cfg *config.Config, store *storage.Store, p *parser.Parser, embedder embedding.Embedder, graphEngine *graph.GraphEngine) {
	// Walk the repo to find all source files
	w, err := watcher.New(cfg.RepoRoot, cfg.DebounceInterval, cfg.ExcludedDirs)
	if err != nil {
		log.Printf("Failed to create walker: %v", err)
		return
	}

	files, err := w.WalkExisting()
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
				log.Printf("Embedding batch error: %v", err)
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
}

// handleFileEvents processes incremental file change events from the watcher
func handleFileEvents(w *watcher.Watcher, cfg *config.Config, store *storage.Store, p *parser.Parser, embedder embedding.Embedder, graphEngine *graph.GraphEngine) {
	for event := range w.Events() {
		absPath := filepath.Join(cfg.RepoRoot, event.Path)

		switch event.Action {
		case types.FileEventDeleted:
			log.Printf("File deleted: %s", event.Path)
			if err := store.DeleteByFile(event.Path); err != nil {
				log.Printf("Failed to delete file data: %v", err)
			}
			// Rebuild graph after deletion
			edges, err := store.GetAllEdges()
			if err == nil {
				graphEngine.BuildFromEdges(edges)
			}

		case types.FileEventCreated, types.FileEventModified:
			log.Printf("File changed: %s", event.Path)

			// Delete old data for this file
			store.DeleteByFile(event.Path)

			// Re-parse the file
			result, err := p.ParseFile(absPath, cfg.RepoRoot)
			if err != nil {
				log.Printf("Parse error %s: %v", event.Path, err)
				continue
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

			// Rebuild graph
			edges, err := store.GetAllEdges()
			if err == nil {
				graphEngine.BuildFromEdges(edges)
			}
		}
	}
}
