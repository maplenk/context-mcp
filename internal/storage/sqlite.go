package storage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
	"github.com/naman/qb-context/internal/types"
)

// DefaultEmbeddingDim is the default embedding dimension (TFIDF fallback).
const DefaultEmbeddingDim = 384

// sqliteVecOnce ensures sqlite_vec.Auto() is called exactly once.
var sqliteVecOnce sync.Once

// dangerousPatternRegexes is compiled once at package level to avoid
// recompiling on every RawQuery call (M14).
var dangerousPatternRegexes []*regexp.Regexp

func init() {
	patterns := []string{"load_extension", "writefile", "readfile", "fts3_tokenizer", "attach", "pragma", "vacuum", "reindex", "recursive"}
	dangerousPatternRegexes = make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		dangerousPatternRegexes[i] = regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(p) + `\b`)
	}
}

// Store manages all SQLite database operations
type Store struct {
	db           *sql.DB
	mu           sync.RWMutex
	hasVecTable  bool // true if sqlite-vec vec0 table was created successfully
	embeddingDim int  // configurable embedding dimension
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
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000&_trusted_schema=off")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Verify connection
	if err := db.Ping(); err != nil {
		db.Close()
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
		db.Close()
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
		INSERT INTO nodes (id, file_path, symbol_name, node_type, start_byte, end_byte, content_sum)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			file_path = excluded.file_path,
			symbol_name = excluded.symbol_name,
			node_type = excluded.node_type,
			start_byte = excluded.start_byte,
			end_byte = excluded.end_byte,
			content_sum = excluded.content_sum`,
		node.ID, node.FilePath, node.SymbolName, uint8(node.NodeType),
		node.StartByte, node.EndByte, node.ContentSum,
	)
	if err != nil {
		return err
	}

	// Update FTS index (delete old entry then insert new)
	if _, err := tx.Exec("DELETE FROM nodes_fts WHERE node_id = ?", node.ID); err != nil {
		return fmt.Errorf("FTS delete: %w", err)
	}
	_, err = tx.Exec(
		"INSERT INTO nodes_fts (symbol_name, content_sum, node_id) VALUES (?, ?, ?)",
		node.SymbolName, node.ContentSum, node.ID)
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
		INSERT INTO nodes (id, file_path, symbol_name, node_type, start_byte, end_byte, content_sum)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			file_path = excluded.file_path,
			symbol_name = excluded.symbol_name,
			node_type = excluded.node_type,
			start_byte = excluded.start_byte,
			end_byte = excluded.end_byte,
			content_sum = excluded.content_sum`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	ftsDeleteStmt, err := tx.Prepare("DELETE FROM nodes_fts WHERE node_id = ?")
	if err != nil {
		return err
	}
	defer ftsDeleteStmt.Close()

	ftsStmt, err := tx.Prepare("INSERT INTO nodes_fts (symbol_name, content_sum, node_id) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer ftsStmt.Close()

	for _, node := range nodes {
		_, err := stmt.Exec(node.ID, node.FilePath, node.SymbolName, uint8(node.NodeType),
			node.StartByte, node.EndByte, node.ContentSum)
		if err != nil {
			return err
		}
		if _, err := ftsDeleteStmt.Exec(node.ID); err != nil {
			return fmt.Errorf("FTS delete for %s: %w", node.ID, err)
		}
		if _, err := ftsStmt.Exec(node.SymbolName, node.ContentSum, node.ID); err != nil {
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

	// Delete only outgoing edges (where source belongs to this file).
	// Incoming edges from other files are preserved — they may become stale if
	// target nodes are removed, but that is handled by graph pruning, not here.
	_, err = tx.Exec(`
		DELETE FROM edges WHERE source_id IN (SELECT id FROM nodes WHERE file_path = ?)`, filePath)
	if err != nil {
		return err
	}

	// Delete embeddings (ignore error only if vec0 table doesn't exist)
	if _, err := tx.Exec(`
		DELETE FROM node_embeddings WHERE node_id IN (SELECT id FROM nodes WHERE file_path = ?)`, filePath); err != nil {
		if !strings.Contains(err.Error(), "no such table") {
			return err
		}
	}

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
	if _, err := tx.Exec("INSERT INTO nodes_fts (symbol_name, content_sum, node_id) VALUES (?, ?, ?)",
		node.SymbolName, node.ContentSum, node.ID); err != nil {
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
		       bm25(nodes_fts, 10.0, 1.0, 0.0) as score
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
		       bm25(nodes_fts, 10.0, 1.0, 0.0) as score
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

// HasVecTable returns whether the sqlite-vec virtual table is available
func (s *Store) HasVecTable() bool {
	return s.hasVecTable
}

// SearchSemantic performs KNN semantic search using sqlite-vec.
// Returns nil, nil (empty results, no error) if sqlite-vec is not available,
// enabling graceful degradation to lexical-only search.
// Returns an error if the query embedding dimension does not match the store's
// configured dimension, preventing garbage KNN results from mismatched vectors.
func (s *Store) SearchSemantic(queryEmbedding []float32, limit int) ([]types.SearchResult, error) {
	if !s.hasVecTable {
		return nil, nil
	}

	if len(queryEmbedding) != s.embeddingDim {
		return nil, fmt.Errorf("query embedding dimension mismatch: got %d, want %d", len(queryEmbedding), s.embeddingDim)
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

	// Reject multi-statement queries (semicolons outside string literals).
	// Note: this may reject queries with semicolons in string literals.
	// Use parameterized values to avoid this limitation.
	if strings.Contains(query, ";") {
		return nil, fmt.Errorf("multi-statement queries are not allowed (semicolons forbidden)")
	}

	// Reject queries containing dangerous SQLite functions/patterns.
	// Uses word-boundary regex (pre-compiled at package level) to avoid false
	// positives (e.g., "attachment", "credited").
	// "edit" is intentionally omitted — it appears in normal identifiers and the real
	// protection against edit() is PRAGMA query_only / read-only transactions.
	dangerousNames := []string{"load_extension", "writefile", "readfile", "fts3_tokenizer", "attach", "pragma", "vacuum", "reindex", "recursive"}
	for i, re := range dangerousPatternRegexes {
		if re.MatchString(query) {
			return nil, fmt.Errorf("query contains forbidden pattern: %s", dangerousNames[i])
		}
	}

	// Clamp results to 500 to prevent DoS via unbounded result sets.
	// Wrap the user query in a subquery with an outer LIMIT 500.
	// This avoids parsing/replacing LIMIT clauses (which can be bypassed via
	// subqueries or Atoi overflow) and guarantees at most 500 rows are returned.
	query = "SELECT * FROM (" + query + ") LIMIT 500"

	// Pin a connection from the pool and enforce read-only at the SQLite level.
	// mattn/go-sqlite3 ignores TxOptions{ReadOnly: true}, so we must use PRAGMA.
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		return nil, fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Close()

	// Enable query_only mode to prevent any writes
	if _, err := conn.ExecContext(context.Background(), "PRAGMA query_only = ON"); err != nil {
		return nil, fmt.Errorf("enabling query_only: %w", err)
	}
	// Always restore the connection to read-write before returning to pool.
	// If restoration fails, close the connection to prevent returning a
	// poisoned (stuck read-only) connection to the pool.
	defer func() {
		if _, err := conn.ExecContext(context.Background(), "PRAGMA query_only = OFF"); err != nil {
			conn.Close()
		}
	}()

	rows, err := conn.QueryContext(context.Background(), query)
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

// SearchNodesByName searches for nodes whose symbol_name contains the given pattern (case-insensitive).
// LIKE wildcards (%, _) in the pattern are escaped using '\' as the ESCAPE character.
// Results are capped at 100 rows to prevent unbounded result sets for short patterns.
func (s *Store) SearchNodesByName(pattern string) ([]types.ASTNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Escape LIKE wildcards in the user-provided pattern
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(pattern)

	rows, err := s.db.Query(`
		SELECT id, file_path, symbol_name, node_type, start_byte, end_byte, content_sum
		FROM nodes WHERE symbol_name LIKE ? ESCAPE '\' COLLATE NOCASE
		LIMIT 100`, "%"+escaped+"%")
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

// GetSymbolIndex returns a map of symbol_name -> node_id for class, struct, and
// interface nodes. Used for cross-file edge resolution during incremental re-index.
func (s *Store) GetSymbolIndex() (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT symbol_name, id FROM nodes
		WHERE node_type IN (?, ?, ?)`,
		uint8(types.NodeTypeClass), uint8(types.NodeTypeStruct), uint8(types.NodeTypeInterface))
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
