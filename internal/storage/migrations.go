package storage

import (
	"database/sql"
	"fmt"
	"log"
)

// currentSchemaVersion is the latest schema version.
// Increment this when adding new migrations.
const currentSchemaVersion = 5

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

	// Migration v3: Git metadata tables for Cold Start Enhancement.
	// Stores repository snapshots, commit history, file-level attribution, and intent summaries.
	3: {
		// repo_git_snapshot: one-row snapshot of repository state
		`CREATE TABLE IF NOT EXISTS repo_git_snapshot (
			repo_root TEXT PRIMARY KEY,
			head_ref TEXT NOT NULL DEFAULT '',
			head_commit TEXT NOT NULL DEFAULT '',
			is_detached INTEGER NOT NULL DEFAULT 0,
			is_dirty INTEGER NOT NULL DEFAULT 0,
			ahead_count INTEGER NOT NULL DEFAULT 0,
			behind_count INTEGER NOT NULL DEFAULT 0,
			staged_files INTEGER NOT NULL DEFAULT 0,
			modified_files INTEGER NOT NULL DEFAULT 0,
			untracked_files INTEGER NOT NULL DEFAULT 0,
			snapshot_summary TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,

		// git_commits: normalized commit metadata for bounded local history
		`CREATE TABLE IF NOT EXISTS git_commits (
			commit_hash TEXT PRIMARY KEY,
			author_name TEXT NOT NULL DEFAULT '',
			author_email TEXT NOT NULL DEFAULT '',
			author_time TEXT NOT NULL DEFAULT '',
			subject TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL DEFAULT '',
			trailers_json TEXT NOT NULL DEFAULT '',
			is_merge INTEGER NOT NULL DEFAULT 0,
			first_parent_hash TEXT NOT NULL DEFAULT '',
			source_rank REAL NOT NULL DEFAULT 0
		)`,

		// git_file_history: file-level attribution between paths and commits
		`CREATE TABLE IF NOT EXISTS git_file_history (
			file_path TEXT NOT NULL,
			commit_hash TEXT NOT NULL,
			change_type TEXT NOT NULL DEFAULT 'unknown',
			commit_time TEXT NOT NULL DEFAULT '',
			summary_text TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (file_path, commit_hash)
		)`,

		// Index for efficient file lookups
		`CREATE INDEX IF NOT EXISTS idx_git_file_history_path ON git_file_history(file_path)`,

		// git_file_intent: compacted intent summaries per file
		`CREATE TABLE IF NOT EXISTS git_file_intent (
			file_path TEXT PRIMARY KEY,
			intent_text TEXT NOT NULL DEFAULT '',
			source_hash TEXT NOT NULL DEFAULT '',
			commit_count INTEGER NOT NULL DEFAULT 0,
			last_commit_hash TEXT NOT NULL DEFAULT '',
			last_updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
	},

	// Migration v4: Index on git_commits(author_time) for efficient ORDER BY author_time DESC.
	// author_time is stored as RFC3339 UTC text, which sorts lexicographically.
	4: {
		`CREATE INDEX IF NOT EXISTS idx_git_commits_author_time ON git_commits(author_time)`,
	},

	// Migration v5: Add search_terms column and file_path to FTS index.
	// search_terms stores pre-split CamelCase tokens so inner words are searchable.
	// file_path in FTS enables file-name matching via BM25.
	// The Go-based backfill hook (postMigrationHooks[5]) populates the new columns.
	5: {
		`ALTER TABLE nodes ADD COLUMN search_terms TEXT NOT NULL DEFAULT ''`,
		`DROP TABLE IF EXISTS nodes_fts`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(
			symbol_name,
			file_path,
			content_sum,
			search_terms,
			node_id UNINDEXED,
			tokenize='porter unicode61'
		)`,
	},
}

// postMigrationHooks maps schema versions to Go callbacks that run after the
// version's SQL statements are committed. Use this when a migration needs Go
// logic (e.g., calling BuildSearchTerms) that cannot be expressed in pure SQL.
var postMigrationHooks = map[int]func(s *Store) error{
	5: backfillSearchTerms,
}

// backfillSearchTerms populates the search_terms column for all existing nodes
// and rebuilds the FTS index with the new 5-column schema.
func backfillSearchTerms(s *Store) error {
	rows, err := s.db.Query(`SELECT id, symbol_name, file_path, content_sum FROM nodes`)
	if err != nil {
		return fmt.Errorf("querying nodes for backfill: %w", err)
	}
	defer rows.Close()

	type nodeRow struct {
		id, symbolName, filePath, contentSum string
	}
	var nodes []nodeRow
	for rows.Next() {
		var n nodeRow
		if err := rows.Scan(&n.id, &n.symbolName, &n.filePath, &n.contentSum); err != nil {
			return fmt.Errorf("scanning node for backfill: %w", err)
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating nodes for backfill: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning backfill transaction: %w", err)
	}
	defer tx.Rollback()

	updateStmt, err := tx.Prepare(`UPDATE nodes SET search_terms = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("preparing search_terms update: %w", err)
	}
	defer updateStmt.Close()

	ftsStmt, err := tx.Prepare(`INSERT INTO nodes_fts (symbol_name, file_path, content_sum, search_terms, node_id) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("preparing FTS insert: %w", err)
	}
	defer ftsStmt.Close()

	for _, n := range nodes {
		terms := BuildSearchTerms(n.symbolName, n.filePath)
		if _, err := updateStmt.Exec(terms, n.id); err != nil {
			return fmt.Errorf("updating search_terms for %s: %w", n.id, err)
		}
		if _, err := ftsStmt.Exec(n.symbolName, n.filePath, n.contentSum, terms, n.id); err != nil {
			return fmt.Errorf("FTS insert for %s: %w", n.id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing backfill: %w", err)
	}
	log.Printf("Backfilled search_terms for %d nodes", len(nodes))
	return nil
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

		// Run post-migration Go hook if one exists for this version.
		if hook, ok := postMigrationHooks[v]; ok {
			if err := hook(s); err != nil {
				return fmt.Errorf("post-migration hook for version %d: %w", v, err)
			}
		}
	}

	// Create the vec0 table for semantic search embeddings.
	// sqlite-vec is statically linked via CGO bindings, so this must always succeed.
	_, err = s.db.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS node_embeddings USING vec0(
		node_id TEXT PRIMARY KEY,
		embedding float[%d] distance_metric=cosine
	)`, s.embeddingDim))
	if err != nil {
		return fmt.Errorf("creating vec0 table (sqlite-vec should be statically linked): %w", err)
	}
	s.hasVecTable = true
	log.Printf("sqlite-vec vec0 table ready (embedding dim: %d)", s.embeddingDim)

	return nil
}

// SchemaVersion returns the current schema version (exported for testing)
func (s *Store) SchemaVersion() (int, error) {
	return s.getSchemaVersion()
}
