# Plan: Harness-First MCP — "Top 5 Inspectables, Not 500 Lines"

## Thesis

qb-context already has the right primitives (hybrid search, graph algorithms, blast radius, community detection). The problem is **packaging**: tools return raw evidence piles, descriptions overlap, and nothing tells the harness "here's what to do next." The fix is not adding capability — it's reshaping output contracts, descriptions, and tool surface so the harness arrives at the right symbol in fewer calls and fewer tokens.

## Harness Constraints (Portable — Claude Code + Codex)

Both Claude Code and Codex were reviewed at source level. Neither provides annotation-based routing levers (`_meta`, `searchHint`, `alwaysLoad`). The only portable levers are:

| Lever | Claude Code | Codex | Implication |
|---|---|---|---|
| **Which tools are registered** | ToolSearch defers when MCP tools > ~10% of context | All tools visible to model | Register fewer tools = less routing noise in both |
| **Tool descriptions** | Keyword-matched by ToolSearch | BM25 full-text indexed by ToolSearch | Descriptions must contain the right routing keywords |
| **Input schema field names** | Not indexed | BM25-indexed alongside descriptions | Param names are part of discoverability in Codex |
| **Bounded outputs** | Truncated at 25k tokens; >50KB persisted to disk, model sees 2KB preview | No MCP output truncation — bloat hits raw context window | Server-side output capping is essential for both, critical for Codex |
| **Tool names** | Matched by ToolSearch | Matched by ToolSearch | Names must be clear, distinct, and searchable |

**Key difference**: Claude Code truncates large MCP outputs client-side. Codex does not. So our bloated responses (architecture member lists, unranked change dumps) consume raw context in Codex — even worse than in Claude Code. **Server-side output capping is Phase 1, not a polish step.**

## Architecture: What Changes

### 1. Inspectable Contract (new shared type)

A new `Inspectable` struct that all discovery tools return instead of ad-hoc maps:

```go
type Inspectable struct {
    Rank       int               `json:"rank"`
    TargetType string            `json:"target_type"` // "symbol", "file", "entry_point", "hub", "connector"
    Name       string            `json:"name"`
    FilePath   string            `json:"file_path"`
    ID         string            `json:"id,omitempty"`
    Score      float64           `json:"score"`
    Reason     string            `json:"reason"`                // one-sentence why this result
    WhyNow     string            `json:"why_now,omitempty"`     // time-sensitive context (git change, active file)
    NextTool   string            `json:"next_tool"`             // "read_symbol", "understand", "impact", "trace_call_path"
    NextArgs   map[string]string `json:"next_args,omitempty"`   // pre-filled args for next tool call
}
```

And a wrapper:

```go
type InspectableResponse struct {
    Inspectables []Inspectable `json:"inspectables"`
    Total        int           `json:"total"`           // total candidates before capping
    Query        string        `json:"query,omitempty"`
    Summary      string        `json:"summary"`         // one-line tl;dr
}
```

**Default cap: 5 items.** Configurable via `limit` param.

### 2. Tool Surface Strategy

**The portable approach: tool profiles via selective registration.**

The `metoro-io/mcp-golang` SDK exposes `RegisterTool(name, description, handler)` only (`server.go:76-84`). No `_meta` annotations. Neither harness supports annotation-based routing. So the cleanest strategy that works for both is: **register only core tools by default**.

Add a `-profile` flag (`core`/`extended`/`full`, default `core`):
- **`core`** (default): Register 6 tools: `context`, `read_symbol`, `understand`, `impact`, `detect_changes`, `trace_call_path`
- **`extended`**: Core + `get_architecture_summary`, `get_key_symbols`, `explore`, `search_code`
- **`full`**: All 13 tools (opt-in for power users)

CLI mode always registers all 13 regardless of profile.

**Why `core` is the default**: Both harnesses handle fewer tools better — less deferral in Claude Code, less BM25 noise in Codex. The output-shape improvements and description rewrites make the 6 core tools sufficient for the vast majority of harness asks. Users who need architecture summaries, key symbols, or literal regex can opt into `extended`/`full`.

