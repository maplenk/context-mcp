package config

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Config holds all configuration for the context-mcp daemon
type Config struct {
	// RepoRoot is the absolute path to the repository to index
	RepoRoot string

	// DBPath is the path to the SQLite database file
	DBPath string

	// DebounceInterval is the duration to wait before processing filesystem events
	DebounceInterval time.Duration

	// MaxBFSDepth is the maximum depth for blast radius BFS traversal
	MaxBFSDepth int

	// EmbeddingBatchSize is the number of nodes to embed in a single batch
	EmbeddingBatchSize int

	// WorkerCount is the number of parallel workers for file parsing
	WorkerCount int

	// ExcludedDirs are additional directories to exclude beyond .gitignore
	ExcludedDirs []string

	// ONNXModelDir is the path to the ONNX model directory (contains model_quantized.onnx + tokenizer.json).
	// Empty string disables ONNX and uses the TFIDF fallback embedder.
	ONNXModelDir string

	// ONNXLibPath is the path to the ONNX Runtime shared library (libonnxruntime.dylib/.so/.dll).
	// Required when ONNXModelDir is set.
	ONNXLibPath string

	// EmbeddingDim is the embedding vector dimension. For ONNX models with
	// Matryoshka support, valid values are 64, 128, 256, 512, 896.
	// Defaults to 384 (TFIDF) or 256 (ONNX).
	EmbeddingDim int

	// ColdStartEnabled enables Git-derived intent metadata ingestion
	ColdStartEnabled bool

	// GitHistoryDepth is the maximum number of commits to scan per repository
	GitHistoryDepth int

	// GitPerFileCommitCap is the maximum commits to associate per file
	GitPerFileCommitCap int

	// GitMaxMessageBytes is the maximum bytes per commit message/body to store
	GitMaxMessageBytes int

	// GitMaxIntentBytes is the maximum bytes for compacted file intent text
	GitMaxIntentBytes int

	// Profile selects which tools are registered for MCP SDK mode.
	// Valid values: "core" (7 tools), "extended" (14 tools), "full" (all 17).
	// CLI mode always registers all 17 tools regardless of profile.
	Profile string

	// OllamaEndpoint is the Ollama API endpoint (e.g., http://localhost:11434).
	// Empty string disables the Ollama backend.
	OllamaEndpoint string

	// OllamaModel is the Ollama model name for embeddings (e.g., nomic-embed-code).
	OllamaModel string

	// LlamaCppEndpoint is the llama.cpp server endpoint (e.g., http://localhost:8080).
	// Empty string disables the llama.cpp backend.
	LlamaCppEndpoint string

	// OpenAIEndpoint is the OpenAI-compatible API endpoint (e.g., http://localhost:1234).
	// Empty string disables the OpenAI-compatible backend.
	OpenAIEndpoint string

	// OpenAIModel is the model name for the OpenAI-compatible embeddings API.
	OpenAIModel string

	// HTTPPort is the port for serve-http mode (default: 8080)
	HTTPPort int

	// HTTPBearerToken is the optional bearer token for HTTP authentication.
	// When set, requests must include "Authorization: Bearer <token>".
	HTTPBearerToken string
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		RepoRoot:            ".",
		DBPath:              ".context-mcp/index.db",
		DebounceInterval:    500 * time.Millisecond,
		MaxBFSDepth:         5,
		EmbeddingBatchSize:  32,
		WorkerCount:         4,
		ExcludedDirs:        []string{".git", ".context-mcp"},
		ONNXModelDir:        detectDefaultONNXModel(),
		EmbeddingDim:        384,
		ColdStartEnabled:    true,
		GitHistoryDepth:     500,
		GitPerFileCommitCap: 20,
		GitMaxMessageBytes:  2000,
		GitMaxIntentBytes:   1500,
		Profile:             "core",
		OllamaModel:         "nomic-embed-code",
		OpenAIModel:         "text-embedding-nomic-embed-code",
		HTTPPort:            8080,
	}
}

