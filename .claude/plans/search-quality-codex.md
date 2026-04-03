# Search Quality Upgrade for qb-context

## Summary
- `qb-context` is a local-first Go indexer plus MCP server with a pipeline of parse -> store -> graph -> hybrid search. The main quality gaps are in [internal/search/hybrid.go](/Users/naman/Documents/qb-context/internal/search/hybrid.go): lexical phrase noise, no intent-aware path priors, file nodes indexed by path only, and weak coverage for non-symbol assets.
- Keep the `context` tool contract unchanged and improve the default `fts5`/TFIDF path first. ONNX should remain optional and only amplify the same ranking pipeline.
- Optimize for general search quality, but use the benchmark B/C queries and the human answer key as the acceptance target.

## Implementation Changes
- Add an internal query-intent prepass in [internal/search/hybrid.go](/Users/naman/Documents/qb-context/internal/search/hybrid.go) with three modes: `exact`, `concept`, and `flow`.
- Classify `flow` queries by phrases like `end to end`, `flow`, `request to`, `from ... to ...`, `callback`, and `lifecycle`; treat the rest as `concept` unless they are obvious exact-symbol lookups.
- Strip low-signal phrase tokens from the lexical branch only (`complete`, `logic`, `handling`, `management`, `end to end`) while keeping the original query for embeddings.
- Add a small general synonym expansion map for stable developer vocabulary, not benchmark IDs: `auth -> oauth token session middleware`, `inventory -> stock ledger warehouse`, `webhook -> callback`, `omnichannel -> unicommerce easyecom online order`, `billing -> invoice payment`.
- Change retrieval from one flat candidate pool to three merged pools:
- symbol candidates from current BM25 plus embeddings,
- file/document candidates from searchable `file` nodes,
- graph-neighborhood candidates expanded from the strongest initial seeds.
- Make scoring intent-aware:
- always downweight `tests/`, `examples/`, `docs/`, `lib/`, `vendor/`, and generated/helper files,
- downweight `database/migrations/` unless the query is schema/migration/database oriented,
- boost production layers conditionally: routes/controllers/services/jobs/models for `flow`, middleware/config/auth files for auth-like concepts, schema/config files for schema/config concepts.
- Add a neighborhood/coverage bonus for `flow` queries so connected multi-file clusters rank above isolated lexical hits, and diversify results across layers instead of letting one file dominate.
- Expand indexed surfaces in [cmd/qb-context/main.go](/Users/naman/Documents/qb-context/cmd/qb-context/main.go) without changing public APIs:
- keep AST parsing for supported source files,
- add lightweight searchable `NodeTypeFile` document nodes for route-only PHP files, `config/**/*.php`, `resources/views/**/*.blade.php`, `cloud_schema/**/*.sql`, `*.md`, and any walked file that yields no useful symbols but has useful text,
- populate those file nodes with bounded sanitized text snippets plus filename/path tokens, not path-only `content_sum`,
- embed those file nodes too so `context` can return them under the existing `file` target type.
- Align the real-repo harness in [tests/realrepo_test.go](/Users/naman/Documents/qb-context/tests/realrepo_test.go) with production indexing so tests and daemon behavior do not drift.

## Public Interfaces / Types
- No MCP or CLI parameter changes.
- `context` keeps the same request and response shape.
- File/document hits continue to use the existing `file` result type; no new public node type is introduced.

## Test Plan
- Add unit tests for intent classification, synonym expansion, phrase stripping, path-prior scoring, and flow-neighborhood boosting.
- Add integration tests proving `context` can return relevant file hits for a route file, a schema SQL file, a config/doc file, and a Blade/view file.
- Replace the current shallow real-repo relevance checks with rubric-based benchmark checks derived from the human answers.
- Create a machine-readable answer-key fixture from `benchmarks/human-answers.md` and assert minimum top-k hit counts for B1-B6 and C1-C5, with special focus on B2, B5, B6, C1, C3, and C5.
- Keep latency guardrails: no regression past existing test limits, and benchmark queries should remain in the current fast path, comfortably below a few hundred milliseconds.

## Assumptions
- The quality win must hold on the default `fts5` build with TFIDF; ONNX is optional follow-up, not a requirement.
- We are not adding per-query hardcoded rules keyed to benchmark IDs.
- It is acceptable for `context` to return a mix of symbols and files when that improves relevance, especially for flow, schema, config, and architecture-style queries.