**Why `trace_call_path` is core**: It answers "how does control flow reach this code?" — a common harness ask that `context` and `understand` cannot answer. Natural follow-up from `impact` and `understand`.

**Why `get_key_symbols` stays separate from `get_architecture_summary`**: They answer different questions. Merging muddies descriptions and routing. Both are extended.

### 3. Tool Roles — Explicit Hierarchy

Codex has thinner built-ins than Claude Code (no built-in grep/glob/read), so our tools carry more weight. The hierarchy must be explicit:

| Role | Tool | When to use | Position |
|---|---|---|---|
| **Default first tool** | `context` | Any code search, discovery, "where should I look?" | Always the starting point for discovery |
| **Default exact inspection** | `read_symbol` | Read the source code of a specific symbol | The endpoint after discovery |
| **Deep inspection** | `understand` | What does this symbol do? Who calls it? | When `read_symbol` isn't enough |
| **Risk assessment** | `impact` | What breaks if I change this? | Before making changes |
| **Time-aware discovery** | `detect_changes` | What changed? What matters now? | When the question is about recent work |
| **Path tracing** | `trace_call_path` | How does control flow from A to B? | Follow-up from understand/impact |
| **Literal text fallback** | `search_code` | Exact regex/pattern matching | Only when `context` can't find it (extended) |

`context` is not "one of six tools." It is the **primary discovery tool** — the one the harness should reach for first on any code question. `read_symbol` is the **primary inspection tool** — the endpoint that `context` chains into. Everything else is a follow-up or specialized path.

### 4. Tool-by-Tool Changes

#### `context` — The Primary Discovery Tool

**Current state** (`tools.go:139-240`): Returns a map with:
- `results`: `[]SearchResult` (node + composite score, no score breakdown)
- `architecture_context`: ADR text blob joined from all `project_summaries` — always included
- `git_context`: `{ repo_snapshot, file_intents }` — raw blob from cold-start tables

**Current problem**: `SearchResult` contains only the final composite `Score` float. The five sub-signal scores (PPR, BM25, betweenness, in-degree, semantic) are computed in `hybrid.go:236-256` but discarded after compositing. Generating a meaningful `reason` field requires these component scores.

**Changes**:

*In `internal/search/hybrid.go`*:
- Add `ScoreBreakdown` to `types.SearchResult`:
  ```go
  type SearchResult struct {
      Node      ASTNode        `json:"node"`
      Score     float64        `json:"score"`
      Breakdown ScoreBreakdown `json:"breakdown,omitempty"`
  }
  type ScoreBreakdown struct {
      PPR         float64 `json:"ppr"`
      BM25        float64 `json:"bm25"`
      Betweenness float64 `json:"betweenness"`
      InDegree    float64 `json:"in_degree"`
      Semantic    float64 `json:"semantic"`
  }
  ```
- Populate `Breakdown` in the scoring loop (`hybrid.go:235-257`) by capturing the per-signal normalized values before compositing. This is ~6 lines of change in the existing loop — each signal value is already a local variable; we just store them.

*In `internal/mcp/tools.go` (contextHandler)*:
- Replace ad-hoc map return with `InspectableResponse`
- Change default limit from 10 to 5
- **Remove** `architecture_context` blob (lines 227-234 — delete the `GetAllProjectSummaries` call). This is `get_architecture_summary`'s job.
- **Replace** raw `git_context` blob (lines 186-238) with per-item `why_now` strings. Currently fetches `FileIntentsByPaths` and attaches the full intents map. New: for each result, if its `file_path` has a file intent, set `inspectable.WhyNow = intent_text`.
- Generate `reason` from `ScoreBreakdown`: pick the dominant signal (e.g., "Lexical match + high PageRank" when BM25 and PPR are the top two components)
- Set `next_tool`: `"read_symbol"` for functions/methods, `"understand"` for structs/classes/interfaces
- Pre-fill `next_args`: use node ID when available (`{"symbol_id": node.ID}`), fall back to `node.SymbolName` when ID is empty. IDs are durable hashes; names can collide across files.

#### `get_architecture_summary` — Biggest Current Token Bloat

