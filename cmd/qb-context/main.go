package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/maplenk/context-mcp/internal/adr"
	"github.com/maplenk/context-mcp/internal/config"
	"github.com/maplenk/context-mcp/internal/harness"
	"github.com/maplenk/context-mcp/internal/embedding"
	"github.com/maplenk/context-mcp/internal/gitmeta"
	"github.com/maplenk/context-mcp/internal/graph"
	"github.com/maplenk/context-mcp/internal/mcp"
	"github.com/maplenk/context-mcp/internal/parser"
	"github.com/maplenk/context-mcp/internal/search"
	"github.com/maplenk/context-mcp/internal/storage"
	"github.com/maplenk/context-mcp/internal/types"
	"github.com/maplenk/context-mcp/internal/watcher"
)

// indexMu prevents concurrent index operations (M8)
var indexMu sync.Mutex

// asyncScoreWg tracks in-flight async betweenness/PageRank goroutines (H42)
// so the shutdown path can wait for them before closing the store.
var asyncScoreWg sync.WaitGroup

func main() {
	// Route all logging to stderr to avoid corrupting MCP JSON-RPC on stdout
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// L3: Check for subcommands BEFORE flag.Parse() — avoids fragile
	// reliance on flag stopping at non-flag arguments.
	for i, arg := range os.Args[1:] {
		if arg == "cli" {
			cfg, err := config.ParseFlags()
			if err != nil {
				log.Fatalf("Failed to parse flags: %v", err)
			}
			runCLI(cfg, os.Args[i+2:]) // pass everything after "cli"
			return
		}
		if arg == "serve-http" {
			// Remove "serve-http" from os.Args so ParseFlags sees all flags
			// (flags before and after the subcommand).
			os.Args = append(os.Args[:i+1], os.Args[i+2:]...)
			cfg, err := config.ParseFlags()
			if err != nil {
				log.Fatalf("Failed to parse flags: %v", err)
			}
			runServeHTTP(cfg)
			return
		}
		if arg == "install" {
			runInstall(os.Args[i+2:])
			return
		}
		if arg == "uninstall" {
			runUninstall(os.Args[i+2:])
			return
		}
		if arg == "doctor" {
			runDoctor(os.Args[i+2:])
			return
		}
		if arg == "print-config" {
			runPrintConfig(os.Args[i+2:])
			return
		}
	}

	cfg, err := config.ParseFlags()
	if err != nil {
		log.Fatalf("Failed to parse flags: %v", err)
	}

	log.Printf("context-mcp daemon starting — repo: %s", cfg.RepoRoot)

	// 1. Initialize embedding engine — priority: ONNX → Ollama → OpenAI-compat → llama.cpp → TF-IDF
	embedder := initEmbedder(cfg)

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
	defer func() {
		asyncScoreWg.Wait() // H42: wait for in-flight async score goroutines before closing store
		cleanup()
	}()

	// 3. Initialize parser
	p := parser.New()

	// 4. Initialize graph engine
	graphEngine := graph.New()

	// 5. Initialize hybrid search
	hybridSearch := search.New(store, embedder, graphEngine)

	// 6. Run initial indexing
	log.Printf("Starting initial index...")
	if err := indexRepo(cfg, store, p, embedder, graphEngine); err != nil {
		log.Printf("WARNING: Initial index encountered critical errors: %v", err)
	}
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

	// 8. Start incremental update goroutine (H35: tracked with WaitGroup)
	var fileEventsWg sync.WaitGroup
	fileEventsWg.Add(1)
	go func() {
		defer fileEventsWg.Done()
		handleFileEvents(w, cfg, store, p, embedder, graphEngine)
	}()

	// 9. Set up MCP server
	server := mcp.NewServer()
	indexFn := func(path string) error {
		indexMu.Lock()
		defer indexMu.Unlock()
		if path == "" {
			log.Printf("Full re-index triggered")
			if err := indexRepo(cfg, store, p, embedder, graphEngine); err != nil {
				return fmt.Errorf("full re-index failed: %w", err)
			}
		} else {
			// C9: Validate path is within repo root (prevent path traversal)
			// M20: Always join with RepoRoot before resolving, avoids CWD-dependent resolution
			absPath, err := filepath.Abs(filepath.Join(cfg.RepoRoot, path))
			if err != nil {
				return fmt.Errorf("invalid path: %w", err)
			}
			absRoot, absRootErr := filepath.Abs(cfg.RepoRoot)
			if absRootErr != nil {
				return fmt.Errorf("resolving repo root: %w", absRootErr)
			}
			if !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) && absPath != absRoot {
				return fmt.Errorf("path traversal detected: %s is outside repo root", path)
			}
			log.Printf("Targeted re-index triggered for: %s", absPath)
			indexPath(cfg, store, p, embedder, graphEngine, absPath)
		}
		return nil
	}
	mcp.RegisterTools(server, mcp.ToolDeps{
		Store:       store,
		Graph:       graphEngine,
		Search:      hybridSearch,
		RepoRoot:    cfg.RepoRoot,
		Profile:     cfg.Profile,
		Checkpoints: mcp.NewCheckpointStore(),
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
		w.Stop()                // closes Events channel, causing handleFileEvents to return
		fileEventsWg.Wait()     // H35: wait for handleFileEvents to finish before cleanup
		asyncScoreWg.Wait()     // H42: wait for in-flight async score goroutines before closing store
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

	// Normal exit: stop watcher and wait for handleFileEvents before deferred cleanup runs
	w.Stop()
	fileEventsWg.Wait()
}

// runCLI handles the "cli" subcommand for direct tool invocation
func runCLI(cfg *config.Config, args []string) {
	log.SetOutput(os.Stderr)

	// Handle --list flag
	if len(args) > 0 && args[0] == "--list" {
		embedder := initEmbedder(cfg)
		defer embedder.Close()

		store, err := storage.NewStore(cfg.DBPath, cfg.EmbeddingDim)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to init storage: %v\n", err)
			os.Exit(1)
		}
		defer store.Close()
		graphEngine := graph.New()
		hybridSearch := search.New(store, embedder, graphEngine)

		server := mcp.NewServer()
		mcp.RegisterTools(server, mcp.ToolDeps{
			Store: store, Graph: graphEngine, Search: hybridSearch, RepoRoot: cfg.RepoRoot,
			Checkpoints: mcp.NewCheckpointStore(),
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
		fmt.Fprintf(os.Stderr, "Usage: context-mcp cli [--reindex] <tool_name> [json_args]\n")
		fmt.Fprintf(os.Stderr, "       context-mcp cli --list\n")
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

	// Boot pipeline — init embedder first so cfg.EmbeddingDim is set correctly
	embedder := initEmbedder(cfg)
	defer embedder.Close()

	store, err := storage.NewStore(cfg.DBPath, cfg.EmbeddingDim)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to init storage: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	p := parser.New()
	graphEngine := graph.New()
	hybridSearch := search.New(store, embedder, graphEngine)

	// Skip full indexing if DB already has data (use --reindex to force)
	if !forceReindex {
		nodeIDs, _ := store.GetAllNodeIDs()
		if len(nodeIDs) > 0 {
			// Build valid node ID set for ghost-node filtering
			validIDs := make(map[string]bool, len(nodeIDs))
			for _, id := range nodeIDs {
				validIDs[id] = true
			}
			// Rebuild in-memory graph from stored edges
			edges, edgeErr := store.GetAllEdges()
			if edgeErr != nil {
				log.Printf("Failed to load edges from DB: %v", edgeErr)
			} else {
				graphEngine.BuildFromEdges(edges, validIDs)
			}
			log.Printf("Loaded %d nodes from existing index (use --reindex to force)", len(nodeIDs))
		} else {
			// Empty DB — must index
			if err := indexRepo(cfg, store, p, embedder, graphEngine); err != nil {
				fmt.Fprintf(os.Stderr, "WARNING: Index encountered critical errors: %v\n", err)
			}
		}
	} else {
		log.Printf("--reindex flag set, forcing full re-index...")
		if err := indexRepo(cfg, store, p, embedder, graphEngine); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: Re-index encountered critical errors: %v\n", err)
		}
	}

	// Register tools and look up handler
	server := mcp.NewServer()
	mcp.RegisterTools(server, mcp.ToolDeps{
		Store: store, Graph: graphEngine, Search: hybridSearch, RepoRoot: cfg.RepoRoot,
		Checkpoints: mcp.NewCheckpointStore(),
	}, func(path string) error {
		return indexRepo(cfg, store, p, embedder, graphEngine)
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
	// M3/M12: Truncate oversized output while preserving valid JSON.
	// The output from json.MarshalIndent is always JSON, so we wrap truncated
	// content in a valid JSON envelope.
	const maxCLIOutput = 5 * 1024 * 1024
	outStr := string(out)
	if len(outStr) > maxCLIOutput {
		trimmed := strings.TrimSpace(outStr)
		isJSON := len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
		if isJSON {
			const envelope = `{"truncated":true,"message":"Output exceeded 5MB size limit","partial_data":""}`
			budget := maxCLIOutput - len(envelope)
			if budget < 0 {
				budget = 0
			}
			partial := outStr[:budget]
			for !utf8.ValidString(partial) && len(partial) > 0 {
				partial = partial[:len(partial)-1]
			}
			escapedBytes, _ := json.Marshal(partial)
			escaped := string(escapedBytes[1 : len(escapedBytes)-1])
			outStr = `{"truncated":true,"message":"Output exceeded 5MB size limit","partial_data":"` + escaped + `"}`
		} else {
			truncated := outStr[:maxCLIOutput]
			for !utf8.ValidString(truncated) && len(truncated) > 0 {
				truncated = truncated[:len(truncated)-1]
			}
			outStr = truncated + "\n... [truncated, output exceeded 5MB]"
		}
	}
	fmt.Println(outStr)
}

// runServeHTTP starts the MCP server over streamable HTTP transport.
func runServeHTTP(cfg *config.Config) {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	log.Printf("context-mcp HTTP server starting — repo: %s, port: %d", cfg.RepoRoot, cfg.HTTPPort)

	// 1. Initialize embedding engine
	embedder := initEmbedder(cfg)

	// 2. Initialize SQLite storage
	store, err := storage.NewStore(cfg.DBPath, cfg.EmbeddingDim)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	log.Printf("Storage initialized at %s", cfg.DBPath)

	// Unified cleanup
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
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
	if err := indexRepo(cfg, store, p, embedder, graphEngine); err != nil {
		log.Printf("WARNING: Initial index encountered critical errors: %v", err)
	}
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
	var fileEventsWg sync.WaitGroup
	fileEventsWg.Add(1)
	go func() {
		defer fileEventsWg.Done()
		handleFileEvents(w, cfg, store, p, embedder, graphEngine)
	}()

	// 9. Create MCP server and register tools
	mcpSrv := mcp.NewServer()
	indexFn := func(path string) error {
		indexMu.Lock()
		defer indexMu.Unlock()
		if path == "" {
			log.Printf("Full re-index triggered")
			if err := indexRepo(cfg, store, p, embedder, graphEngine); err != nil {
				return fmt.Errorf("full re-index failed: %w", err)
			}
		} else {
			absPath, err := filepath.Abs(filepath.Join(cfg.RepoRoot, path))
			if err != nil {
				return fmt.Errorf("invalid path: %w", err)
			}
			absRoot, absRootErr := filepath.Abs(cfg.RepoRoot)
			if absRootErr != nil {
				return fmt.Errorf("resolving repo root: %w", absRootErr)
			}
			if !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) && absPath != absRoot {
				return fmt.Errorf("path traversal detected: %s is outside repo root", path)
			}
			log.Printf("Targeted re-index triggered for: %s", absPath)
			indexPath(cfg, store, p, embedder, graphEngine, absPath)
		}
		return nil
	}
	mcp.RegisterTools(mcpSrv, mcp.ToolDeps{
		Store:       store,
		Graph:       graphEngine,
		Search:      hybridSearch,
		RepoRoot:    cfg.RepoRoot,
		Profile:     cfg.Profile,
		Checkpoints: mcp.NewCheckpointStore(),
	}, indexFn)

	// 10. Create streamable HTTP transport
	var httpOpts []mcpserver.StreamableHTTPOption
	if cfg.HTTPBearerToken != "" {
		log.Printf("Bearer token authentication enabled")
	}

	httpServer := mcpserver.NewStreamableHTTPServer(mcpSrv.MCPServer(), httpOpts...)

	// 11. Build the HTTP handler (with optional bearer token middleware)
	var handler http.Handler = httpServer
	if cfg.HTTPBearerToken != "" {
		expected := "Bearer " + cfg.HTTPBearerToken
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			httpServer.ServeHTTP(w, r)
		})
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.HTTPPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Printf("MCP HTTP server listening on %s", addr)

	// 12. Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down HTTP server...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		w.Stop()
		fileEventsWg.Wait()
		asyncScoreWg.Wait()
		close(done)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("HTTP server error: %v", err)
	}
	<-done // wait for cleanup to finish

	log.Printf("HTTP server stopped")
}

// initEmbedder creates an embedder based on config flags.
// Priority: ONNX → Ollama → OpenAI-compat → llama.cpp → TF-IDF.
// On backend init failure, falls back to TF-IDF and resets cfg.EmbeddingDim.
func initEmbedder(cfg *config.Config) embedding.Embedder {
	switch {
	case cfg.ONNXModelDir != "":
		dim := cfg.EmbeddingDim
		if dim == storage.DefaultEmbeddingDim {
			dim = embedding.DefaultDimForModel(cfg.ONNXModelDir)
		}
		onnxEmb, err := embedding.NewONNXEmbedder(cfg.ONNXModelDir, dim, cfg.ONNXLibPath)
		if err != nil {
			log.Printf("WARNING: ONNX model requested (--onnx-model=%s) but initialization failed: %v. Falling back to TFIDF.", cfg.ONNXModelDir, err)
			cfg.EmbeddingDim = embedding.GetEmbeddingDim()
			return embedding.NewEmbedder()
		}
		embedding.SetEmbeddingDim(dim)
		cfg.EmbeddingDim = dim
		log.Printf("ONNX embedder initialized (dim=%d, model=%s)", dim, cfg.ONNXModelDir)
		return onnxEmb
	case cfg.OllamaEndpoint != "":
		ollamaEmb, err := embedding.NewOllamaEmbedder(cfg.OllamaEndpoint, cfg.OllamaModel, cfg.EmbeddingDim)
		if err != nil {
			log.Printf("WARNING: Ollama requested (--ollama-endpoint=%s) but initialization failed: %v. Falling back to TFIDF.", cfg.OllamaEndpoint, err)
			cfg.EmbeddingDim = embedding.GetEmbeddingDim()
			return embedding.NewEmbedder()
		}
		embedding.SetEmbeddingDim(cfg.EmbeddingDim)
		log.Printf("Ollama embedder initialized (dim=%d, model=%s, endpoint=%s)", cfg.EmbeddingDim, cfg.OllamaModel, cfg.OllamaEndpoint)
		return ollamaEmb
	case cfg.OpenAIEndpoint != "":
		openaiEmb, err := embedding.NewOpenAIEmbedder(cfg.OpenAIEndpoint, cfg.OpenAIModel, cfg.EmbeddingDim)
		if err != nil {
			log.Printf("WARNING: OpenAI-compatible endpoint requested (--openai-endpoint=%s) but initialization failed: %v. Falling back to TFIDF.", cfg.OpenAIEndpoint, err)
			cfg.EmbeddingDim = embedding.GetEmbeddingDim()
			return embedding.NewEmbedder()
		}
		embedding.SetEmbeddingDim(cfg.EmbeddingDim)
		log.Printf("OpenAI-compatible embedder initialized (dim=%d, model=%s, endpoint=%s)", cfg.EmbeddingDim, cfg.OpenAIModel, cfg.OpenAIEndpoint)
		return openaiEmb
	case cfg.LlamaCppEndpoint != "":
		llamaEmb, err := embedding.NewLlamaCppEmbedder(cfg.LlamaCppEndpoint, cfg.EmbeddingDim)
		if err != nil {
			log.Printf("WARNING: llama.cpp requested (--llamacpp-endpoint=%s) but initialization failed: %v. Falling back to TFIDF.", cfg.LlamaCppEndpoint, err)
			cfg.EmbeddingDim = embedding.GetEmbeddingDim()
			return embedding.NewEmbedder()
		}
		embedding.SetEmbeddingDim(cfg.EmbeddingDim)
		log.Printf("llama.cpp embedder initialized (dim=%d, endpoint=%s)", cfg.EmbeddingDim, cfg.LlamaCppEndpoint)
		return llamaEmb
	default:
		log.Printf("Embedding engine initialized (TF-IDF, dim=%d)", embedding.GetEmbeddingDim())
		return embedding.NewEmbedder()
	}
}

// indexRepo performs a full index of the repository.
// Returns an error for critical failures (walk failure, node storage failure).
// Non-critical failures (individual file parse/embedding errors) are logged but do not cause an error return.
func indexRepo(cfg *config.Config, store *storage.Store, p *parser.Parser, embedder embedding.Embedder, graphEngine *graph.GraphEngine) error {
	// L4: Use standalone walk function — no fsnotify watcher allocation needed
	files, err := watcher.WalkSourceFiles(cfg.RepoRoot, cfg.ExcludedDirs)
	if err != nil {
		log.Printf("Failed to walk repository: %v", err)
		return fmt.Errorf("failed to walk repository: %w", err)
	}
	log.Printf("Found %d source files to index", len(files))

	// H2: Reconcile database against filesystem — purge data for deleted files
	knownFiles, err := store.GetAllFilePaths()
	if err != nil {
		log.Printf("Warning: failed to get known files for reconciliation: %v", err)
	} else {
		currentFiles := make(map[string]bool, len(files))
		for _, f := range files {
			currentFiles[f] = true
		}
		var purged int
		for _, known := range knownFiles {
			if !currentFiles[known] {
				if err := store.DeleteByFile(known); err != nil {
					log.Printf("Warning: failed to purge stale file %s: %v", known, err)
				} else {
					purged++
				}
			}
		}
		if purged > 0 {
			log.Printf("Purged %d stale files from index", purged)
		}
	}

	// Parse files using a worker pool
	type parseJob struct {
		relPath string
	}
	type parseResult struct {
		nodes []types.ASTNode
		edges []types.ASTEdge
	}

	var wg sync.WaitGroup

	// Determine worker count
	workerCount := cfg.WorkerCount
	if workerCount > len(files) {
		workerCount = len(files)
	}
	if workerCount < 1 {
		workerCount = 1
	}

	// H50: Cap channel buffers to avoid unbounded memory for large repos.
	// Workers naturally throttle via channel backpressure.
	chanBufSize := min(len(files), workerCount*4)
	jobs := make(chan parseJob, chanBufSize)
	results := make(chan parseResult, chanBufSize)

	// Launch workers

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

	// File document nodes: index config, Blade views, SQL schemas with full content.
	// For files already parsed by tree-sitter (e.g., config/*.php, *.blade.php),
	// the file doc node has the same ID as the tree-sitter file node and will
	// overwrite it with richer content (file content instead of just the path).
	// For files not handled by tree-sitter (e.g., *.sql), this is the only node.
	fileDocFiles, fdErr := watcher.WalkAllFiles(cfg.RepoRoot, cfg.ExcludedDirs)
	if fdErr != nil {
		log.Printf("Warning: failed to walk for file doc candidates: %v", fdErr)
	} else {
		var fileDocCount int
		for _, rel := range fileDocFiles {
			if !parser.IsFileDocCandidate(rel) {
				continue
			}
			absPath := filepath.Join(cfg.RepoRoot, rel)
			node, err := parser.CreateFileDocNode(absPath, rel)
			if err != nil {
				continue
			}
			allNodes = append(allNodes, *node)
			fileDocCount++
		}
		if fileDocCount > 0 {
			log.Printf("Indexed %d file document nodes (config, Blade, SQL)", fileDocCount)
		}
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

	// Store nodes (critical — without stored nodes, the index is useless)
	if len(allNodes) > 0 {
		if err := store.UpsertNodes(allNodes); err != nil {
			log.Printf("Failed to store nodes: %v", err)
			return fmt.Errorf("failed to store nodes: %w", err)
		}
		log.Printf("Stored %d nodes", len(allNodes))
	}

	// Store edges
	if len(allEdges) > 0 {
		if err := store.UpsertEdges(allEdges); err != nil {
			log.Printf("Failed to store edges: %v", err)
		} else {
			log.Printf("Stored %d edges", len(allEdges))
		}
	}

	// --- Cold Start: Git metadata ingestion ---
	if cfg.ColdStartEnabled {
		gitCfg := gitmeta.Config{
			HistoryDepth:     cfg.GitHistoryDepth,
			PerFileCommitCap: cfg.GitPerFileCommitCap,
			MaxMessageBytes:  cfg.GitMaxMessageBytes,
			MaxIntentBytes:   cfg.GitMaxIntentBytes,
		}
		extractor, gitErr := gitmeta.NewExtractor(cfg.RepoRoot, gitCfg)
		if gitErr != nil {
			log.Printf("Cold Start: failed to open git repo: %v", gitErr)
		} else if extractor != nil {
			gitCtx, gitCancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer gitCancel()

			// 1. Repo snapshot
			snap, snapErr := extractor.Snapshot()
			if snapErr != nil {
				log.Printf("Cold Start: snapshot error: %v", snapErr)
			} else {
				if err := store.UpsertRepoSnapshot(*snap); err != nil {
					log.Printf("Cold Start: failed to store snapshot: %v", err)
				} else {
					log.Printf("Cold Start: snapshot stored (%s)", snap.Summary)
				}
			}

			// 2. Recent commits
			commits, commitErr := extractor.RecentCommits(gitCtx, 0)
			if commitErr != nil {
				log.Printf("Cold Start: commit history error: %v", commitErr)
			} else if len(commits) > 0 {
				if err := store.UpsertGitCommits(commits); err != nil {
					log.Printf("Cold Start: failed to store commits: %v", err)
				} else {
					log.Printf("Cold Start: stored %d commits", len(commits))
				}
			}

			// 3. File history
			changes, histErr := extractor.FileHistory(gitCtx, nil) // all files
			if histErr != nil {
				log.Printf("Cold Start: file history error: %v", histErr)
			} else if len(changes) > 0 {
				if err := store.UpsertFileHistory(changes); err != nil {
					log.Printf("Cold Start: failed to store file history: %v", err)
				} else {
					log.Printf("Cold Start: stored %d file-commit associations", len(changes))
				}

				// 4. Compact file intents
				intents := extractor.CompactFileIntents(changes)
				if len(intents) > 0 {
					if err := store.UpsertFileIntents(intents); err != nil {
						log.Printf("Cold Start: failed to store file intents: %v", err)
					} else {
						log.Printf("Cold Start: stored intent summaries for %d files", len(intents))
					}
				}
			}
		} else {
			log.Printf("Cold Start: not a git repository, skipping git metadata")
		}
	}

	// Look up file intents for embedding enrichment
	var fileIntents map[string]*gitmeta.FileIntent
	if cfg.ColdStartEnabled {
		// Collect unique file paths
		filePaths := make(map[string]bool)
		for _, n := range allNodes {
			filePaths[n.FilePath] = true
		}
		paths := make([]string, 0, len(filePaths))
		for p := range filePaths {
			paths = append(paths, p)
		}
		var fiErr error
		fileIntents, fiErr = store.GetFileIntentsByPaths(paths)
		if fiErr != nil {
			log.Printf("Cold Start: failed to look up file intents for embedding: %v", fiErr)
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
				text := node.ContentSum
				if fileIntents != nil {
					if fi, ok := fileIntents[node.FilePath]; ok && fi.IntentText != "" {
						// Append bounded git intent to embedding text
						text = text + "\n[git-intent]\n" + fi.IntentText
					}
				}
				texts[j] = text
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
		graphEngine.BuildFromEdges(edges, nodeIDSet)
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

	return nil
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
		// M8: Use symlink-aware walk with gitignore support (matches indexRepo)
		walked, walkErr := watcher.WalkSourceFilesUnder(cfg.RepoRoot, absTarget, cfg.ExcludedDirs)
		if walkErr != nil {
			log.Printf("Failed to walk directory %s: %v", targetPath, walkErr)
			return
		}
		files = walked
	} else {
		relPath, err := filepath.Rel(cfg.RepoRoot, absTarget)
		if err != nil {
			log.Printf("Cannot compute relative path for %s: %v", absTarget, err)
			return
		}
		files = []string{relPath}
	}

	log.Printf("Targeted re-index: %d files under %s", len(files), targetPath)

	// Collect all parsed nodes and edges across files for cross-file resolution
	var allNewNodes []types.ASTNode
	var allNewEdges []types.ASTEdge

	for _, relPath := range files {
		absPath := filepath.Join(cfg.RepoRoot, relPath)

		// Delete old data for this file
		if err := store.DeleteByFile(relPath); err != nil {
			log.Printf("Failed to delete stale data for %s: %v", relPath, err)
		}

		// Re-parse: tree-sitter for supported files, file doc for candidates
		var fileNodes []types.ASTNode
		var fileEdges []types.ASTEdge

		if parser.IsSupported(absPath) {
			result, err := p.ParseFile(absPath, cfg.RepoRoot)
			if err != nil {
				log.Printf("Parse error %s: %v", relPath, err)
				continue
			}
			fileNodes = result.Nodes
			fileEdges = result.Edges
		}

		// File doc candidates get a content-rich file node
		if parser.IsFileDocCandidate(relPath) {
			node, err := parser.CreateFileDocNode(absPath, relPath)
			if err != nil {
				log.Printf("File doc error %s: %v", relPath, err)
			} else {
				fileNodes = append(fileNodes, *node)
			}
		}

		if len(fileNodes) == 0 {
			continue // unsupported file type with no file doc match
		}

		allNewNodes = append(allNewNodes, fileNodes...)
		allNewEdges = append(allNewEdges, fileEdges...)

		// Store nodes first (needed for symbol index query below)
		if len(fileNodes) > 0 {
			if err := store.UpsertNodes(fileNodes); err != nil {
				log.Printf("Failed to store nodes for %s: %v", relPath, err)
			}
		}

		// Generate embeddings
		for _, node := range fileNodes {
			vec, err := embedder.Embed(node.ContentSum)
			if err != nil {
				log.Printf("Embedding error for %s: %v", node.SymbolName, err)
				continue
			}
			store.UpsertEmbedding(node.ID, vec)
		}
	}

	// H32: Cross-file edge resolution — same logic as indexRepo
	if len(allNewEdges) > 0 {
		// Build symbol index from ALL nodes in the store (includes just-inserted nodes)
		symbolIndex, siErr := store.GetSymbolIndex()
		if siErr != nil {
			log.Printf("Failed to build symbol index for cross-file resolution: %v", siErr)
		} else {
			// Also include new nodes that might be class/struct/interface but not yet
			// returned by GetSymbolIndex (race-free: we already upserted above)
			// Build nodeID set for resolution check
			nodeIDSet := make(map[string]bool, len(allNewNodes))
			for _, n := range allNewNodes {
				nodeIDSet[n.ID] = true
			}
			// Extend with all existing node IDs from symbolIndex values
			for _, id := range symbolIndex {
				nodeIDSet[id] = true
			}

			resolved := 0
			for i, e := range allNewEdges {
				if !nodeIDSet[e.TargetID] && e.TargetSymbol != "" {
					if rid, ok := symbolIndex[e.TargetSymbol]; ok {
						allNewEdges[i].TargetID = rid
						resolved++
					}
				}
			}
			if resolved > 0 {
				log.Printf("Cross-file edge resolution: resolved %d edges", resolved)
			}
		}

		// Store edges after resolution
		if err := store.UpsertEdges(allNewEdges); err != nil {
			log.Printf("Failed to store edges: %v", err)
		}
	}

	// Rebuild graph after targeted re-index
	edges, err := store.GetAllEdges()
	if err == nil {
		// Build valid node ID set for ghost-node filtering
		allNodeIDs, _ := store.GetAllNodeIDs()
		validIDs := make(map[string]bool, len(allNodeIDs))
		for _, id := range allNodeIDs {
			validIDs[id] = true
		}
		graphEngine.BuildFromEdges(edges, validIDs)
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

	// --- Cold Start: incremental refresh for changed files ---
	if cfg.ColdStartEnabled {
		gitCfg := gitmeta.Config{
			HistoryDepth:     cfg.GitHistoryDepth,
			PerFileCommitCap: cfg.GitPerFileCommitCap,
			MaxMessageBytes:  cfg.GitMaxMessageBytes,
			MaxIntentBytes:   cfg.GitMaxIntentBytes,
		}
		extractor, gitErr := gitmeta.NewExtractor(cfg.RepoRoot, gitCfg)
		if gitErr != nil {
			log.Printf("Cold Start incremental: failed to open git repo: %v", gitErr)
		} else if extractor != nil {
			gitCtx, gitCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer gitCancel()

			// Build set of affected file paths (relative to repo root)
			affectedFiles := make(map[string]bool)
			for _, f := range files {
				affectedFiles[f] = true
			}

			if len(affectedFiles) > 0 {
				changes, chErr := extractor.FileHistory(gitCtx, affectedFiles)
				if chErr != nil {
					log.Printf("Cold Start incremental: file history error: %v", chErr)
				} else if len(changes) > 0 {
					if err := store.UpsertFileHistory(changes); err != nil {
						log.Printf("Cold Start incremental: failed to store file history: %v", err)
					}
					intents := extractor.CompactFileIntents(changes)
					if len(intents) > 0 {
						if err := store.UpsertFileIntents(intents); err != nil {
							log.Printf("Cold Start incremental: failed to store intents: %v", err)
						}
					}
				}
			}

			// Update snapshot
			snap, snapErr := extractor.Snapshot()
			if snapErr == nil {
				if err := store.UpsertRepoSnapshot(*snap); err != nil {
					log.Printf("Cold Start incremental: failed to update snapshot: %v", err)
				}
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
			asyncScoreWg.Add(1)
			go func() {
				defer asyncScoreWg.Done()
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

		// Save incoming cross-file edges before deletion (H1 fix)
		incomingEdges, icErr := store.GetIncomingCrossFileEdges(event.Path)
		if icErr != nil {
			log.Printf("Failed to get incoming cross-file edges for %s: %v", event.Path, icErr)
		}

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

		// Re-parse the file: tree-sitter for supported files, file doc for candidates
		var resultNodes []types.ASTNode
		var resultEdges []types.ASTEdge

		if parser.IsSupported(absPath) {
			result, err := p.ParseFile(absPath, cfg.RepoRoot)
			if err != nil {
				log.Printf("Parse error %s: %v", event.Path, err)
				return
			}
			resultNodes = result.Nodes
			resultEdges = result.Edges
		}

		// File doc candidates get a content-rich file node (overwrites the
		// sparse tree-sitter file node for files that are also IsSupported).
		if parser.IsFileDocCandidate(event.Path) {
			node, err := parser.CreateFileDocNode(absPath, event.Path)
			if err != nil {
				log.Printf("File doc error %s: %v", event.Path, err)
			} else {
				resultNodes = append(resultNodes, *node)
			}
		}

		if len(resultNodes) == 0 {
			return // unsupported file type with no file doc match
		}

		// Store new nodes first (needed before edge resolution)
		if len(resultNodes) > 0 {
			if err := store.UpsertNodes(resultNodes); err != nil {
				log.Printf("Failed to store nodes: %v", err)
			}
		}

		// Resolve cross-file edges using TargetSymbol (matches indexRepo behavior)
		if len(resultEdges) > 0 {
			symbolIndex, siErr := store.GetSymbolIndex()
			if siErr != nil {
				log.Printf("Failed to get symbol index for cross-file resolution: %v", siErr)
			} else {
				nodeIDSet := make(map[string]bool, len(resultNodes))
				for _, n := range resultNodes {
					nodeIDSet[n.ID] = true
				}
				for i, e := range resultEdges {
					if !nodeIDSet[e.TargetID] && e.TargetSymbol != "" {
						if resolved, ok := symbolIndex[e.TargetSymbol]; ok {
							resultEdges[i].TargetID = resolved
						}
					}
				}
			}
		}

		if len(resultEdges) > 0 {
			if err := store.UpsertEdges(resultEdges); err != nil {
				log.Printf("Failed to store edges: %v", err)
			}
		}

		// Cold Start: look up file intent to enrich embeddings
		var intentText string
		if cfg.ColdStartEnabled {
			fi, fiErr := store.GetFileIntent(event.Path)
			if fiErr == nil && fi != nil && fi.IntentText != "" {
				intentText = fi.IntentText
			}
		}

		// Generate embeddings for new/updated nodes
		for _, node := range resultNodes {
			text := node.ContentSum
			if intentText != "" {
				text = text + "\n[git-intent]\n" + intentText
			}
			vec, err := embedder.Embed(text)
			if err != nil {
				log.Printf("Embedding error for %s: %v", node.SymbolName, err)
				continue
			}
			store.UpsertEmbedding(node.ID, vec)
		}

		// Add new edges to graph incrementally (nodes are created automatically
		// by AddEdge via ensureNode; isolated nodes without edges don't affect
		// graph signals like PPR/betweenness/in-degree)
		for _, edge := range resultEdges {
			graphEngine.AddEdge(edge)
		}

		// Restore incoming cross-file edges whose target nodes still exist (H1 fix)
		if len(incomingEdges) > 0 {
			newNodeIDs, niErr := store.GetNodeIDsByFile(event.Path)
			if niErr == nil {
				newNodeSet := make(map[string]bool, len(newNodeIDs))
				for _, id := range newNodeIDs {
					newNodeSet[id] = true
				}
				var validIncoming []types.ASTEdge
				for _, e := range incomingEdges {
					if newNodeSet[e.TargetID] {
						validIncoming = append(validIncoming, e)
					}
				}
				if len(validIncoming) > 0 {
					if err := store.UpsertEdges(validIncoming); err != nil {
						log.Printf("Failed to restore incoming cross-file edges: %v", err)
					}
					// Add restored edges to graph
					for _, edge := range validIncoming {
						graphEngine.AddEdge(edge)
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// P2: Install / Doctor / PrintConfig / Uninstall subcommands
// ---------------------------------------------------------------------------

// validateClient checks --client flag value.
func validateClient(c string, required bool) {
	if c == "" && required {
		fmt.Fprintf(os.Stderr, "Error: --client is required (claude-code or codex)\n")
		os.Exit(1)
	}
	if c != "" && c != "claude-code" && c != "codex" {
		fmt.Fprintf(os.Stderr, "Error: invalid --client %q, must be claude-code or codex\n", c)
		os.Exit(1)
	}
}

// validateProfile checks --profile flag value.
func validateProfile(p string) {
	switch p {
	case "core", "extended", "full":
	default:
		fmt.Fprintf(os.Stderr, "Error: invalid --profile %q, must be core, extended, or full\n", p)
		os.Exit(1)
	}
}

// validateTransport checks --transport flag value.
func validateTransport(t string) {
	switch t {
	case "stdio", "http", "sse":
	default:
		fmt.Fprintf(os.Stderr, "Error: invalid --transport %q, must be stdio, http, or sse\n", t)
		os.Exit(1)
	}
}

// validateScope checks --scope flag value.
func validateScope(scope, client string) {
	if scope == "" {
		return
	}
	switch scope {
	case "user", "local", "project":
	default:
		fmt.Fprintf(os.Stderr, "Error: invalid --scope %q, must be user, local, or project\n", scope)
		os.Exit(1)
	}
	if client == "codex" && scope != "" && scope != "user" {
		fmt.Fprintf(os.Stderr, "Error: --scope is only supported for claude-code\n")
		os.Exit(1)
	}
}

func runInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	client := fs.String("client", "", "Target client: claude-code or codex (required)")
	profile := fs.String("profile", "extended", "Tool profile: core, extended, or full")
	repo := fs.String("repo", ".", "Repository root path")
	force := fs.Bool("force", false, "Overwrite existing configuration")
	transport := fs.String("transport", "stdio", "Transport type: stdio, http, or sse")
	url := fs.String("url", "", "Server URL (required when --transport is http or sse)")
	scope := fs.String("scope", "", "Installation scope: user, local, or project (claude-code only)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	validateClient(*client, true)
	validateProfile(*profile)
	validateTransport(*transport)
	validateScope(*scope, *client)

	if (*transport == "http" || *transport == "sse") && *url == "" {
		fmt.Fprintf(os.Stderr, "Error: --url is required when --transport is %s\n", *transport)
		os.Exit(1)
	}

	// Suppress unused variable warnings — these flags are validated above and
	// will be wired into InstallOpts when remote transport support is added.
	_ = transport
	_ = url
	_ = scope

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving repo path: %v\n", err)
		os.Exit(1)
	}

	msg, err := harness.Install(harness.InstallOpts{
		Client:   harness.Client(*client),
		Profile:  *profile,
		RepoRoot: absRepo,
		Force:    *force,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(msg)
}

func runUninstall(args []string) {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	client := fs.String("client", "", "Target client: claude-code or codex (required)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	validateClient(*client, true)

	msg, err := harness.Uninstall(harness.UninstallOpts{
		Client: harness.Client(*client),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(msg)
}

func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	client := fs.String("client", "", "Target client: claude-code or codex (optional, checks all if omitted)")
	repo := fs.String("repo", "", "Repository root path to check (optional)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *repo != "" {
		absRepo, err := filepath.Abs(*repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving repo path: %v\n", err)
			os.Exit(1)
		}
		*repo = absRepo
	}

	validateClient(*client, false)

	checks, err := harness.Doctor(harness.DoctorOpts{
		Client:   harness.Client(*client),
		RepoRoot: *repo,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	anyFailed := false
	for _, c := range checks {
		if c.Passed {
			fmt.Printf("[PASS] %s: %s\n", c.Name, c.Message)
		} else {
			anyFailed = true
			fmt.Printf("[FAIL] %s: %s\n", c.Name, c.Message)
			if c.Fix != "" {
				fmt.Printf("       Fix: %s\n", c.Fix)
			}
		}
	}
	if anyFailed {
		os.Exit(1)
	}
}

func runPrintConfig(args []string) {
	fs := flag.NewFlagSet("print-config", flag.ContinueOnError)
	client := fs.String("client", "", "Target client: claude-code or codex (required)")
	profile := fs.String("profile", "extended", "Tool profile: core, extended, or full")
	repo := fs.String("repo", ".", "Repository root path")
	transport := fs.String("transport", "stdio", "Transport type: stdio, http, or sse")
	url := fs.String("url", "", "Server URL (required when --transport is http or sse)")
	scope := fs.String("scope", "", "Installation scope: user, local, or project (claude-code only)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	validateClient(*client, true)
	validateProfile(*profile)
	validateTransport(*transport)
	validateScope(*scope, *client)

	if (*transport == "http" || *transport == "sse") && *url == "" {
		fmt.Fprintf(os.Stderr, "Error: --url is required when --transport is %s\n", *transport)
		os.Exit(1)
	}

	// Suppress unused variable warnings — these flags are validated above and
	// will be wired into PrintConfigOpts when remote transport support is added.
	_ = transport
	_ = url
	_ = scope

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving repo path: %v\n", err)
		os.Exit(1)
	}

	output, err := harness.PrintConfig(harness.PrintConfigOpts{
		Client:   harness.Client(*client),
		Profile:  *profile,
		RepoRoot: absRepo,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(output)
}
