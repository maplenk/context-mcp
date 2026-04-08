package storage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/maplenk/context-mcp/internal/gitmeta"
	"github.com/maplenk/context-mcp/internal/tokenutil"
	"github.com/maplenk/context-mcp/internal/types"
	_ "github.com/mattn/go-sqlite3"
)

// DefaultEmbeddingDim is the default embedding dimension (TFIDF fallback).
const DefaultEmbeddingDim = 384

// sqliteVecOnce ensures sqlite_vec.Auto() is called exactly once.
var sqliteVecOnce sync.Once

// dangerousPatterns is the canonical blocklist of SQL patterns rejected by RawQuery.
// Each entry is matched with word-boundary regex (case-insensitive) to avoid false
// positives (e.g., "attachment" won't match "attach").
// "edit" is intentionally omitted — it appears in normal identifiers; the real
// protection against edit() is PRAGMA query_only / read-only transactions.
var dangerousPatterns = []string{
	"load_extension", "writefile", "readfile", "fts3_tokenizer",
	"attach", "pragma", "vacuum", "reindex",
}

// recursiveCTERe matches WITH RECURSIVE (case-insensitive, flexible whitespace)
// to block recursive CTE DoS vectors (exponential expansion).
var recursiveCTERe = regexp.MustCompile(`(?i)\bWITH\s+RECURSIVE\b`)

// Store manages all SQLite database operations
type Store struct {
	db           *sql.DB
	mu           sync.RWMutex
	hasVecTable  bool // true if sqlite-vec vec0 table was created successfully
	embeddingDim int  // configurable embedding dimension
}

func closeWithLog(label string, closer interface{ Close() error }) {
	if err := closer.Close(); err != nil {
		log.Printf("Warning: failed to close %s: %v", label, err)
	}
}

