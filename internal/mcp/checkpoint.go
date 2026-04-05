package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/maplenk/context-mcp/internal/storage"
	"github.com/maplenk/context-mcp/internal/types"
)

// maxCheckpoints is the maximum number of checkpoints retained in memory.
// Oldest checkpoints are evicted when this limit is exceeded.
const maxCheckpoints = 10

// CheckpointStore holds in-memory session checkpoints.
type CheckpointStore struct {
	mu          sync.RWMutex
	checkpoints map[string]*Checkpoint
}

// Checkpoint captures index state at a point in time.
type Checkpoint struct {
	Name       string    `json:"name"`
	Timestamp  time.Time `json:"timestamp"`
	HeadCommit string    `json:"head_commit"`
	NodeCount  int       `json:"node_count"`
	FileCount  int       `json:"file_count"`
	// unexported to prevent external mutation (M3)
	nodeHashes map[string]string              // nodeID -> SHA256 of source bytes
	nodeMeta   map[string]nodeCheckpointMeta  // nodeID -> metadata for delta
}

type nodeCheckpointMeta struct {
	FilePath   string
	SymbolName string
	NodeType   string
	Hash       string
}

// NewCheckpointStore creates a new in-memory checkpoint store.
func NewCheckpointStore() *CheckpointStore {
	return &CheckpointStore{
		checkpoints: make(map[string]*Checkpoint),
	}
}

// CreateCheckpoint snapshots the current index state under the given name.
// If name is empty, an auto-generated name is used.
func (cs *CheckpointStore) CreateCheckpoint(name, repoRoot string, store *storage.Store) (*Checkpoint, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fmt.Sprintf("cp-%d", time.Now().UnixNano()) // L1: nanosecond precision
	}
	if len(name) > 64 {
		return nil, fmt.Errorf("checkpoint name too long (max 64 chars)")
	}

	nodeIDs, err := store.GetAllNodeIDs()
	if err != nil {
		return nil, fmt.Errorf("failed to get node IDs: %w", err)
	}

	nodeHashes := make(map[string]string, len(nodeIDs))
	nodeMeta := make(map[string]nodeCheckpointMeta, len(nodeIDs))
	fileSet := make(map[string]struct{})

	for _, id := range nodeIDs {
		node, err := store.GetNode(id)
		if err != nil {
			continue // skip nodes that can't be fetched
		}

		hash := hashNodeSource(repoRoot, *node)
		nodeHashes[id] = hash
		nodeMeta[id] = nodeCheckpointMeta{
			FilePath:   node.FilePath,
			SymbolName: node.SymbolName,
			NodeType:   node.NodeType.String(),
			Hash:       hash,
		}
		fileSet[node.FilePath] = struct{}{}
	}

	// Get HEAD commit
	headCommit := ""
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "HEAD")
	if out, err := cmd.Output(); err == nil {
		headCommit = strings.TrimSpace(string(out))
	}

	cp := &Checkpoint{
		Name:       name,
		Timestamp:  time.Now(),
		HeadCommit: headCommit,
		NodeCount:  len(nodeIDs),
		FileCount:  len(fileSet),
		nodeHashes: nodeHashes,
		nodeMeta:   nodeMeta,
	}

	cs.mu.Lock()
	cs.checkpoints[name] = cp
	// C1: Evict oldest checkpoint if over limit
	if len(cs.checkpoints) > maxCheckpoints {
		var oldestName string
		var oldestTime time.Time
		for n, c := range cs.checkpoints {
			if oldestName == "" || c.Timestamp.Before(oldestTime) {
				oldestName = n
				oldestTime = c.Timestamp
			}
		}
		delete(cs.checkpoints, oldestName)
	}
	cs.mu.Unlock()

	return cp, nil
}

// GetCheckpoint retrieves a checkpoint by name.
func (cs *CheckpointStore) GetCheckpoint(name string) (*Checkpoint, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	cp, ok := cs.checkpoints[name]
	return cp, ok
}

// DeltaResult holds the diff between a checkpoint and the current index state.
type DeltaResult struct {
	CheckpointName string      `json:"checkpoint_name"`
	CheckpointTime string      `json:"checkpoint_time"`
	Summary        string      `json:"summary"`
	Added          []DeltaItem `json:"added"`
	Modified       []DeltaItem `json:"modified"`
	Deleted        []DeltaItem `json:"deleted"`
	TotalChanges   int         `json:"total_changes"`
}