// ParseFlags populates the config from command-line flags.
// Uses a dedicated FlagSet so context-mcp can be embedded in programs that also
// use the global flag package, and returns an error on unknown flags instead of
// calling os.Exit(2).
func ParseFlags() (*Config, error) {
	cfg := DefaultConfig()

	fs := flag.NewFlagSet("context-mcp", flag.ContinueOnError)
	fs.StringVar(&cfg.RepoRoot, "repo", cfg.RepoRoot, "Path to the repository root")
	fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "Path to the SQLite database file")
	fs.DurationVar(&cfg.DebounceInterval, "debounce", cfg.DebounceInterval, "Filesystem event debounce interval")
	fs.IntVar(&cfg.MaxBFSDepth, "max-depth", cfg.MaxBFSDepth, "Maximum BFS traversal depth for impact analysis")
	fs.IntVar(&cfg.EmbeddingBatchSize, "batch-size", cfg.EmbeddingBatchSize, "Embedding batch size")
	fs.IntVar(&cfg.WorkerCount, "workers", cfg.WorkerCount, "Number of parallel parsing workers")
	fs.StringVar(&cfg.ONNXModelDir, "onnx-model", cfg.ONNXModelDir, "Path to ONNX model directory (enables neural embeddings)")
	fs.StringVar(&cfg.ONNXLibPath, "onnx-lib", cfg.ONNXLibPath, "Path to ONNX Runtime shared library")
	fs.IntVar(&cfg.EmbeddingDim, "embedding-dim", cfg.EmbeddingDim, "Embedding vector dimension (ONNX Matryoshka: 64/128/256/512/896)")
	fs.BoolVar(&cfg.ColdStartEnabled, "cold-start", cfg.ColdStartEnabled, "Enable Git-derived intent metadata ingestion")
	fs.IntVar(&cfg.GitHistoryDepth, "git-history-depth", cfg.GitHistoryDepth, "Maximum commits to scan per repository")
	fs.IntVar(&cfg.GitPerFileCommitCap, "git-per-file-cap", cfg.GitPerFileCommitCap, "Maximum commits per file")
	fs.IntVar(&cfg.GitMaxMessageBytes, "git-max-message", cfg.GitMaxMessageBytes, "Maximum bytes per commit message")
	fs.IntVar(&cfg.GitMaxIntentBytes, "git-max-intent", cfg.GitMaxIntentBytes, "Maximum bytes per file intent summary")
	fs.StringVar(&cfg.Profile, "profile", cfg.Profile, "Tool profile for MCP SDK: core (7), extended (14), full (all 17), or minimal (3 + discover)")
	fs.StringVar(&cfg.OllamaEndpoint, "ollama-endpoint", cfg.OllamaEndpoint, "Ollama API endpoint (e.g., http://localhost:11434)")
	fs.StringVar(&cfg.OllamaModel, "ollama-model", cfg.OllamaModel, "Ollama embedding model name")
	fs.StringVar(&cfg.LlamaCppEndpoint, "llamacpp-endpoint", cfg.LlamaCppEndpoint, "llama.cpp server endpoint (e.g., http://localhost:8080)")
	fs.StringVar(&cfg.OpenAIEndpoint, "openai-endpoint", cfg.OpenAIEndpoint, "OpenAI-compatible API endpoint (e.g., http://localhost:1234)")
	fs.StringVar(&cfg.OpenAIModel, "openai-model", cfg.OpenAIModel, "Model name for OpenAI-compatible embeddings")
	fs.IntVar(&cfg.HTTPPort, "port", cfg.HTTPPort, "HTTP port for serve-http mode")
	fs.StringVar(&cfg.HTTPBearerToken, "bearer-token", cfg.HTTPBearerToken, "Bearer token for HTTP authentication")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, fmt.Errorf("parsing flags: %w", err)
	}

	// M4: Fall back to env var for bearer token
	if cfg.HTTPBearerToken == "" {
		cfg.HTTPBearerToken = os.Getenv("QB_CONTEXT_BEARER_TOKEN")
	}

	// H21: Prevent zero or negative batch-size causing infinite loop
	if cfg.EmbeddingBatchSize < 1 {
		cfg.EmbeddingBatchSize = 32
	}

	// M61: Reject negative values for numeric config fields
	if cfg.MaxBFSDepth < 0 {
		cfg.MaxBFSDepth = 0
	}
	if cfg.WorkerCount < 1 {
		cfg.WorkerCount = 1
	}
	if cfg.EmbeddingDim < 1 {
		return nil, fmt.Errorf("embedding-dim must be positive, got %d", cfg.EmbeddingDim)
	}

	if cfg.GitHistoryDepth < 1 {
		cfg.GitHistoryDepth = 500
	}
	if cfg.GitPerFileCommitCap < 1 {
		cfg.GitPerFileCommitCap = 20
	}
	if cfg.GitMaxMessageBytes < 100 {
		cfg.GitMaxMessageBytes = 2000
	}
	if cfg.GitMaxIntentBytes < 100 {
		cfg.GitMaxIntentBytes = 1500
	}

	// Validate port range
	if cfg.HTTPPort < 1 || cfg.HTTPPort > 65535 {
		return nil, fmt.Errorf("port must be between 1 and 65535, got %d", cfg.HTTPPort)
	}

	// Validate profile
	switch cfg.Profile {
	case "core", "extended", "full", "minimal":
		// valid
	default:
		return nil, fmt.Errorf("invalid profile %q: must be core, extended, full, or minimal", cfg.Profile)
	}

	// Resolve absolute paths
	if !filepath.IsAbs(cfg.RepoRoot) {
		abs, err := filepath.Abs(cfg.RepoRoot)
		if err != nil {
			return nil, fmt.Errorf("resolving absolute path for repo root %q: %w", cfg.RepoRoot, err)
		}
		cfg.RepoRoot = abs
	}

	if !filepath.IsAbs(cfg.DBPath) {
		cfg.DBPath = filepath.Join(cfg.RepoRoot, cfg.DBPath)
	}

	// M20: Validate ONNX settings
	if cfg.ONNXModelDir != "" && cfg.ONNXLibPath == "" {
		log.Printf("Warning: --onnx-model specified without --onnx-lib; ONNX may fail to initialize")
	}

	return cfg, nil
}

// detectDefaultONNXModel checks for a bundled CodeRankEmbed model at known paths.
func detectDefaultONNXModel() string {
	candidates := []string{
		"models/CodeRankEmbed-onnx-int8",
	}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "tokenizer.json")); err == nil {
			return dir
		}
	}
	return ""
}
