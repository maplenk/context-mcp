package storage

import (
	"database/sql"
	"fmt"
	"log"
)

// currentSchemaVersion is the latest schema version.
// Increment this when adding new migrations.
const currentSchemaVersion = 2

// migrationSet maps schema versions to their SQL statements.
// Version 1 is the initial schema.
var migrationSet = map[int][]string{
	1: {
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

		// Node scores table for precomputed graph metrics
		`CREATE TABLE IF NOT EXISTS node_scores (
			node_id TEXT PRIMARY KEY,
			pagerank REAL NOT NULL DEFAULT 0,
			betweenness REAL NOT NULL DEFAULT 0,
			FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
		)`,

		// Project summaries table for ADR / architecture documents
		`CREATE TABLE IF NOT EXISTS project_summaries (
			project TEXT PRIMARY KEY,
			summary TEXT NOT NULL DEFAULT '',
			source_hash TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
	},

	// Migration v2: Remove foreign key constraints from edges table.
	// FK + INSERT OR IGNORE silently drops edges referencing non-existent nodes
	// (import edges, cross-file call edges). DeleteByFile already explicitly
	// removes edges before nodes, so CASCADE is not needed.
	// NOTE: ALTER TABLE ... RENAME requires SQLite 3.25.0+ (mattn/go-sqlite3 bundles 3.41+)
	2: {
		`CREATE TABLE IF NOT EXISTS edges_new (
			source_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			edge_type INTEGER NOT NULL,
			PRIMARY KEY (source_id, target_id, edge_type)
		)`,
		`INSERT OR IGNORE INTO edges_new SELECT source_id, target_id, edge_type FROM edges`,
		`DROP TABLE edges`,
		`ALTER TABLE edges_new RENAME TO edges`,
		`CREATE INDEX IF NOT EXISTS idx_edges_source ON edges(source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_edges_target ON edges(target_id)`,
	},
}

// getSchemaVersion returns the current schema version from the database.
// Returns 0 if the schema_version table does not exist yet.
func (s *Store) getSchemaVersion() (int, error) {
	// Check if the schema_version table exists
	var count int
	err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_version'`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("checking schema_version table: %w", err)
	}
	if count == 0 {
		return 0, nil
	}

	var version int
	err = s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("reading schema version: %w", err)
	}
	return version, nil
}

// setSchemaVersion records the schema version in the database within the given transaction.
func setSchemaVersion(tx *sql.Tx, version int) error {
	_, err := tx.Exec(`INSERT OR REPLACE INTO schema_version (version) VALUES (?)`, version)
	return err
}

// runMigrations creates the schema_version table and applies any pending migrations
func (s *Store) runMigrations() error {
	// Ensure schema_version table exists
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("creating schema_version table: %w", err)
	}

	currentVersion, err := s.getSchemaVersion()
	if err != nil {
		return fmt.Errorf("getting schema version: %w", err)
	}

	// Apply migrations from currentVersion+1 up to currentSchemaVersion
	for v := currentVersion + 1; v <= currentSchemaVersion; v++ {
		stmts, ok := migrationSet[v]
		if !ok {
			return fmt.Errorf("migration version %d not found", v)
		}

		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("beginning migration %d transaction: %w", v, err)
		}

		for i, stmt := range stmts {
			if _, err := tx.Exec(stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d statement %d failed: %w", v, i, err)
			}
		}

		if err := setSchemaVersion(tx, v); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording schema version %d: %w", v, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", v, err)
		}

		log.Printf("Applied schema migration version %d", v)
	}

	// Try to create the vec0 table (requires sqlite-vec extension)
	_, err = s.db.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS node_embeddings USING vec0(
		node_id TEXT PRIMARY KEY,
		embedding float[%d] distance_metric=cosine
	)`, s.embeddingDim))
	if err != nil {
		// sqlite-vec extension not available — log but don't fail
		// Semantic search will be unavailable; hasVecTable stays false
		log.Printf("Warning: sqlite-vec not available, semantic search disabled: %v", err)
	} else {
		s.hasVecTable = true
	}

	return nil
}

// SchemaVersion returns the current schema version (exported for testing)
func (s *Store) SchemaVersion() (int, error) {
	return s.getSchemaVersion()
}