**Current state** (`tools.go:1323-1418`): Returns a map with:
- `communities`: Full `[]namedCommunity` where each community lists **all member symbol names** (`tools.go:1334-1353`). For large codebases this is easily 10-50KB.
- `entry_points`: All zero-in-degree nodes, up to `limit` (default 10)
- `hubs`: Top `limit` by out-degree, with out-degree counts
- `connectors`: Top `limit` by betweenness + multi-community edges
- `modularity`, `community_count`, `total_nodes`, `total_edges`

**Current problem**: The full `members` lists in communities are the single biggest response size issue. A codebase with 500 symbols and 5 communities means ~500 names serialized. In Codex (no client-side truncation), this bloat hits the context window raw.

**Changes**:
- Return `InspectableResponse` with the top 5 most structurally important nodes
- Collect entry points, hubs, and connectors into a single candidate pool
- Rank all candidates by: `0.4*pagerank + 0.3*betweenness + 0.3*normalized_out_degree`
- Set `target_type` to distinguish "entry_point" / "hub" / "connector"
- Generate `reason`: "Entry point: 12 downstream callees" or "Hub: calls 15 functions"
- **Drop full `communities[].members` lists entirely**. Include in `summary`: "5 communities detected (modularity 0.75), 523 nodes, 1247 edges"
- Set `next_tool`: `"understand"` for entry points, `"impact"` for hubs/connectors

#### `detect_changes` — Real Ranking Refactor (Not Just Output Reshaping)

**Current state** (`tools.go:1144-1276`): Returns a map with:
- `changed_files`: Raw `git diff --name-only` output
- `changed_symbols`: All nodes from modified files, each tagged `"file_modified"` — **no ranking, no scoring**
- `new_symbols`: Files with no stored nodes
- `deleted_symbols`: Nodes from deleted files
- `file_intents`: Raw intent map for changed files
- `summary`: Counts only ("12 files changed, 45 symbols in modified files")

**Current problem**: This tool does **zero ranking**. It dumps every symbol in every changed file. A 10-file change touching 200 symbols produces 200 entries. In Codex, all 200 hit the context window. This is the tool where "top 5 inspectables" requires the most new logic.

**Changes — significant refactor**:

1. **Collect changed symbols** (keep existing `git diff` + file categorization logic)
2. **New: Score each changed symbol** by combining:
   - **Betweenness centrality** from `node_scores` table (bottleneck importance)
   - **Dependent count** via `BlastRadiusWithDepth(id, 1)` — direct dependents only (cheap)
   - **File-level change significance**: deleted > new_file > file_modified
3. **New: Composite rank**: `0.5*betweenness + 0.3*normalized_dependent_count + 0.2*change_severity`
   where `change_severity` = 1.0 for deleted, 0.7 for new, 0.5 for modified
4. **Return `InspectableResponse`** with top 5 by composite rank
5. **Distill** `file_intents` into per-item `why_now` (same pattern as `context`)
6. Set `next_tool`: `"impact"` for high-betweenness changes, `"read_symbol"` for others
7. Include in `summary`: "3 high-risk changes (betweenness > 0.1), 12 total modified symbols, 2 new files, 1 deleted"

**Performance note**: `BlastRadiusWithDepth` per symbol at depth 1 is cheap for typical diffs (20-100 symbols). Threshold: if `len(changedSymbols) > 100`, use betweenness-only ranking.

**Pragmatism note**: The composite formula is a starting point. If it produces noisy rankings in practice, the first version can ship with a simpler deterministic heuristic (e.g., sort by betweenness descending, break ties by change severity). The goal of Phase 2 is ranked output with a cap, not a perfect scoring function. Don't let tuning block the phase.

#### `impact` — Keep Shape, Add Chaining + Caps

**Current state** (`tools.go:305-394`): Good structure — risk-tiered groups (direct/high/medium/low) with impactNode structs. Has summary string.

**Changes** (output-shape only, no new logic):
- Add `next_tool: "read_symbol"` and `next_args: {"symbol_id": symbol_name}` to each impactNode
- Cap each risk tier at 5 entries, add tier counts in summary
- Add `next_tool: "trace_call_path"` as alternate on direct dependents

#### `read_symbol` — Keep As-Is, Add Chaining Hint

