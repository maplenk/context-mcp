package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setArgs replaces os.Args for the duration of a test and restores them on
// cleanup. The first element is always set to "context-mcp" (the program name).
func setArgs(t *testing.T, args ...string) {
	t.Helper()
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = append([]string{"context-mcp"}, args...)
}

// ---- DefaultConfig tests ----

func TestDefaultConfig_Values(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.RepoRoot != "." {
		t.Errorf("RepoRoot: got %q, want %q", cfg.RepoRoot, ".")
	}
	if cfg.DBPath != ".context-mcp/index.db" {
		t.Errorf("DBPath: got %q, want %q", cfg.DBPath, ".context-mcp/index.db")
	}
	if cfg.DebounceInterval != 500*time.Millisecond {
		t.Errorf("DebounceInterval: got %v, want %v", cfg.DebounceInterval, 500*time.Millisecond)
	}
	if cfg.MaxBFSDepth != 5 {
		t.Errorf("MaxBFSDepth: got %d, want %d", cfg.MaxBFSDepth, 5)
	}
	if cfg.EmbeddingBatchSize != 32 {
		t.Errorf("EmbeddingBatchSize: got %d, want %d", cfg.EmbeddingBatchSize, 32)
	}
	if cfg.WorkerCount != 4 {
		t.Errorf("WorkerCount: got %d, want %d", cfg.WorkerCount, 4)
	}
	if cfg.EmbeddingDim != 384 {
		t.Errorf("EmbeddingDim: got %d, want %d", cfg.EmbeddingDim, 384)
	}
	if cfg.ONNXModelDir != "" {
		t.Errorf("ONNXModelDir: got %q, want empty", cfg.ONNXModelDir)
	}
	if cfg.ONNXLibPath != "" {
		t.Errorf("ONNXLibPath: got %q, want empty", cfg.ONNXLibPath)
	}

	wantExcluded := []string{".git", ".context-mcp"}
	if len(cfg.ExcludedDirs) != len(wantExcluded) {
		t.Fatalf("ExcludedDirs length: got %d, want %d", len(cfg.ExcludedDirs), len(wantExcluded))
	}
	for i, d := range wantExcluded {
		if cfg.ExcludedDirs[i] != d {
			t.Errorf("ExcludedDirs[%d]: got %q, want %q", i, cfg.ExcludedDirs[i], d)
		}
	}
}

func TestDefaultConfig_ReturnsNewInstance(t *testing.T) {
	c1 := DefaultConfig()
	c2 := DefaultConfig()
	if c1 == c2 {
		t.Error("DefaultConfig should return distinct instances")
	}
}

// ---- ParseFlags tests ----

func TestParseFlags_NoArgs_ReturnsDefaults(t *testing.T) {
	setArgs(t)

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}

	// RepoRoot is resolved to an absolute path, so just check it's absolute.
	if !filepath.IsAbs(cfg.RepoRoot) {
		t.Errorf("RepoRoot should be absolute, got %q", cfg.RepoRoot)
	}
	// DBPath should be joined with the absolute RepoRoot.
	if !filepath.IsAbs(cfg.DBPath) {
		t.Errorf("DBPath should be absolute, got %q", cfg.DBPath)
	}
	if cfg.DebounceInterval != 500*time.Millisecond {
		t.Errorf("DebounceInterval: got %v, want %v", cfg.DebounceInterval, 500*time.Millisecond)
	}
	if cfg.MaxBFSDepth != 5 {
		t.Errorf("MaxBFSDepth: got %d, want %d", cfg.MaxBFSDepth, 5)
	}
	if cfg.EmbeddingBatchSize != 32 {
		t.Errorf("EmbeddingBatchSize: got %d, want %d", cfg.EmbeddingBatchSize, 32)
	}
	if cfg.WorkerCount != 4 {
		t.Errorf("WorkerCount: got %d, want %d", cfg.WorkerCount, 4)
	}
	if cfg.EmbeddingDim != 384 {
		t.Errorf("EmbeddingDim: got %d, want %d", cfg.EmbeddingDim, 384)
	}
}

func TestParseFlags_OverrideRepo(t *testing.T) {
	setArgs(t, "-repo", "/tmp/myrepo")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.RepoRoot != "/tmp/myrepo" {
		t.Errorf("RepoRoot: got %q, want %q", cfg.RepoRoot, "/tmp/myrepo")
	}
}