// NewStore opens (or creates) a SQLite database at the given path and runs migrations.
// embeddingDim sets the expected embedding vector dimension (0 uses DefaultEmbeddingDim).
func NewStore(dbPath string, embeddingDim ...int) (*Store, error) {
	// Register sqlite-vec extension once for all future SQLite connections.
	sqliteVecOnce.Do(func() {
		sqlite_vec.Auto()
	})

	// Ensure parent directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000&_trusted_schema=off")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Verify connection
	if err := db.Ping(); err != nil {
		closeWithLog("database after ping failure", db)
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	// Configure connection pool for SQLite WAL mode (concurrent readers + single writer)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)

	dim := DefaultEmbeddingDim
	if len(embeddingDim) > 0 && embeddingDim[0] > 0 {
		dim = embeddingDim[0]
	}

	s := &Store{db: db, embeddingDim: dim}

	if err := s.runMigrations(); err != nil {
		closeWithLog("database after migration failure", db)
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return s, nil
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying sql.DB for raw queries.
// WARNING: Bypasses all safety checks (read-only enforcement, blocklist).
// Use RawQuery for safe user-facing queries. Only use DB() for internal operations.
func (s *Store) DB() *sql.DB {
	return s.db
}

// splitFileBasename splits a filename (without extension) on '.', '_', '-' separators.
func splitFileBasename(name string) []string {
	return strings.FieldsFunc(name, func(r rune) bool {
		return r == '.' || r == '_' || r == '-'
	})
}

// BuildSearchTerms produces a space-separated string of tokens derived from
// the symbol name (CamelCase-split) and the file basename (split on . _ -).
// This string is stored in the FTS index so that inner tokens of compound identifiers
// like "PaymentMappingService" become individually searchable.
// The original unsplit symbol name and file basename are prepended (preserving case)
// to enable compound prefix matching, matching the C reference codebase's behavior.
func BuildSearchTerms(symbolName, filePath string) string {
	// Split symbolName via CamelCase
	parts := tokenutil.SplitCamelCase(symbolName)

	// Split file basename (without extension) on . _ -
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)
	fileParts := splitFileBasename(nameWithoutExt)

	// Collect tokens, deduplicate preserving order.
	// Track originals separately from lowercase parts so both the compound
	// identifier and its split parts appear in the output.
	seenOriginal := make(map[string]bool) // tracks originals (case-insensitive)
	seenLower := make(map[string]bool)    // tracks lowercase split parts
	var tokens []string

	// C-style: prepend original symbolName (unsplit) for compound prefix matching.
	// FTS5 tokenizer is case-insensitive, so "OrderController" enables prefix
	// queries like "ordercontroller*" to match.
	if symbolName != "" {
		lowerSym := strings.ToLower(symbolName)
		seenOriginal[lowerSym] = true
		tokens = append(tokens, symbolName)
	}

	// C-style: prepend original file basename (unsplit) if different
	if nameWithoutExt != "" {
		lowerBase := strings.ToLower(nameWithoutExt)
		if !seenOriginal[lowerBase] {
			seenOriginal[lowerBase] = true
			tokens = append(tokens, nameWithoutExt)
		}
	}

	// Add lowercase CamelCase-split parts from symbolName
	for _, p := range parts {
		lower := strings.ToLower(p)
		if lower != "" && !seenLower[lower] {
			seenLower[lower] = true
			tokens = append(tokens, lower)
		}
	}
	// Add lowercase split parts from file basename
	for _, p := range fileParts {
		lower := strings.ToLower(p)
		if lower != "" && !seenLower[lower] {
			seenLower[lower] = true
			tokens = append(tokens, lower)
		}
	}

	// Also split on URL-path characters for route-style symbols
	// "POST /v1/merchant/{storeID}/order" → ["post", "v1", "merchant", "storeid", "order"]
	if strings.Contains(symbolName, "/") {
		for _, segment := range strings.FieldsFunc(symbolName, func(r rune) bool {
			return r == '/' || r == '{' || r == '}' || r == ' '
		}) {
			lower := strings.ToLower(segment)
			if lower != "" && !seenLower[lower] {
				seenLower[lower] = true
				tokens = append(tokens, lower)
			}
		}
	}

	return strings.Join(tokens, " ")
}

// UpsertNode inserts or updates a node in the database.
// Uses ON CONFLICT to avoid DELETE+INSERT which would cascade-delete edges.
// Wrapped in a transaction to ensure node + FTS index stay in sync.
func (s *Store) UpsertNode(node types.ASTNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		INSERT INTO nodes (id, file_path, symbol_name, node_type, start_byte, end_byte, content_sum, search_terms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			file_path = excluded.file_path,
			symbol_name = excluded.symbol_name,
			node_type = excluded.node_type,
			start_byte = excluded.start_byte,
			end_byte = excluded.end_byte,
			content_sum = excluded.content_sum,
			search_terms = excluded.search_terms`,
		node.ID, node.FilePath, node.SymbolName, uint8(node.NodeType),
		node.StartByte, node.EndByte, node.ContentSum, BuildSearchTerms(node.SymbolName, node.FilePath),
	)
	if err != nil {
		return err
	}

	// Update FTS index (delete old entry then insert new)
	if _, err := tx.Exec("DELETE FROM nodes_fts WHERE node_id = ?", node.ID); err != nil {
		return fmt.Errorf("FTS delete: %w", err)
	}
	_, err = tx.Exec(
		"INSERT INTO nodes_fts (symbol_name, file_path, content_sum, search_terms, node_id) VALUES (?, ?, ?, ?, ?)",
		node.SymbolName, node.FilePath, node.ContentSum, BuildSearchTerms(node.SymbolName, node.FilePath), node.ID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// UpsertEdge inserts or ignores an edge in the database
func (s *Store) UpsertEdge(edge types.ASTEdge) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO edges (source_id, target_id, edge_type)
		VALUES (?, ?, ?)`,
		edge.SourceID, edge.TargetID, uint8(edge.EdgeType),
	)
	return err
}

// UpsertNodes batch-inserts nodes using a transaction
func (s *Store) UpsertNodes(nodes []types.ASTNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO nodes (id, file_path, symbol_name, node_type, start_byte, end_byte, content_sum, search_terms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			file_path = excluded.file_path,
			symbol_name = excluded.symbol_name,
			node_type = excluded.node_type,
			start_byte = excluded.start_byte,
			end_byte = excluded.end_byte,
			content_sum = excluded.content_sum,
			search_terms = excluded.search_terms`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	ftsStmt, err := tx.Prepare("INSERT INTO nodes_fts (symbol_name, file_path, content_sum, search_terms, node_id) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer ftsStmt.Close()

	for _, node := range nodes {
		_, err := stmt.Exec(node.ID, node.FilePath, node.SymbolName, uint8(node.NodeType),
			node.StartByte, node.EndByte, node.ContentSum, BuildSearchTerms(node.SymbolName, node.FilePath))
		if err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM nodes_fts WHERE node_id = ?", node.ID); err != nil {
			return fmt.Errorf("FTS delete for %s: %w", node.ID, err)
		}
		if _, err := ftsStmt.Exec(node.SymbolName, node.FilePath, node.ContentSum, BuildSearchTerms(node.SymbolName, node.FilePath), node.ID); err != nil {
			return fmt.Errorf("FTS insert for %s: %w", node.ID, err)
		}
	}

	return tx.Commit()
}

// UpsertEdges batch-inserts edges using a transaction
func (s *Store) UpsertEdges(edges []types.ASTEdge) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO edges (source_id, target_id, edge_type)
		VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, edge := range edges {
		_, err := stmt.Exec(edge.SourceID, edge.TargetID, uint8(edge.EdgeType))
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// DeleteByFile removes all nodes and their edges for a given file path
func (s *Store) DeleteByFile(filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete outgoing edges (where source belongs to this file)
	_, err = tx.Exec(`
		DELETE FROM edges WHERE source_id IN (SELECT id FROM nodes WHERE file_path = ?)`, filePath)
	if err != nil {
		return err
	}

	// Also delete incoming edges (where target belongs to this file)
	_, err = tx.Exec(`
		DELETE FROM edges WHERE target_id IN (SELECT id FROM nodes WHERE file_path = ?)`, filePath)
	if err != nil {
		return err
	}

	// Delete embeddings (ignore error if vec0 table doesn't exist)
	_, _ = tx.Exec(`
		DELETE FROM node_embeddings WHERE node_id IN (SELECT id FROM nodes WHERE file_path = ?)`, filePath)

	// Delete node_scores
	_, err = tx.Exec(`DELETE FROM node_scores WHERE node_id IN (SELECT id FROM nodes WHERE file_path = ?)`, filePath)
	if err != nil {
		return err
	}

	// Delete FTS entries for nodes from this file
	_, err = tx.Exec("DELETE FROM nodes_fts WHERE node_id IN (SELECT id FROM nodes WHERE file_path = ?)", filePath)
	if err != nil {
		return err
	}

	// Delete nodes
	_, err = tx.Exec(`DELETE FROM nodes WHERE file_path = ?`, filePath)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// GetIncomingCrossFileEdges returns edges where the target node belongs to filePath
// but the source node does NOT belong to filePath (cross-file incoming references).
func (s *Store) GetIncomingCrossFileEdges(filePath string) ([]types.ASTEdge, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT e.source_id, e.target_id, e.edge_type
		FROM edges e
		JOIN nodes tn ON e.target_id = tn.id
		WHERE tn.file_path = ?
		AND e.source_id NOT IN (SELECT id FROM nodes WHERE file_path = ?)`, filePath, filePath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []types.ASTEdge
	for rows.Next() {
		var edge types.ASTEdge
		var et uint8
		if err := rows.Scan(&edge.SourceID, &edge.TargetID, &et); err != nil {
			return nil, err
		}
		edge.EdgeType = types.EdgeType(et)
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

// UpsertEmbedding stores a vector embedding for a node.
// The embedding dimension must match the configured store dimension.
// Returns nil (no-op) if sqlite-vec is not available, enabling graceful degradation.
// Wrapped in a transaction to ensure delete + insert are atomic.
func (s *Store) UpsertEmbedding(nodeID string, embedding []float32) error {
	if !s.hasVecTable {
		return nil // graceful degradation: sqlite-vec not available
	}
	if len(embedding) != s.embeddingDim {
		return fmt.Errorf("embedding dimension mismatch: got %d, want %d", len(embedding), s.embeddingDim)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete then insert for vec0 virtual table compatibility
	if _, err := tx.Exec(`DELETE FROM node_embeddings WHERE node_id = ?`, nodeID); err != nil {
		return fmt.Errorf("delete old embedding: %w", err)
	}
	blob := serializeFloat32(embedding)
	if _, err := tx.Exec(`INSERT INTO node_embeddings (node_id, embedding) VALUES (?, ?)`, nodeID, blob); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateFTS updates the FTS index for a node.
// Wrapped in a transaction so DELETE+INSERT are atomic — if INSERT fails, the old entry is preserved.
func (s *Store) UpdateFTS(node types.ASTNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning FTS transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM nodes_fts WHERE node_id = ?", node.ID); err != nil {
		return fmt.Errorf("FTS delete: %w", err)
	}
	if _, err := tx.Exec("INSERT INTO nodes_fts (symbol_name, file_path, content_sum, search_terms, node_id) VALUES (?, ?, ?, ?, ?)",
		node.SymbolName, node.FilePath, node.ContentSum, BuildSearchTerms(node.SymbolName, node.FilePath), node.ID); err != nil {
		return fmt.Errorf("FTS insert: %w", err)
	}
	return tx.Commit()
}

// DeleteFTSByFile removes FTS entries for all nodes in a file
func (s *Store) DeleteFTSByFile(filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		"DELETE FROM nodes_fts WHERE node_id IN (SELECT id FROM nodes WHERE file_path = ?)",
		filePath)
	return err
}

// GetNode retrieves a single node by ID
func (s *Store) GetNode(nodeID string) (*types.ASTNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(`
		SELECT id, file_path, symbol_name, node_type, start_byte, end_byte, content_sum
		FROM nodes WHERE id = ?`, nodeID)

	var node types.ASTNode
	var nt uint8
	err := row.Scan(&node.ID, &node.FilePath, &node.SymbolName, &nt,
		&node.StartByte, &node.EndByte, &node.ContentSum)
	if err != nil {
		return nil, err
	}
	node.NodeType = types.NodeType(nt)
	return &node, nil
}

// GetNodeByName retrieves a node by symbol name (exact match)
func (s *Store) GetNodeByName(symbolName string) (*types.ASTNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(`
		SELECT id, file_path, symbol_name, node_type, start_byte, end_byte, content_sum
		FROM nodes WHERE symbol_name = ? ORDER BY file_path, id LIMIT 1`, symbolName)

	var node types.ASTNode
	var nt uint8
	err := row.Scan(&node.ID, &node.FilePath, &node.SymbolName, &nt,
		&node.StartByte, &node.EndByte, &node.ContentSum)
	if err != nil {
		return nil, err
	}
	node.NodeType = types.NodeType(nt)
	return &node, nil
}

// GetEdgesFrom retrieves all outgoing edges from a node
func (s *Store) GetEdgesFrom(nodeID string) ([]types.ASTEdge, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT source_id, target_id, edge_type FROM edges WHERE source_id = ?`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []types.ASTEdge
	for rows.Next() {
		var edge types.ASTEdge
		var et uint8
		if err := rows.Scan(&edge.SourceID, &edge.TargetID, &et); err != nil {
			return nil, err
		}
		edge.EdgeType = types.EdgeType(et)
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

// GetEdgesTo retrieves all incoming edges to a node
func (s *Store) GetEdgesTo(nodeID string) ([]types.ASTEdge, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT source_id, target_id, edge_type FROM edges WHERE target_id = ?`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []types.ASTEdge
	for rows.Next() {
		var edge types.ASTEdge
		var et uint8
		if err := rows.Scan(&edge.SourceID, &edge.TargetID, &et); err != nil {
			return nil, err
		}
		edge.EdgeType = types.EdgeType(et)
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

// GetAllEdges retrieves all edges from the database
func (s *Store) GetAllEdges() ([]types.ASTEdge, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`SELECT source_id, target_id, edge_type FROM edges`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []types.ASTEdge
	for rows.Next() {
		var edge types.ASTEdge
		var et uint8
		if err := rows.Scan(&edge.SourceID, &edge.TargetID, &et); err != nil {
			return nil, err
		}
		edge.EdgeType = types.EdgeType(et)
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

// GetAllNodeIDs retrieves all node IDs from the database
func (s *Store) GetAllNodeIDs() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`SELECT id FROM nodes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetNodeIDsByFile retrieves all node IDs for a given file path.
// Used for incremental graph updates: remove old nodes before adding new ones.
func (s *Store) GetNodeIDsByFile(filePath string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`SELECT id FROM nodes WHERE file_path = ?`, filePath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SearchLexical performs FTS5 BM25 lexical search
func (s *Store) SearchLexical(query string, limit int) ([]types.SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Sanitize FTS5 special characters to prevent injection
	query = sanitizeFTSStorage(query)

	rows, err := s.db.Query(`
		SELECT n.id, n.file_path, n.symbol_name, n.node_type, n.start_byte, n.end_byte, n.content_sum,
		       bm25(nodes_fts, 10.0, 1.0, 1.0, 5.0, 0.0) as score
		FROM nodes_fts fts
		JOIN nodes n ON n.id = fts.node_id
		WHERE nodes_fts MATCH ?
		ORDER BY score
		LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []types.SearchResult
	for rows.Next() {
		var r types.SearchResult
		var nt uint8
		if err := rows.Scan(&r.Node.ID, &r.Node.FilePath, &r.Node.SymbolName, &nt,
			&r.Node.StartByte, &r.Node.EndByte, &r.Node.ContentSum, &r.Score); err != nil {
			return nil, err
		}
		r.Node.NodeType = types.NodeType(nt)
		// BM25 returns negative scores (lower is better), negate for consistency
		r.Score = -r.Score
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchLexicalRaw performs FTS5 BM25 lexical search without sanitizing the query.
// Use this when the caller has already constructed a valid FTS5 query (e.g., with
// boolean operators like OR and prefix wildcards like *). For untrusted user input,
// use SearchLexical instead which sanitizes automatically.
func (s *Store) SearchLexicalRaw(query string, limit int) ([]types.SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT n.id, n.file_path, n.symbol_name, n.node_type, n.start_byte, n.end_byte, n.content_sum,
		       bm25(nodes_fts, 10.0, 1.0, 1.0, 5.0, 0.0) as score
		FROM nodes_fts fts
		JOIN nodes n ON n.id = fts.node_id
		WHERE nodes_fts MATCH ?
		ORDER BY score
		LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []types.SearchResult
	for rows.Next() {
		var r types.SearchResult
		var nt uint8
		if err := rows.Scan(&r.Node.ID, &r.Node.FilePath, &r.Node.SymbolName, &nt,
			&r.Node.StartByte, &r.Node.EndByte, &r.Node.ContentSum, &r.Score); err != nil {
			return nil, err
		}
		r.Node.NodeType = types.NodeType(nt)
		// BM25 returns negative scores (lower is better), negate for consistency
		r.Score = -r.Score
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchLexicalByType performs FTS5 BM25 search filtered to a specific node type.
// Used for route-specific candidate injection when query intent indicates routes.
func (s *Store) SearchLexicalByType(query string, nodeType types.NodeType, limit int) ([]types.SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT n.id, n.file_path, n.symbol_name, n.node_type, n.start_byte, n.end_byte, n.content_sum,
		       bm25(nodes_fts, 10.0, 1.0, 1.0, 5.0, 0.0) as score
		FROM nodes_fts fts
		JOIN nodes n ON n.id = fts.node_id
		WHERE nodes_fts MATCH ? AND n.node_type = ?
		ORDER BY score
		LIMIT ?`, query, uint8(nodeType), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []types.SearchResult
	for rows.Next() {
		var r types.SearchResult
		var nt uint8
		if err := rows.Scan(&r.Node.ID, &r.Node.FilePath, &r.Node.SymbolName, &nt,
			&r.Node.StartByte, &r.Node.EndByte, &r.Node.ContentSum, &r.Score); err != nil {
			return nil, err
		}
		r.Node.NodeType = types.NodeType(nt)
		r.Score = -r.Score
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchLexicalFilteredRaw performs FTS5 BM25 search filtered to specific node types.
// Like SearchLexicalRaw but adds WHERE n.node_type IN (...) filter.
// Used to exclude route nodes from general FTS (routes are found via edge-based lookup).
func (s *Store) SearchLexicalFilteredRaw(query string, nodeTypes []types.NodeType, limit int) ([]types.SearchResult, error) {
	if len(nodeTypes) == 0 {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Build IN clause
	placeholders := make([]string, len(nodeTypes))
	args := make([]interface{}, 0, len(nodeTypes)+2)
	args = append(args, query)
	for i, nt := range nodeTypes {
		placeholders[i] = "?"
		args = append(args, uint8(nt))
	}
	args = append(args, limit)

	// #nosec G202 -- placeholders are generated internally from nodeTypes and values stay parameterized in args.
	sqlQuery := `
		SELECT n.id, n.file_path, n.symbol_name, n.node_type, n.start_byte, n.end_byte, n.content_sum,
		       bm25(nodes_fts, 10.0, 1.0, 1.0, 5.0, 0.0) as score
		FROM nodes_fts fts
		JOIN nodes n ON n.id = fts.node_id
		WHERE nodes_fts MATCH ?
		AND n.node_type IN (` + strings.Join(placeholders, ", ") + `)
		ORDER BY score
		LIMIT ?`

	rows, err := s.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []types.SearchResult
	for rows.Next() {
		var r types.SearchResult
		var nt uint8
		if err := rows.Scan(&r.Node.ID, &r.Node.FilePath, &r.Node.SymbolName, &nt,
			&r.Node.StartByte, &r.Node.EndByte, &r.Node.ContentSum, &r.Score); err != nil {
			return nil, err
		}
		r.Node.NodeType = types.NodeType(nt)
		r.Score = -r.Score
		results = append(results, r)
	}
	return results, rows.Err()
}

// FindRouteNodesByKeywords searches for route-type nodes whose search_terms
// contain any of the given keywords. Returns matching route nodes with relevance scoring.
func (s *Store) FindRouteNodesByKeywords(keywords []string, limit int) ([]types.SearchResult, error) {
	if len(keywords) == 0 {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Build OR'd LIKE conditions for each keyword
	var conditions []string
	var args []interface{}
	for _, kw := range keywords {
		escaped := escapeLIKE(strings.ToLower(kw))
		conditions = append(conditions, "LOWER(n.search_terms) LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escaped+"%")
	}
	args = append(args, uint8(types.NodeTypeRoute))
	args = append(args, limit)

	// #nosec G202 -- conditions are fixed internal LIKE clauses; user keywords remain bound parameters in args.
	sqlQuery := `
		SELECT n.id, n.file_path, n.symbol_name, n.node_type, n.start_byte, n.end_byte, n.content_sum, n.search_terms
		FROM nodes n
		WHERE (` + strings.Join(conditions, " OR ") + `)
		AND n.node_type = ?
		LIMIT ?`

	rows, err := s.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []types.SearchResult
	for rows.Next() {
		var r types.SearchResult
		var nt uint8
		var searchTerms string
		if err := rows.Scan(&r.Node.ID, &r.Node.FilePath, &r.Node.SymbolName, &nt,
			&r.Node.StartByte, &r.Node.EndByte, &r.Node.ContentSum, &searchTerms); err != nil {
			return nil, err
		}
		r.Node.NodeType = types.NodeType(nt)

		// Score based on keyword match ratio
		matchCount := 0
		lowerTerms := strings.ToLower(searchTerms)
		for _, kw := range keywords {
			if strings.Contains(lowerTerms, strings.ToLower(kw)) {
				matchCount++
			}
		}
		r.Score = float64(matchCount) / float64(len(keywords))
		results = append(results, r)
	}
	return results, rows.Err()
}

// HasVecTable returns whether the sqlite-vec virtual table is available
func (s *Store) HasVecTable() bool {
	return s.hasVecTable
}

// SearchSemantic performs KNN semantic search using sqlite-vec.
// Returns nil, nil (empty results, no error) if sqlite-vec is not available,
// enabling graceful degradation to lexical-only search.
func (s *Store) SearchSemantic(queryEmbedding []float32, limit int) ([]types.SearchResult, error) {
	if !s.hasVecTable {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	blob := serializeFloat32(queryEmbedding)
	rows, err := s.db.Query(`
		SELECT n.id, n.file_path, n.symbol_name, n.node_type, n.start_byte, n.end_byte, n.content_sum,
		       e.distance
		FROM node_embeddings e
		JOIN nodes n ON n.id = e.node_id
		WHERE e.embedding MATCH ?
		AND k = ?
		ORDER BY e.distance`, blob, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []types.SearchResult
	for rows.Next() {
		var r types.SearchResult
		var nt uint8
		var distance float64
		if err := rows.Scan(&r.Node.ID, &r.Node.FilePath, &r.Node.SymbolName, &nt,
			&r.Node.StartByte, &r.Node.EndByte, &r.Node.ContentSum, &distance); err != nil {
			return nil, err
		}
		r.Node.NodeType = types.NodeType(nt)
		// Convert cosine distance to similarity score (1 - distance)
		r.Score = 1.0 - distance
		results = append(results, r)
	}
	return results, rows.Err()
}

// RawQuery executes a read-only SQL query and returns results as maps.
// Only SELECT statements are allowed to prevent SQL injection.
func (s *Store) RawQuery(query string) ([]map[string]interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Enforce read-only: only allow SELECT statements
	trimmed := strings.TrimSpace(strings.ToUpper(query))
	if !strings.HasPrefix(trimmed, "SELECT") && !strings.HasPrefix(trimmed, "WITH") {
		return nil, fmt.Errorf("only SELECT/WITH queries are allowed, got: %s", strings.SplitN(trimmed, " ", 2)[0])
	}

	// Defense-in-depth for WITH (CTE) queries: reject mutation keywords that
	// could appear after the CTE block (e.g., "WITH x AS (...) DELETE FROM ...").
	// PRAGMA query_only is the primary guard; this is an additional layer.
	if strings.HasPrefix(trimmed, "WITH") {
		mutationKeywords := []string{"INSERT", "UPDATE", "DELETE", "DROP", "ALTER", "CREATE"}
		for _, kw := range mutationKeywords {
			re := regexp.MustCompile(`(?i)\)\s*` + kw + `\b`)
			if re.MatchString(query) {
				return nil, fmt.Errorf("CTE query contains forbidden mutation keyword: %s", kw)
			}
		}
	}

	// Reject multi-statement queries (semicolons outside string literals).
	// Note: this may reject queries with semicolons in string literals.
	// Use parameterized values to avoid this limitation.
	if strings.Contains(query, ";") {
		return nil, fmt.Errorf("multi-statement queries are not allowed (semicolons forbidden)")
	}

	// Reject recursive CTEs to prevent CPU exhaustion via exponential expansion.
	if recursiveCTERe.MatchString(query) {
		return nil, fmt.Errorf("query contains forbidden pattern: WITH RECURSIVE")
	}

	// Reject CTE-based mutation bypass: WITH ... DELETE/INSERT/UPDATE/DROP
	if strings.HasPrefix(trimmed, "WITH") {
		for _, kw := range []string{"DELETE", "INSERT", "UPDATE", "DROP", "ALTER", "CREATE"} {
			re := regexp.MustCompile(`(?i)\b` + kw + `\b`)
			if re.MatchString(query) {
				return nil, fmt.Errorf("query contains forbidden mutation keyword in CTE: %s", kw)
			}
		}
	}

	// Reject queries containing dangerous SQLite functions/patterns.
	// Uses word-boundary regex to avoid false positives (e.g., "attachment", "credited").
	// See package-level dangerousPatterns for the canonical blocklist.
	for _, pattern := range dangerousPatterns {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(pattern) + `\b`)
		if re.MatchString(query) {
			return nil, fmt.Errorf("query contains forbidden pattern: %s", pattern)
		}
	}

	// Clamp LIMIT to 500 to prevent DoS via unbounded result sets.
	// Uses ReplaceAllStringFunc with per-match logic so that:
	//   1. Only LIMIT values exceeding 500 are clamped (small LIMITs preserved).
	//   2. LIMIT keywords inside string literals are handled gracefully because
	//      we only clamp the *last* match (the outermost/actual query LIMIT).
	limitRe := regexp.MustCompile(`(?i)\bLIMIT\s+(\d+)\b`)
	allMatches := limitRe.FindAllStringIndex(query, -1)
	if len(allMatches) > 0 {
		// Only process the last LIMIT match (the actual query LIMIT, not decoys in string literals).
		lastMatch := allMatches[len(allMatches)-1]
		matchedText := query[lastMatch[0]:lastMatch[1]]
		sub := limitRe.FindStringSubmatch(matchedText)
		if len(sub) > 1 {
			userLimit, _ := strconv.Atoi(sub[1])
			if userLimit > 500 {
				query = query[:lastMatch[0]] + "LIMIT 500" + query[lastMatch[1]:]
			}
		}
	} else {
		query = query + " LIMIT 500"
	}

	// Pin a connection from the pool and enforce read-only at the SQLite level.
	// mattn/go-sqlite3 ignores TxOptions{ReadOnly: true}, so we must use PRAGMA.
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		return nil, fmt.Errorf("acquiring connection: %w", err)
	}
	defer closeWithLog("raw query connection", conn)

	// Enable query_only mode to prevent any writes
	if _, err := conn.ExecContext(context.Background(), "PRAGMA query_only = ON"); err != nil {
		return nil, fmt.Errorf("enabling query_only: %w", err)
	}
	// Always restore the connection to read-write before returning to pool
	defer func() {
		if _, err := conn.ExecContext(context.Background(), "PRAGMA query_only = OFF"); err != nil {
			log.Printf("Warning: failed to reset query_only on connection: %v", err)
			closeWithLog("read-only raw query connection", conn) // prevent returning a read-only connection to the pool
		}
	}()

	// Enforce a 30-second timeout to prevent long-running queries from
	// holding the RLock indefinitely and blocking all write operations.
	queryCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := conn.QueryContext(queryCtx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}

		row := make(map[string]interface{})
		for i, col := range columns {
			row[col] = values[i]
		}
		results = append(results, row)
	}
	return results, rows.Err()
}

// UpsertNodeScores batch-inserts node scores using a transaction
func (s *Store) UpsertNodeScores(scores []types.NodeScore) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO node_scores (node_id, pagerank, betweenness)
		VALUES (?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			pagerank = excluded.pagerank,
			betweenness = excluded.betweenness`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, score := range scores {
		if _, err := stmt.Exec(score.NodeID, score.PageRank, score.Betweenness); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetNodeScore retrieves the scores for a single node
func (s *Store) GetNodeScore(nodeID string) (*types.NodeScore, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(`SELECT node_id, pagerank, betweenness FROM node_scores WHERE node_id = ?`, nodeID)
	var score types.NodeScore
	err := row.Scan(&score.NodeID, &score.PageRank, &score.Betweenness)
	if err != nil {
		return nil, err
	}
	return &score, nil
}

// GetAllBetweenness retrieves all betweenness centrality scores as a map
func (s *Store) GetAllBetweenness() (map[string]float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`SELECT node_id, betweenness FROM node_scores WHERE betweenness > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]float64)
	for rows.Next() {
		var nodeID string
		var betweenness float64
		if err := rows.Scan(&nodeID, &betweenness); err != nil {
			return nil, err
		}
		result[nodeID] = betweenness
	}
	return result, rows.Err()
}

// UpsertProjectSummary inserts or updates a project summary
func (s *Store) UpsertProjectSummary(summary types.ProjectSummary) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO project_summaries (project, summary, source_hash)
		VALUES (?, ?, ?)
		ON CONFLICT(project) DO UPDATE SET
			summary = excluded.summary,
			source_hash = excluded.source_hash,
			updated_at = datetime('now')`,
		summary.Project, summary.Summary, summary.SourceHash)
	return err
}

// GetProjectSummary retrieves a project summary by project name
func (s *Store) GetProjectSummary(project string) (*types.ProjectSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(`SELECT project, summary, source_hash FROM project_summaries WHERE project = ?`, project)
	var ps types.ProjectSummary
	err := row.Scan(&ps.Project, &ps.Summary, &ps.SourceHash)
	if err != nil {
		return nil, err
	}
	return &ps, nil
}

// GetAllProjectSummaries retrieves all project summaries
func (s *Store) GetAllProjectSummaries() ([]types.ProjectSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`SELECT project, summary, source_hash FROM project_summaries`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []types.ProjectSummary
	for rows.Next() {
		var ps types.ProjectSummary
		if err := rows.Scan(&ps.Project, &ps.Summary, &ps.SourceHash); err != nil {
			return nil, err
		}
		summaries = append(summaries, ps)
	}
	return summaries, rows.Err()
}

// GetAllFilePaths returns all unique file paths from the nodes table
func (s *Store) GetAllFilePaths() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`SELECT DISTINCT file_path FROM nodes ORDER BY file_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// escapeLIKE escapes the special LIKE pattern characters %, _, and \ so that
// they are matched literally when used with ESCAPE '\'.
func escapeLIKE(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// SearchNodesByName searches for nodes whose symbol_name contains the given pattern (case-insensitive)
func (s *Store) SearchNodesByName(pattern string) ([]types.ASTNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, file_path, symbol_name, node_type, start_byte, end_byte, content_sum
		FROM nodes WHERE symbol_name LIKE ? ESCAPE '\' COLLATE NOCASE
		LIMIT 100`, "%"+escapeLIKE(pattern)+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []types.ASTNode
	for rows.Next() {
		var node types.ASTNode
		var nt uint8
		if err := rows.Scan(&node.ID, &node.FilePath, &node.SymbolName, &nt,
			&node.StartByte, &node.EndByte, &node.ContentSum); err != nil {
			return nil, err
		}
		node.NodeType = types.NodeType(nt)
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

// GetSymbolIndex returns a map of symbol_name -> node_id for symbols used in
// cross-file edge resolution during incremental re-index.
// It includes type nodes plus functions/methods, and also adds a best-effort
// bare-method alias (e.g. "Controller.handle" -> "handle") when the alias is
// not already claimed by another symbol.
func (s *Store) GetSymbolIndex() (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT symbol_name, id FROM nodes
		WHERE node_type IN (?, ?, ?, ?, ?)`,
		uint8(types.NodeTypeClass), uint8(types.NodeTypeStruct), uint8(types.NodeTypeInterface),
		uint8(types.NodeTypeFunction), uint8(types.NodeTypeMethod))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	index := make(map[string]string)
	for rows.Next() {
		var sym, id string
		if err := rows.Scan(&sym, &id); err != nil {
			return nil, err
		}
		// first-wins: same behavior as indexRepo's symbolIndex
		if _, exists := index[sym]; !exists {
			index[sym] = id
		}
		if dot := strings.LastIndex(sym, "."); dot >= 0 && dot < len(sym)-1 {
			bare := sym[dot+1:]
			if _, exists := index[bare]; !exists {
				index[bare] = id
			}
		}
	}
	return index, rows.Err()
}

// GetNodesByFile returns all nodes for a given file path
func (s *Store) GetNodesByFile(filePath string) ([]types.ASTNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, file_path, symbol_name, node_type, start_byte, end_byte, content_sum
		FROM nodes WHERE file_path = ?`, filePath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []types.ASTNode
	for rows.Next() {
		var node types.ASTNode
		var nt uint8
		if err := rows.Scan(&node.ID, &node.FilePath, &node.SymbolName, &nt,
			&node.StartByte, &node.EndByte, &node.ContentSum); err != nil {
			return nil, err
		}
		node.NodeType = types.NodeType(nt)
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

// GetAllNodeScores returns all node scores as a slice
func (s *Store) GetAllNodeScores() ([]types.NodeScore, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`SELECT node_id, pagerank, betweenness FROM node_scores`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var scores []types.NodeScore
	for rows.Next() {
		var score types.NodeScore
		if err := rows.Scan(&score.NodeID, &score.PageRank, &score.Betweenness); err != nil {
			return nil, err
		}
		scores = append(scores, score)
	}
	return scores, rows.Err()
}

// sanitizeFTSStorage strips FTS5 special characters for safe direct queries
func sanitizeFTSStorage(s string) string {
	replacer := strings.NewReplacer(
		`"`, " ", `(`, " ", `)`, " ", `{`, " ", `}`, " ",
		`^`, " ", `+`, " ", `-`, " ", `*`, " ", `:`, " ",
	)
	s = replacer.Replace(s)
	// Neutralize FTS5 boolean operators by lowercasing them
	words := strings.Fields(s)
	for i, w := range words {
		upper := strings.ToUpper(w)
		if upper == "OR" || upper == "AND" || upper == "NOT" || upper == "NEAR" {
			words[i] = strings.ToLower(w)
		}
	}
	return strings.Join(words, " ")
}

// serializeFloat32 converts a float32 slice to a little-endian byte slice for sqlite-vec
func serializeFloat32(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// boolToInt converts a bool to an int (0 or 1) for SQLite storage.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- Git metadata CRUD methods ----

// UpsertRepoSnapshot stores or updates the repository git snapshot.
func (s *Store) UpsertRepoSnapshot(snap gitmeta.RepoSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`INSERT INTO repo_git_snapshot
		(repo_root, head_ref, head_commit, is_detached, is_dirty, ahead_count, behind_count,
		 staged_files, modified_files, untracked_files, snapshot_summary, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_root) DO UPDATE SET
			head_ref=excluded.head_ref, head_commit=excluded.head_commit,
			is_detached=excluded.is_detached, is_dirty=excluded.is_dirty,
			ahead_count=excluded.ahead_count, behind_count=excluded.behind_count,
			staged_files=excluded.staged_files, modified_files=excluded.modified_files,
			untracked_files=excluded.untracked_files, snapshot_summary=excluded.snapshot_summary,
			updated_at=excluded.updated_at`,
		snap.RepoRoot, snap.HeadRef, snap.HeadCommit,
		boolToInt(snap.IsDetached), boolToInt(snap.IsDirty),
		snap.AheadCount, snap.BehindCount,
		snap.StagedFiles, snap.ModifiedFiles, snap.UntrackedFiles,
		snap.Summary, snap.UpdatedAt.Format(time.RFC3339))
	return err
}

// GetRepoSnapshot returns the stored git snapshot for the given repo root.
func (s *Store) GetRepoSnapshot(repoRoot string) (*gitmeta.RepoSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var snap gitmeta.RepoSnapshot
	var isDetached, isDirty int
	var updatedAt string
	err := s.db.QueryRow(`SELECT repo_root, head_ref, head_commit, is_detached, is_dirty,
		ahead_count, behind_count, staged_files, modified_files, untracked_files,
		snapshot_summary, updated_at FROM repo_git_snapshot WHERE repo_root = ?`, repoRoot).
		Scan(&snap.RepoRoot, &snap.HeadRef, &snap.HeadCommit, &isDetached, &isDirty,
			&snap.AheadCount, &snap.BehindCount, &snap.StagedFiles, &snap.ModifiedFiles,
			&snap.UntrackedFiles, &snap.Summary, &updatedAt)
	if err != nil {
		return nil, err
	}
	snap.IsDetached = isDetached != 0
	snap.IsDirty = isDirty != 0
	parsedTime, parseErr := time.Parse(time.RFC3339, updatedAt)
	if parseErr != nil {
		log.Printf("Warning: malformed updated_at timestamp %q in repo_git_snapshot for %s: %v", updatedAt, repoRoot, parseErr)
	}
	snap.UpdatedAt = parsedTime
	return &snap, nil
}

// UpsertGitCommits stores commit metadata in batch.
func (s *Store) UpsertGitCommits(commits []gitmeta.CommitInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(commits) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO git_commits
		(commit_hash, author_name, author_email, author_time, subject, body, trailers_json, is_merge, first_parent_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(commit_hash) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, c := range commits {
		_, err = stmt.Exec(c.Hash, c.AuthorName, c.AuthorEmail, c.AuthorTime.UTC().Format(time.RFC3339),
			c.Subject, c.Body, c.TrailersJSON, boolToInt(c.IsMerge), c.FirstParent)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpsertFileHistory stores file-commit associations in batch.
func (s *Store) UpsertFileHistory(changes []gitmeta.FileChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(changes) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO git_file_history
		(file_path, commit_hash, change_type, commit_time, summary_text)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(file_path, commit_hash) DO UPDATE SET
			change_type=excluded.change_type, commit_time=excluded.commit_time, summary_text=excluded.summary_text`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, c := range changes {
		_, err = stmt.Exec(c.FilePath, c.CommitHash, c.ChangeType, c.CommitTime.Format(time.RFC3339), c.Summary)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpsertFileIntents stores compacted file intent summaries in batch.
func (s *Store) UpsertFileIntents(intents []gitmeta.FileIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(intents) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO git_file_intent
		(file_path, intent_text, source_hash, commit_count, last_commit_hash, last_updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_path) DO UPDATE SET
			intent_text=excluded.intent_text, source_hash=excluded.source_hash,
			commit_count=excluded.commit_count, last_commit_hash=excluded.last_commit_hash,
			last_updated_at=excluded.last_updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, fi := range intents {
		_, err = stmt.Exec(fi.FilePath, fi.IntentText, fi.SourceHash, fi.CommitCount,
			fi.LastCommitHash, fi.LastUpdatedAt.Format(time.RFC3339))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetFileIntent returns the stored intent summary for a file.
func (s *Store) GetFileIntent(filePath string) (*gitmeta.FileIntent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var fi gitmeta.FileIntent
	var lastUpdated string
	err := s.db.QueryRow(`SELECT file_path, intent_text, source_hash, commit_count, last_commit_hash, last_updated_at
		FROM git_file_intent WHERE file_path = ?`, filePath).
		Scan(&fi.FilePath, &fi.IntentText, &fi.SourceHash, &fi.CommitCount, &fi.LastCommitHash, &lastUpdated)
	if err != nil {
		return nil, err
	}
	parsedTime, parseErr := time.Parse(time.RFC3339, lastUpdated)
	if parseErr != nil {
		log.Printf("Warning: malformed last_updated_at timestamp %q in git_file_intent for %s: %v", lastUpdated, filePath, parseErr)
	}
	fi.LastUpdatedAt = parsedTime
	return &fi, nil
}

// GetFileIntentsByPaths returns intent summaries for multiple files.
func (s *Store) GetFileIntentsByPaths(paths []string) (map[string]*gitmeta.FileIntent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(paths) == 0 {
		return nil, nil
	}
	result := make(map[string]*gitmeta.FileIntent)

	// Process in batches to avoid SQL variable limits
	batchSize := 500
	for i := 0; i < len(paths); i += batchSize {
		end := i + batchSize
		if end > len(paths) {
			end = len(paths)
		}
		batch := paths[i:end]

		placeholders := make([]string, len(batch))
		args := make([]interface{}, len(batch))
		for j, p := range batch {
			placeholders[j] = "?"
			args[j] = p
		}

		// #nosec G202 -- IN-clause placeholders are generated internally; path values stay parameterized in args.
		query := `SELECT file_path, intent_text, source_hash, commit_count, last_commit_hash, last_updated_at
			FROM git_file_intent WHERE file_path IN (` + strings.Join(placeholders, ",") + `)`

		rows, err := s.db.Query(query, args...)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			var fi gitmeta.FileIntent
			var lastUpdated string
			if err := rows.Scan(&fi.FilePath, &fi.IntentText, &fi.SourceHash, &fi.CommitCount, &fi.LastCommitHash, &lastUpdated); err != nil {
				closeWithLog("git_file_intent rows after scan failure", rows)
				return nil, err
			}
			parsedTime, parseErr := time.Parse(time.RFC3339, lastUpdated)
			if parseErr != nil {
				log.Printf("Warning: malformed last_updated_at timestamp %q in git_file_intent for %s: %v", lastUpdated, fi.FilePath, parseErr)
			}
			fi.LastUpdatedAt = parsedTime
			result[fi.FilePath] = &fi
		}
		closeWithLog("git_file_intent rows", rows)
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// GetLatestStoredCommitHash returns the most recent commit hash stored in git_commits.
// Returns empty string if no commits stored.
func (s *Store) GetLatestStoredCommitHash() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var hash string
	// NOTE: ORDER BY author_time DESC works correctly only because all stored
	// author_time values are RFC3339 in UTC, which sorts lexicographically.
	err := s.db.QueryRow(`SELECT commit_hash FROM git_commits ORDER BY author_time DESC LIMIT 1`).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return hash, err
}
