package storage

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/naman/qb-context/internal/types"
	_ "github.com/mattn/go-sqlite3"
)

// Store manages all SQLite database operations
type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewStore opens (or creates) a SQLite database at the given path and runs migrations
func NewStore(dbPath string) (*Store, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Verify connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	s := &Store{db: db}

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

// DB returns the underlying sql.DB for raw queries
func (s *Store) DB() *sql.DB {
	return s.db
}

// UpsertNode inserts or replaces a node in the database
func (s *Store) UpsertNode(node types.ASTNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO nodes (id, file_path, symbol_name, node_type, start_byte, end_byte, content_sum)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		node.ID, node.FilePath, node.SymbolName, uint8(node.NodeType),
		node.StartByte, node.EndByte, node.ContentSum,
	)
	return err
}

// UpsertEdge inserts or replaces an edge in the database
func (s *Store) UpsertEdge(edge types.ASTEdge) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO edges (source_id, target_id, edge_type)
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
		INSERT OR REPLACE INTO nodes (id, file_path, symbol_name, node_type, start_byte, end_byte, content_sum)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, node := range nodes {
		_, err := stmt.Exec(node.ID, node.FilePath, node.SymbolName, uint8(node.NodeType),
			node.StartByte, node.EndByte, node.ContentSum)
		if err != nil {
			return err
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
		INSERT OR REPLACE INTO edges (source_id, target_id, edge_type)
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

	// Delete edges where source or target is a node from this file
	_, err = tx.Exec(`
		DELETE FROM edges WHERE source_id IN (SELECT id FROM nodes WHERE file_path = ?)
		OR target_id IN (SELECT id FROM nodes WHERE file_path = ?)`, filePath, filePath)
	if err != nil {
		return err
	}

	// Delete embeddings for nodes from this file
	_, err = tx.Exec(`
		DELETE FROM node_embeddings WHERE node_id IN (SELECT id FROM nodes WHERE file_path = ?)`, filePath)
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

// UpsertEmbedding stores a vector embedding for a node
func (s *Store) UpsertEmbedding(nodeID string, embedding []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	blob := serializeFloat32(embedding)
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO node_embeddings (node_id, embedding)
		VALUES (?, ?)`, nodeID, blob)
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
		FROM nodes WHERE symbol_name = ? LIMIT 1`, symbolName)

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

// SearchLexical performs FTS5 BM25 lexical search
func (s *Store) SearchLexical(query string, limit int) ([]types.SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT n.id, n.file_path, n.symbol_name, n.node_type, n.start_byte, n.end_byte, n.content_sum,
		       bm25(nodes_fts) as score
		FROM nodes_fts fts
		JOIN nodes n ON n.id = fts.rowid_ref
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

// SearchSemantic performs KNN semantic search using sqlite-vec
func (s *Store) SearchSemantic(queryEmbedding []float32, limit int) ([]types.SearchResult, error) {
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

// RawQuery executes a read-only SQL query and returns results as maps
func (s *Store) RawQuery(query string) ([]map[string]interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(query)
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

// serializeFloat32 converts a float32 slice to a little-endian byte slice for sqlite-vec
func serializeFloat32(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}