func TestParseFlags_OverrideDB(t *testing.T) {
	setArgs(t, "-repo", "/tmp/myrepo", "-db", "/tmp/custom.db")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	// An absolute -db path should be kept as-is.
	if cfg.DBPath != "/tmp/custom.db" {
		t.Errorf("DBPath: got %q, want %q", cfg.DBPath, "/tmp/custom.db")
	}
}

func TestParseFlags_RelativeDBJoinedWithRepoRoot(t *testing.T) {
	setArgs(t, "-repo", "/tmp/myrepo", "-db", "data/index.db")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	want := filepath.Join("/tmp/myrepo", "data/index.db")
	if cfg.DBPath != want {
		t.Errorf("DBPath: got %q, want %q", cfg.DBPath, want)
	}
}

func TestParseFlags_OverrideDebounce(t *testing.T) {
	setArgs(t, "-debounce", "2s")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.DebounceInterval != 2*time.Second {
		t.Errorf("DebounceInterval: got %v, want %v", cfg.DebounceInterval, 2*time.Second)
	}
}

func TestParseFlags_OverrideMaxDepth(t *testing.T) {
	setArgs(t, "-max-depth", "10")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.MaxBFSDepth != 10 {
		t.Errorf("MaxBFSDepth: got %d, want %d", cfg.MaxBFSDepth, 10)
	}
}

func TestParseFlags_OverrideBatchSize(t *testing.T) {
	setArgs(t, "-batch-size", "64")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.EmbeddingBatchSize != 64 {
		t.Errorf("EmbeddingBatchSize: got %d, want %d", cfg.EmbeddingBatchSize, 64)
	}
}

func TestParseFlags_OverrideWorkers(t *testing.T) {
	setArgs(t, "-workers", "8")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.WorkerCount != 8 {
		t.Errorf("WorkerCount: got %d, want %d", cfg.WorkerCount, 8)
	}
}

func TestParseFlags_OverrideEmbeddingDim(t *testing.T) {
	setArgs(t, "-embedding-dim", "256")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.EmbeddingDim != 256 {
		t.Errorf("EmbeddingDim: got %d, want %d", cfg.EmbeddingDim, 256)
	}
}

func TestParseFlags_OverrideONNXFlags(t *testing.T) {
	setArgs(t, "-onnx-model", "/opt/model", "-onnx-lib", "/opt/libonnx.dylib")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.ONNXModelDir != "/opt/model" {
		t.Errorf("ONNXModelDir: got %q, want %q", cfg.ONNXModelDir, "/opt/model")
	}
	if cfg.ONNXLibPath != "/opt/libonnx.dylib" {
		t.Errorf("ONNXLibPath: got %q, want %q", cfg.ONNXLibPath, "/opt/libonnx.dylib")
	}
}

func TestParseFlags_MultipleOverrides(t *testing.T) {
	setArgs(t,
		"-repo", "/tmp/repo",
		"-db", "my.db",
		"-debounce", "1s",
		"-max-depth", "3",
		"-batch-size", "16",
		"-workers", "2",
		"-embedding-dim", "128",
	)

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.RepoRoot != "/tmp/repo" {
		t.Errorf("RepoRoot: got %q, want %q", cfg.RepoRoot, "/tmp/repo")
	}
	if cfg.DBPath != filepath.Join("/tmp/repo", "my.db") {
		t.Errorf("DBPath: got %q, want %q", cfg.DBPath, filepath.Join("/tmp/repo", "my.db"))
	}
	if cfg.DebounceInterval != 1*time.Second {
		t.Errorf("DebounceInterval: got %v, want %v", cfg.DebounceInterval, 1*time.Second)
	}
	if cfg.MaxBFSDepth != 3 {
		t.Errorf("MaxBFSDepth: got %d, want %d", cfg.MaxBFSDepth, 3)
	}
	if cfg.EmbeddingBatchSize != 16 {
		t.Errorf("EmbeddingBatchSize: got %d, want %d", cfg.EmbeddingBatchSize, 16)
	}
	if cfg.WorkerCount != 2 {
		t.Errorf("WorkerCount: got %d, want %d", cfg.WorkerCount, 2)
	}
	if cfg.EmbeddingDim != 128 {
		t.Errorf("EmbeddingDim: got %d, want %d", cfg.EmbeddingDim, 128)
	}
}

// ---- Validation / edge-case tests ----

func TestParseFlags_UnknownFlagReturnsError(t *testing.T) {
	setArgs(t, "-nonexistent-flag", "value")

	_, err := ParseFlags()
	if err == nil {
		t.Fatal("expected error for unknown flag, got nil")
	}
}