// DeltaItem represents a single changed node in the delta.
type DeltaItem struct {
	NodeID     string `json:"node_id"`
	SymbolName string `json:"symbol_name"`
	FilePath   string `json:"file_path"`
	NodeType   string `json:"node_type"`
	ChangeType string `json:"change_type"` // "added", "modified", "deleted"
}

// ComputeDelta compares the current index state against a named checkpoint.
func (cs *CheckpointStore) ComputeDelta(checkpointName, repoRoot string, store *storage.Store, pathFilter string, limit int) (*DeltaResult, error) {
	cp, ok := cs.GetCheckpoint(checkpointName)
	if !ok {
		return nil, fmt.Errorf("checkpoint %q not found", checkpointName)
	}

	if limit <= 0 {
		limit = 20
	}

	currentIDs, err := store.GetAllNodeIDs()
	if err != nil {
		return nil, fmt.Errorf("failed to get current node IDs: %w", err)
	}

	currentSet := make(map[string]struct{}, len(currentIDs))
	var added, modified []DeltaItem

	for _, id := range currentIDs {
		currentSet[id] = struct{}{}

		node, err := store.GetNode(id)
		if err != nil {
			continue
		}

		// Apply path filter
		if pathFilter != "" && !strings.HasPrefix(node.FilePath, pathFilter) {
			continue
		}

		oldHash, existed := cp.nodeHashes[id]
		if !existed {
			added = append(added, DeltaItem{
				NodeID:     id,
				SymbolName: node.SymbolName,
				FilePath:   node.FilePath,
				NodeType:   node.NodeType.String(),
				ChangeType: "added",
			})
		} else {
			newHash := hashNodeSource(repoRoot, *node)
			// M1: Treat newHash=="" (file deleted/unreadable) as modified when oldHash was valid
			if newHash != oldHash {
				if oldHash == "" {
					continue // couldn't hash at checkpoint time either, skip
				}
				modified = append(modified, DeltaItem{
					NodeID:     id,
					SymbolName: node.SymbolName,
					FilePath:   node.FilePath,
					NodeType:   node.NodeType.String(),
					ChangeType: "modified",
				})
			}
		}
	}

	var deleted []DeltaItem
	for id, meta := range cp.nodeMeta {
		if _, exists := currentSet[id]; !exists {
			// Apply path filter
			if pathFilter != "" && !strings.HasPrefix(meta.FilePath, pathFilter) {
				continue
			}
			deleted = append(deleted, DeltaItem{
				NodeID:     id,
				SymbolName: meta.SymbolName,
				FilePath:   meta.FilePath,
				NodeType:   meta.NodeType,
				ChangeType: "deleted",
			})
		}
	}

	// H1: Capture true counts before truncation
	trueAdded := len(added)
	trueModified := len(modified)
	trueDeleted := len(deleted)
	totalChanges := trueAdded + trueModified + trueDeleted

	if len(added) > limit {
		added = added[:limit]
	}
	if len(modified) > limit {
		modified = modified[:limit]
	}
	if len(deleted) > limit {
		deleted = deleted[:limit]
	}

	summary := fmt.Sprintf("%d added, %d modified, %d deleted since checkpoint '%s'",
		trueAdded, trueModified, trueDeleted, checkpointName)
	if trueAdded > limit || trueModified > limit || trueDeleted > limit {
		summary += fmt.Sprintf(" (showing up to %d per type)", limit)
	}

	return &DeltaResult{
		CheckpointName: cp.Name,
		CheckpointTime: cp.Timestamp.Format(time.RFC3339),
		Summary:        summary,
		Added:          added,
		Modified:       modified,
		Deleted:        deleted,
		TotalChanges:   totalChanges,
	}, nil
}

// hashNodeSource computes a SHA-256 hash of the source bytes for a node.
// Returns "" on any error (file missing, out-of-range, >5MB files skipped).
func hashNodeSource(repoRoot string, node types.ASTNode) string {
	filePath := filepath.Join(repoRoot, node.FilePath)
	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() > 5*1024*1024 { // skip files >5MB to avoid excessive memory
		return ""
	}

	start := node.StartByte
	end := node.EndByte
	if end <= start || int64(end) > info.Size() {
		return ""
	}

	buf := make([]byte, end-start)
	if _, err := f.ReadAt(buf, int64(start)); err != nil {
		return ""
	}

	h := sha256.Sum256(buf)
	return hex.EncodeToString(h[:])
}
