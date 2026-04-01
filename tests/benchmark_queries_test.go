//go:build fts5 && realrepo

package tests

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestBenchmarkQueries runs 6 specific benchmark queries against the real qbapi
// repo and prints detailed results. Requires build tags: fts5,realrepo.
func TestBenchmarkQueries(t *testing.T) {
	env := getSharedEnv(t)

	t.Logf("Benchmark environment: %d nodes, %d edges, graph: %d nodes / %d edges",
		env.totalNodes, env.totalEdges, env.graphEngine.NodeCount(), env.graphEngine.EdgeCount())

	type benchQuery struct {
		id       string // e.g. "A1", "B1"
		category string // Exact, Concept, Cross-file
		tool     string // handler name
		params   string // JSON params
	}

	queries := []benchQuery{
		{
			id:       "A1",
			category: "Exact",
			tool:     "read_symbol",
			params:   `{"symbol_id": "FiscalYearController"}`,
		},
		{
			id:       "A3",
			category: "Exact",
			tool:     "search_code",
			params:   `{"pattern": "POST.*order", "limit": 10}`,
		},
		{
			id:       "B1",
			category: "Concept",
			tool:     "context",
			params:   `{"query": "payment processing and billing logic", "limit": 10}`,
		},
		{
			id:       "B6",
			category: "Concept",
			tool:     "context",
			params:   `{"query": "omnichannel integration sync logic", "limit": 10}`,
		},
		{
			id:       "C1",
			category: "Cross-file",
			tool:     "context",
			params:   `{"query": "complete flow of creating a new order end to end", "limit": 15}`,
		},
		{
			id:       "C5",
			category: "Cross-file",
			tool:     "context",
			params:   `{"query": "complete API request to database write flow for inventory", "limit": 15}`,
		},
	}

	for _, q := range queries {
		t.Run(fmt.Sprintf("%s_%s_%s", q.id, q.category, q.tool), func(t *testing.T) {
			handler, ok := env.server.GetHandler(q.tool)
			if !ok {
				t.Fatalf("handler %q not registered", q.tool)
			}

			t.Logf("Query %s [%s] tool=%s params=%s", q.id, q.category, q.tool, q.params)

			start := time.Now()
			result, err := handler(json.RawMessage(q.params))
			elapsed := time.Since(start)

			if err != nil {
				t.Fatalf("handler error: %v", err)
			}

			t.Logf("Elapsed: %v", elapsed)

			if result == nil {
				t.Fatalf("query %s returned nil result", q.id)
			}

			// Catch catastrophic performance regressions (generous upper bound).
			if elapsed > 30*time.Second {
				t.Errorf("query %s took %v (expected < 30s)", q.id, elapsed)
			}
			// Tighter performance bound for normal operation.
			if elapsed > 5*time.Second {
				t.Errorf("query %s took %v (expected < 5s) — possible performance regression", q.id, elapsed)
			}

			// Marshal result to inspect structure
			raw, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				t.Fatalf("failed to marshal result: %v", marshalErr)
			}

			// Try to decode as array of result objects
			var results []map[string]interface{}
			if json.Unmarshal(raw, &results) == nil && len(results) > 0 {
				t.Logf("Results: %d items", len(results))
				for i, r := range results {
					symbolName, _ := r["symbol_name"].(string)
					filePath, _ := r["file_path"].(string)
					nodeType, _ := r["node_type"].(string)
					score, _ := r["score"].(float64)
					// Some tools use different field names
					if symbolName == "" {
						if sn, ok := r["name"].(string); ok {
							symbolName = sn
						}
					}
					if filePath == "" {
						if fp, ok := r["file"].(string); ok {
							filePath = fp
						}
						if fp, ok := r["path"].(string); ok {
							filePath = fp
						}
					}
					t.Logf("  [%d] symbol=%s type=%s file=%s score=%.4f",
						i, symbolName, nodeType, filePath, score)
				}
				if len(results) == 0 {
					t.Error("expected non-empty results")
				}
			} else {
				// Single object result (e.g. read_symbol)
				var single map[string]interface{}
				if json.Unmarshal(raw, &single) == nil {
					t.Logf("Result (single): %d fields", len(single))
					for k, v := range single {
						switch val := v.(type) {
						case string:
							if len(val) > 200 {
								t.Logf("  %s: %s... (%d chars)", k, val[:200], len(val))
							} else {
								t.Logf("  %s: %s", k, val)
							}
						default:
							t.Logf("  %s: %v", k, v)
						}
					}
				} else {
					// Fallback: just log raw JSON length
					t.Logf("Raw result: %d bytes", len(raw))
				}
			}
		})
	}
}