func TestParseFlags_ZeroBatchSizeClampedToDefault(t *testing.T) {
	setArgs(t, "-batch-size", "0")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.EmbeddingBatchSize != 32 {
		t.Errorf("EmbeddingBatchSize should be clamped to 32, got %d", cfg.EmbeddingBatchSize)
	}
}

func TestParseFlags_NegativeBatchSizeClampedToDefault(t *testing.T) {
	setArgs(t, "-batch-size", "-5")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.EmbeddingBatchSize != 32 {
		t.Errorf("EmbeddingBatchSize should be clamped to 32, got %d", cfg.EmbeddingBatchSize)
	}
}

func TestParseFlags_NegativeMaxDepthClampedToZero(t *testing.T) {
	setArgs(t, "-max-depth", "-3")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.MaxBFSDepth != 0 {
		t.Errorf("MaxBFSDepth should be clamped to 0, got %d", cfg.MaxBFSDepth)
	}
}

func TestParseFlags_ZeroWorkersClampedToOne(t *testing.T) {
	setArgs(t, "-workers", "0")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.WorkerCount != 1 {
		t.Errorf("WorkerCount should be clamped to 1, got %d", cfg.WorkerCount)
	}
}

func TestParseFlags_NegativeWorkersClampedToOne(t *testing.T) {
	setArgs(t, "-workers", "-2")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.WorkerCount != 1 {
		t.Errorf("WorkerCount should be clamped to 1, got %d", cfg.WorkerCount)
	}
}

func TestParseFlags_NegativeEmbeddingDimReturnsError(t *testing.T) {
	setArgs(t, "-embedding-dim", "-1")

	_, err := ParseFlags()
	if err == nil {
		t.Fatal("expected error for negative embedding-dim, got nil")
	}
}

func TestParseFlags_ZeroEmbeddingDimReturnsError(t *testing.T) {
	setArgs(t, "-embedding-dim", "0")

	_, err := ParseFlags()
	if err == nil {
		t.Fatal("expected error for zero embedding-dim, got nil")
	}
}

func TestParseFlags_RepoRootResolvedToAbsolute(t *testing.T) {
	setArgs(t, "-repo", "relative/path")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if !filepath.IsAbs(cfg.RepoRoot) {
		t.Errorf("RepoRoot should be absolute, got %q", cfg.RepoRoot)
	}
}

func TestParseFlags_AbsoluteRepoRootUnchanged(t *testing.T) {
	setArgs(t, "-repo", "/absolute/path")

	cfg, err := ParseFlags()
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}
	if cfg.RepoRoot != "/absolute/path" {
		t.Errorf("RepoRoot: got %q, want %q", cfg.RepoRoot, "/absolute/path")
	}
}

// TestParseFlags_TableDriven runs a table of flag combinations through ParseFlags.
func TestParseFlags_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		check   func(t *testing.T, cfg *Config)
		wantErr bool
	}{
		{
			name: "empty args",
			args: nil,
			check: func(t *testing.T, cfg *Config) {
				if cfg.MaxBFSDepth != 5 {
					t.Errorf("MaxBFSDepth: got %d, want 5", cfg.MaxBFSDepth)
				}
			},
		},
		{
			name: "max-depth zero is allowed",
			args: []string{"-max-depth", "0"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.MaxBFSDepth != 0 {
					t.Errorf("MaxBFSDepth: got %d, want 0", cfg.MaxBFSDepth)
				}
			},
		},
		{
			name: "batch-size 1 is valid",
			args: []string{"-batch-size", "1"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.EmbeddingBatchSize != 1 {
					t.Errorf("EmbeddingBatchSize: got %d, want 1", cfg.EmbeddingBatchSize)
				}
			},
		},
		{
			name: "workers 1 is valid",
			args: []string{"-workers", "1"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.WorkerCount != 1 {
					t.Errorf("WorkerCount: got %d, want 1", cfg.WorkerCount)
				}
			},
		},
		{
			name:    "invalid duration",
			args:    []string{"-debounce", "notaduration"},
			wantErr: true,
		},
		{
			name:    "non-integer max-depth",
			args:    []string{"-max-depth", "abc"},
			wantErr: true,
		},
		{
			name: "large embedding dim",
			args: []string{"-embedding-dim", "896"},
			check: func(t *testing.T, cfg *Config) {
				if cfg.EmbeddingDim != 896 {
					t.Errorf("EmbeddingDim: got %d, want 896", cfg.EmbeddingDim)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setArgs(t, tt.args...)

			cfg, err := ParseFlags()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}