**Current state** (`tools.go:442-523`): Returns exact source code + metadata. Clean endpoint tool.

**Change**: Add `next_tools` array to response, using ID (durable hash) with name as fallback:
```go
symbolRef := node.ID
if symbolRef == "" { symbolRef = node.SymbolName }
response["next_tools"] = []map[string]string{
    {"tool": "understand", "args_hint": `{"symbol": "` + symbolRef + `"}`},
    {"tool": "impact", "args_hint": `{"symbol_id": "` + symbolRef + `"}`},
}
```

#### `understand` — Keep As-Is, Add Chaining Hint

**Current state** (`tools.go:1700-1802`): Returns symbol detail + callers + callees + pagerank + community + file_intent (already uses `GetFileIntent` at line 1737).

**Change**: Add `next_tools` array (same ID-over-name pattern):
```go
symbolRef := found.ID
if symbolRef == "" { symbolRef = found.Name }
result["next_tools"] = []map[string]string{
    {"tool": "impact", "args_hint": `{"symbol_id": "` + symbolRef + `"}`},
    {"tool": "trace_call_path", "args_hint": `{"from": "` + symbolRef + `"}`},
}
```

#### `trace_call_path` — Core, Add Chaining

**Current state**: Returns path arrays of hash IDs resolved to symbol names.

**Change**: For each node in returned paths, add `read_with` hint:
```go
{"symbol_name": name, "file_path": path, "read_symbol_args": {"symbol_id": name}}
```

#### `get_key_symbols` — Extended, Adopt Inspectable Contract

Convert output to `InspectableResponse` with `target_type: "symbol"`, `reason` based on which centrality metric ranked it highest.

#### `explore` — Extended, Adopt Inspectable Contract

Convert matches to `InspectableResponse`. Keep hotspots and dependency analysis as additional fields.

#### `search_code` — Extended, Literal Fallback

No structural change. Rewrite description to position as "literal regex search — use `context` first for semantic discovery." Not the main path.

#### `query`, `index`, `health` — Operational, No Change

Rewrite descriptions to say "system maintenance" / "advanced debugging."

### 5. Description Rewrite

All 13 tools get rewritten descriptions. Routing keywords embedded directly since neither harness supports `searchHint`:

| Tool | New description |
|---|---|
| `context` | "Ranked code discovery. Returns the top 5 symbols most relevant to your query with scores, reasons, and next-step tool recommendations. Start here for any code search or exploration." |
| `read_symbol` | "Read exact source code of a symbol by name or ID. Returns the precise byte range from disk. Use after context or explore to inspect a specific result." |
| `understand` | "Deep analysis of a symbol: callers, callees, PageRank importance, community membership, and recent file changes. Use to understand what a symbol does and why it matters." |
| `impact` | "Blast radius: finds all downstream dependents of a symbol, grouped by risk level (CRITICAL/HIGH/MEDIUM/LOW). Use before making changes to assess risk." |
| `detect_changes` | "What changed and what matters most? Returns the top 5 highest-risk symbol changes since a git ref, ranked by centrality and blast radius." |
| `trace_call_path` | "Finds the call chain between two symbols using bidirectional graph traversal. Use to understand how control flows from A to B." |
| `get_architecture_summary` | "Top entry points, hubs, and connectors in the codebase ranked by structural importance. Bounded output — returns counts and top 5 nodes, not full member lists." |
| `get_key_symbols` | "Top symbols ranked by graph centrality (PageRank, betweenness). Use to find the most structurally important code in a directory or the whole codebase." |
| `explore` | "Search for symbols by name with optional dependency traversal and hotspot detection. Use for targeted symbol lookup when you know a partial name." |
| `search_code` | "Literal regex search across source files. Fallback for exact text/pattern matching when context does not find what you need." |
| `query` | "Execute a read-only SQL query against the code graph database. Advanced: for custom analysis not covered by other tools." |
| `index` | "Trigger re-indexing of the codebase or a specific file path. Use after bulk file changes." |
| `health` | "System health: node count, edge count, index status, version." |

### 6. Input Schema Audit

Codex BM25-indexes `input_schema.properties` names. Our param names are part of discoverability. Review:

| Current param | Tool(s) | Verdict | Notes |
|---|---|---|---|
| `query` | `context` | **Keep** | Clear, universal search term |
| `symbol_id` | `impact`, `read_symbol` | **Keep** | Descriptive. Consider adding alias `symbol` that maps to same field for ease of use |
| `symbol` | `explore`, `understand` | **Keep** | Consistent with `symbol_id` intent |
| `since` | `detect_changes` | **Keep** | Standard git terminology |
| `pattern` | `search_code` | **Keep** | Standard regex terminology |
| `file_filter` | `search_code`, `get_key_symbols` | **Keep** | Descriptive for scoping |
| `limit` | Many tools | **Keep** | Universal pagination term |
| `depth` | `impact`, `explore` | **Keep** | Standard graph traversal term |
| `from` / `to` | `trace_call_path` | **Keep** | Clear directionality |
| `mode` | `context` | **Review** | Only value is "architecture" — consider deprecating since `get_architecture_summary` exists |
| `max_per_file` | `context` | **Keep** | Descriptive dedup control |
| `active_files` | `context` | **Keep** | PPR personalization |
| `include_deps` | `explore` | **Keep** | Clear toggle |
| `path` | `index`, `detect_changes` | **Keep** | Standard |
| `sql` | `query` | **Keep** | Self-evident |

**Action**: No breaking renames needed. Current names are descriptive and BM25-friendly. One cleanup: deprecate `mode` param on `context` (the `"architecture"` mode duplicates `get_architecture_summary`). If backward compat is needed, keep accepting `mode=architecture` but don't document it.

### 7. Response Size Budget

| Tool | Current typical size | Target size | Change type |
|---|---|---|---|
| `get_architecture_summary` | 5-100 KB (full member lists) | < 3 KB | Output cap — **Phase 1 priority** |
| `context` (10 results + ADR + git blobs) | 5-15 KB | < 3 KB | Output reshape + limit reduction |
| `detect_changes` (all symbols) | 2-50 KB | < 2 KB | New ranking logic + cap |
| `impact` (all affected nodes) | 2-20 KB | < 5 KB | Cap tiers at 5 each |
| `read_symbol` | 0.5-50 KB | Same | Endpoint, no change |
| `understand` | 2-5 KB | Same + hints | No change to data volume |

In Codex (no client-side truncation), oversized responses from `get_architecture_summary` and `detect_changes` consume raw context tokens. Capping these is the single highest-leverage change for Codex compatibility.

### 8. Implementation Phases

#### Phase 1: Output Capping + Inspectable Contract + Context Reshape
**Files**: `internal/types/types.go`, `internal/mcp/tools.go`, `internal/search/hybrid.go`
**Difficulty**: Medium
**Why first**: Output capping is the most urgent harness-compatibility fix. Architecture bloat and context verbosity are actively harmful in Codex.

**types.go:**
1. Add `Inspectable`, `InspectableResponse`, `ScoreBreakdown` types

**hybrid.go:**
2. Add `ScoreBreakdown` field to `SearchResult`
3. In `Search()` scoring loop (`hybrid.go:235-257`): capture the five per-signal normalized values into `result.Breakdown`. ~6 lines of change.

**tools.go — get_architecture_summary (output cap):**
4. Refactor `architectureSummaryHandler`:
   - Collect entry points, hubs, connectors into single candidate pool
   - Rank by composite: pagerank + betweenness + normalized degree
   - Return `InspectableResponse` with top 5
   - Drop `communities[].members` lists; keep counts + modularity in `summary`

