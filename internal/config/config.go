package config

import (
	"flag"
	"path/filepath"
	"time"
)

// Config holds all configuration for the qb-context daemon
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
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		RepoRoot:           ".",
		DBPath:             ".qb-context/index.db",
		DebounceInterval:   500 * time.Millisecond,
		MaxBFSDepth:        5,
		EmbeddingBatchSize: 32,
		WorkerCount:        4,
		ExcludedDirs:       []string{".git", ".qb-context"},
		EmbeddingDim:       384,
	}
}

// ParseFlags populates the config from command-line flags
func ParseFlags() *Config {
	cfg := DefaultConfig()

	flag.StringVar(&cfg.RepoRoot, "repo", cfg.RepoRoot, "Path to the repository root")
	flag.StringVar(&cfg.DBPath, "db", cfg.DBPath, "Path to the SQLite database file")
	flag.DurationVar(&cfg.DebounceInterval, "debounce", cfg.DebounceInterval, "Filesystem event debounce interval")
	flag.IntVar(&cfg.MaxBFSDepth, "max-depth", cfg.MaxBFSDepth, "Maximum BFS traversal depth for impact analysis")
	flag.IntVar(&cfg.EmbeddingBatchSize, "batch-size", cfg.EmbeddingBatchSize, "Embedding batch size")
	flag.IntVar(&cfg.WorkerCount, "workers", cfg.WorkerCount, "Number of parallel parsing workers")
	flag.StringVar(&cfg.ONNXModelDir, "onnx-model", cfg.ONNXModelDir, "Path to ONNX model directory (enables neural embeddings)")
	flag.StringVar(&cfg.ONNXLibPath, "onnx-lib", cfg.ONNXLibPath, "Path to ONNX Runtime shared library")
	flag.IntVar(&cfg.EmbeddingDim, "embedding-dim", cfg.EmbeddingDim, "Embedding vector dimension (ONNX Matryoshka: 64/128/256/512/896)")
	flag.Parse()

	// Resolve absolute paths
	if !filepath.IsAbs(cfg.RepoRoot) {
		abs, err := filepath.Abs(cfg.RepoRoot)
		if err == nil {
			cfg.RepoRoot = abs
		}
	}

	if !filepath.IsAbs(cfg.DBPath) {
		cfg.DBPath = filepath.Join(cfg.RepoRoot, cfg.DBPath)
	}

	return cfg
}
