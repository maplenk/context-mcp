package storage

import (
	"fmt"
	"log"
)

// runMigrations creates all required tables and indexes
func (s *Store) runMigrations() error {
	migrations := []string{
		// Core nodes table
		`CREATE TABLE IF NOT EXISTS nodes (
			id TEXT PRIMARY KEY,
			file_path TEXT NOT NULL,
			symbol_name TEXT NOT NULL,
			node_type INTEGER NOT NULL,
			start_byte INTEGER NOT NULL,
			end_byte INTEGER NOT NULL,
			content_sum TEXT NOT NULL DEFAULT ''
		)`,

		// Index on file_path for fast file-based lookups and deletions
		`CREATE INDEX IF NOT EXISTS idx_nodes_file_path ON nodes(file_path)`,

		// Index on symbol_name for exact-match lookups
		`CREATE INDEX IF NOT EXISTS idx_nodes_symbol_name ON nodes(symbol_name)`,

		// Edges table with foreign key constraints
		`CREATE TABLE IF NOT EXISTS edges (
			source_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			edge_type INTEGER NOT NULL,
			PRIMARY KEY (source_id, target_id, edge_type),
			FOREIGN KEY (source_id) REFERENCES nodes(id) ON DELETE CASCADE,
			FOREIGN KEY (target_id) REFERENCES nodes(id) ON DELETE CASCADE
		)`,

		// Indexes for edge traversal
		`CREATE INDEX IF NOT EXISTS idx_edges_source ON edges(source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_edges_target ON edges(target_id)`,

		// FTS5 virtual table for lexical search (BM25)
		// Regular FTS5 table with content stored; FTS updates are managed in Go code
		// during UpsertNode and DeleteByFile to avoid content-sync trigger issues.
		`CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(
			symbol_name,
			content_sum,
			node_id UNINDEXED,
			tokenize='porter unicode61'
		)`,
	}

	for i, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("migration %d failed: %w", i, err)
		}
	}

	// Try to create the vec0 table (requires sqlite-vec extension)
	_, err := s.db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS node_embeddings USING vec0(
		node_id TEXT PRIMARY KEY,
		embedding float[384] distance_metric=cosine
	)`)
	if err != nil {
		// sqlite-vec extension not available — log but don't fail
		// Semantic search will be unavailable
		log.Printf("Warning: sqlite-vec not available, semantic search disabled: %v", err)
	}

	return nil
}