**tools.go — context (reshape):**
5. Add helper `func toInspectable(r types.SearchResult, fileIntent string) types.Inspectable`
6. Refactor `contextHandler`:
   - Default limit 5
   - Remove `architecture_context` blob (lines 227-234)
   - Replace raw `git_context` blob with per-result `why_now` lookup
   - Deprecate `mode=architecture` (keep accepting, don't document)
   - Return `InspectableResponse`

**tools.go — descriptions:**
7. Rewrite `context` and `get_architecture_summary` descriptions

**Tests:**
8. Update `tools_test.go` for new response shapes
9. Add `hybrid_test.go` case verifying `ScoreBreakdown` is populated

#### Phase 2: Detect Changes Ranking Refactor
**Files**: `internal/mcp/tools.go` (detect_changes handler)
**Difficulty**: Medium-High (new ranking logic, not just reshaping)

1. Keep existing `git diff` + file categorization logic
2. New: score each changed symbol (betweenness + dependent count + change severity)
3. Composite rank, sort, take top 5
4. Build `InspectableResponse` with per-item `why_now`
5. Performance threshold: betweenness-only if >100 symbols
6. Rewrite description, update tests

#### Phase 3: Chaining Hints + Impact Caps
**Files**: `internal/mcp/tools.go` (impact, read_symbol, understand, trace_call_path)
**Difficulty**: Low (output additions only)

1. `impact`: Add `next_tool`/`next_args` per impactNode, cap tiers at 5
2. `read_symbol`: Add `next_tools` array
3. `understand`: Add `next_tools` array
4. `trace_call_path`: Add per-node `read_symbol_args` hints
5. Rewrite all four descriptions, update tests

#### Phase 4a: Tool Profiles
**Files**: `internal/config/config.go`, `internal/mcp/server.go`, `internal/mcp/tools.go`, `cmd/qb-context/main.go`
**Difficulty**: Low-Medium (surface control only, no output-shape changes)

1. Add `-profile` flag (`core`/`extended`/`full`, default `core`)
2. Gate SDK registration by profile:
   ```go
   if profile == "core" && !isCoreTools(toolName) {
       // Register CLI handler only, skip RegisterSDKTool
       continue
   }
   ```
3. Rewrite remaining descriptions (`search_code`, `query`, `index`, `health`)
4. Deprecate `mode` param on `context`
5. Update tests

#### Phase 4b: Extended Tools Output Shapes
**Files**: `internal/mcp/tools.go` (explore, get_key_symbols handlers)
**Difficulty**: Medium (output-shape work, independent of profile plumbing)

1. Convert `explore` matches to `InspectableResponse`
2. Convert `get_key_symbols` output to `InspectableResponse`
3. Update tests

Phase 4a and 4b can be done in parallel — they share no code paths.

### 9. Testing Strategy

For each phase:
- Unit tests for new types and helpers (`toInspectable`, ranking logic)
- Update existing tool tests to verify new response shapes parse correctly
- Integration test: verify response JSON size stays under budget on qbapi codebase
- **Phase 1 specifically**: Measure `get_architecture_summary` response size before/after on qbapi — confirm drop from 10-100KB to <3KB
- **Phase 2 specifically**: Test ranking correctness — high-betweenness symbol in modified file outranks zero-betweenness symbol

### 10. Success Metrics

| Metric | Before | Target |
|---|---|---|
| `get_architecture_summary` response size | 5-100 KB | < 3 KB |
| `context` response size | 5-15 KB | < 3 KB |
| `context` default items returned | 10 | 5 |
| Tool calls to reach right symbol | 3-5 (context → read → grep → read) | 2 (context → read_symbol, args pre-filled) |
| Tokens consumed per inspection loop | ~5000-10000 | ~2000-3000 |
| Tools in default surface | 13 | 6 (core profile) |
| Each discovery result has next-step hint | No | Yes |
| `detect_changes` returns ranked results | No (raw dump) | Yes (by centrality + blast radius) |

### 11. What We're NOT Doing

- **Not relying on `_meta`/`searchHint` annotations** — neither harness supports them via our SDK; portable strategy uses registration + descriptions only
- **Not merging `get_key_symbols` into `get_architecture_summary`** — different questions, different routing
- **Not deleting tools** — all 13 remain in `full` profile
- **Not building async/streaming** — the problem is output shape, not delivery mechanism
- **Not building a project instructions loader** — harnesses already have CLAUDE.md etc.
- **Not competing with grep/glob** — we sit above them as a ranked inspection layer
- **Not changing search engine ranking weights** — `HybridSearch.Search()` composite formula is fine; we're surfacing its signals and capping its output
- **Not breaking input schema field names** — current names are BM25-friendly; deprecate `mode` param, don't rename existing fields
