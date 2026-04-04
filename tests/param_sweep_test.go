//go:build fts5 && realrepo

package tests

import (
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/maplenk/context-mcp/internal/search"
)

// TestParameterSweep runs the benchmark with many different search configurations
// to find optimal parameter settings. Requires SWEEP=1 env var to run (it's slow).
func TestParameterSweep(t *testing.T) {
	if os.Getenv("SWEEP") == "" {
		t.Skip("set SWEEP=1 to run parameter sweep")
	}

	env := getSharedEnv(t)
	answerKey := parseAnswerKey(t)
	limits := map[string]int{"A": 20, "B": 40, "C": 40}

	configs := generateSweepConfigs()

	type result struct {
		idx    int
		name   string
		config search.SearchConfig
		score  benchmarkResult
	}

	var results []result
	baseline := 46 // current B+C hits

	for i, sc := range configs {
		eng := search.NewWithConfig(env.store, env.embedder, env.graphEngine, sc.config)
		score := runBenchmarkWith(eng, answerKey, limits)
		results = append(results, result{
			idx: i, name: sc.name, config: sc.config, score: score,
		})

		marker := ""
		if score.BCHits > baseline {
			marker = fmt.Sprintf(" >>> +%d IMPROVEMENT", score.BCHits-baseline)
		}
		t.Logf("[%02d] %-40s B+C=%d/%d (%.1f%%)%s",
			i, sc.name, score.BCHits, score.BCTotal, score.BCRate, marker)
	}

	// Sort by B+C hits descending and print top 10
	sort.Slice(results, func(i, j int) bool {
		return results[i].score.BCHits > results[j].score.BCHits
	})

	t.Log("\n=== TOP 10 CONFIGURATIONS ===")
	for i := 0; i < 10 && i < len(results); i++ {
		r := results[i]
		t.Logf("#%d: %-40s B+C=%d/%d (%.1f%%)", i+1, r.name, r.score.BCHits, r.score.BCTotal, r.score.BCRate)
		t.Logf("    Weights: PPR=%.2f BM25=%.2f Bet=%.2f InD=%.2f Sem=%.2f",
			r.config.WeightPPR, r.config.WeightBM25, r.config.WeightBetweenness,
			r.config.WeightInDegree, r.config.WeightSemantic)
		t.Logf("    Boosts: Route=%.1f Method=%.1f Class=%.1f Func=%.1f",
			r.config.RouteBoost, r.config.RouteMethodBoost, r.config.ClassBoost, r.config.FunctionBoost)
		t.Logf("    Expansion: Seeds=%d Neighbors=%d Bonus=%.2f",
			r.config.ExpansionSeedCount, r.config.ExpansionMaxNeighbors, r.config.ExpansionBonus)
		// Print per-query breakdown for top config
		if i == 0 {
			t.Log("    Per-query:")
			for _, q := range r.score.Queries {
				t.Logf("      [%s] %d/%d %q", q.ID, q.Hits, q.Total, q.Query)
			}
		}
	}
}

// sweepConfig pairs a descriptive name with a search configuration.
type sweepConfig struct {
	name   string
	config search.SearchConfig
}

// generateSweepConfigs builds the full set of parameter configurations to evaluate.
// Covers 5 phases: composite weights, node-type boosts, graph expansion,
// pipeline params, and path penalties.
func generateSweepConfigs() []sweepConfig {
	base := search.DefaultConfig()
	var configs []sweepConfig

	// Always include baseline
	configs = append(configs, sweepConfig{"baseline", base})

	// Phase A: Composite weight variations (~15 configs)
	// Each set sums to 1.0 across the five signals.
	weightSets := []struct {
		name                          string
		ppr, bm25, bet, ind, sem float64
	}{
		{"heavy-bm25", 0.15, 0.50, 0.10, 0.10, 0.15},
		{"heavy-ppr", 0.50, 0.20, 0.10, 0.10, 0.10},
		{"balanced", 0.25, 0.35, 0.15, 0.10, 0.15},
		{"bm25-focus", 0.20, 0.45, 0.10, 0.10, 0.15},
		{"ppr-focus", 0.45, 0.25, 0.10, 0.10, 0.10},
		{"no-semantic", 0.40, 0.30, 0.15, 0.15, 0.00},
		{"high-semantic", 0.25, 0.25, 0.10, 0.10, 0.30},
		{"no-betweenness", 0.40, 0.30, 0.00, 0.15, 0.15},
		{"no-indegree", 0.35, 0.30, 0.20, 0.00, 0.15},
		{"bm25-ppr-only", 0.50, 0.50, 0.00, 0.00, 0.00},
		{"even-split", 0.20, 0.20, 0.20, 0.20, 0.20},
		{"bm25-dominant", 0.10, 0.60, 0.10, 0.10, 0.10},
		{"ppr-dominant", 0.60, 0.15, 0.10, 0.05, 0.10},
		{"graph-heavy", 0.30, 0.20, 0.25, 0.15, 0.10},
		{"lexical-semantic", 0.15, 0.35, 0.10, 0.05, 0.35},
	}
	for _, ws := range weightSets {
		c := base
		c.WeightPPR = ws.ppr
		c.WeightBM25 = ws.bm25
		c.WeightBetweenness = ws.bet
		c.WeightInDegree = ws.ind
		c.WeightSemantic = ws.sem
		configs = append(configs, sweepConfig{"weights/" + ws.name, c})
	}

	// Phase B: Node-type boost variations (~9 configs)
	for _, rb := range []float64{1.5, 3.0, 4.0, 5.0} {
		c := base
		c.RouteBoost = rb
		configs = append(configs, sweepConfig{fmt.Sprintf("boost/route=%.1f", rb), c})
	}
	for _, cb := range []float64{1.0, 2.0} {
		c := base
		c.ClassBoost = cb
		configs = append(configs, sweepConfig{fmt.Sprintf("boost/class=%.1f", cb), c})
	}
	for _, mb := range []float64{1.0, 1.5, 2.0} {
		c := base
		c.RouteMethodBoost = mb
		configs = append(configs, sweepConfig{fmt.Sprintf("boost/routeMethod=%.1f", mb), c})
	}

	// Phase C: Graph expansion variations (~10 configs)
	for _, sc := range []int{3, 7, 10} {
		c := base
		c.ExpansionSeedCount = sc
		configs = append(configs, sweepConfig{fmt.Sprintf("expand/seeds=%d", sc), c})
	}
	for _, mn := range []int{10, 30, 50} {
		c := base
		c.ExpansionMaxNeighbors = mn
		configs = append(configs, sweepConfig{fmt.Sprintf("expand/neighbors=%d", mn), c})
	}
	for _, bonus := range []float64{0.05, 0.15, 0.20, 0.30} {
		c := base
		c.ExpansionBonus = bonus
		configs = append(configs, sweepConfig{fmt.Sprintf("expand/bonus=%.2f", bonus), c})
	}

	// Phase D: Pipeline params (~10 configs)
	for _, floor := range []float64{0.0, 0.02, 0.10, 0.15} {
		c := base
		c.BM25ScoreFloor = floor
		configs = append(configs, sweepConfig{fmt.Sprintf("pipeline/floor=%.2f", floor), c})
	}
	for _, mpf := range []int{1, 2, 5} {
		c := base
		c.MaxPerFile = mpf
		configs = append(configs, sweepConfig{fmt.Sprintf("pipeline/maxPerFile=%d", mpf), c})
	}
	for _, cm := range []int{3, 8, 10} {
		c := base
		c.CandidateMultiplier = cm
		configs = append(configs, sweepConfig{fmt.Sprintf("pipeline/candidates=%dx", cm), c})
	}

	// Phase E: Path penalty variations (~3 configs)
	{
		c := base
		c.PenaltyTest = 0.1
		c.PenaltyMigration = 0.1
		configs = append(configs, sweepConfig{"penalty/aggressive", c})
	}
	{
		c := base
		c.PenaltyTest = 0.5
		c.PenaltyMigration = 0.4
		configs = append(configs, sweepConfig{"penalty/lenient", c})
	}
	{
		c := base
		c.PenaltyConfig = 1.0
		configs = append(configs, sweepConfig{"penalty/noConfigPenalty", c})
	}

	return configs
}

// TestParameterSweepPhase2 runs a second sweep starting from MaxPerFile=1 as the
// new baseline (43/81 B+C) and varies other parameters on top of it.
// Requires SWEEP=1 env var and realrepo build tag.
func TestParameterSweepPhase2(t *testing.T) {
	if os.Getenv("SWEEP") == "" {
		t.Skip("set SWEEP=1 to run parameter sweep phase 2")
	}

	env := getSharedEnv(t)
	answerKey := parseAnswerKey(t)
	limits := map[string]int{"A": 20, "B": 40, "C": 40}

	configs := generatePhase2Configs()

	type result struct {
		idx    int
		name   string
		config search.SearchConfig
		score  benchmarkResult
	}

	var results []result
	baseline := 43 // MaxPerFile=1 result (Phase 1 winner)

	for i, sc := range configs {
		eng := search.NewWithConfig(env.store, env.embedder, env.graphEngine, sc.config)
		score := runBenchmarkWith(eng, answerKey, limits)
		results = append(results, result{
			idx: i, name: sc.name, config: sc.config, score: score,
		})

		marker := ""
		if score.BCHits > baseline {
			marker = fmt.Sprintf(" >>> +%d IMPROVEMENT", score.BCHits-baseline)
		}
		t.Logf("[%02d] %-45s B+C=%d/%d (%.1f%%)%s",
			i, sc.name, score.BCHits, score.BCTotal, score.BCRate, marker)
	}

	// Sort by B+C hits descending and print top 10
	sort.Slice(results, func(i, j int) bool {
		return results[i].score.BCHits > results[j].score.BCHits
	})

	t.Log("\n=== TOP 10 PHASE 2 CONFIGURATIONS ===")
	for i := 0; i < 10 && i < len(results); i++ {
		r := results[i]
		t.Logf("#%d: %-45s B+C=%d/%d (%.1f%%)", i+1, r.name, r.score.BCHits, r.score.BCTotal, r.score.BCRate)
		t.Logf("    Weights: PPR=%.2f BM25=%.2f Bet=%.2f InD=%.2f Sem=%.2f",
			r.config.WeightPPR, r.config.WeightBM25, r.config.WeightBetweenness,
			r.config.WeightInDegree, r.config.WeightSemantic)
		t.Logf("    Boosts: Route=%.1f Method=%.1f Class=%.1f Func=%.1f",
			r.config.RouteBoost, r.config.RouteMethodBoost, r.config.ClassBoost, r.config.FunctionBoost)
		t.Logf("    Expansion: Seeds=%d Neighbors=%d Bonus=%.2f",
			r.config.ExpansionSeedCount, r.config.ExpansionMaxNeighbors, r.config.ExpansionBonus)
		t.Logf("    Pipeline: MaxPerFile=%d Floor=%.2f Candidates=%dx",
			r.config.MaxPerFile, r.config.BM25ScoreFloor, r.config.CandidateMultiplier)
		// Print per-query breakdown for top config
		if i == 0 {
			t.Log("    Per-query:")
			for _, q := range r.score.Queries {
				t.Logf("      [%s] %d/%d %q", q.ID, q.Hits, q.Total, q.Query)
			}
		}
	}
}

// generatePhase2Configs builds parameter configurations starting from MaxPerFile=1
// (the Phase 1 winner) and varies other parameters on top of it.
func generatePhase2Configs() []sweepConfig {
	base := search.DefaultConfig()
	base.MaxPerFile = 1 // Phase 1 winner
	var configs []sweepConfig

	// Always include the Phase 1 winner as baseline
	configs = append(configs, sweepConfig{"p2/baseline/maxPerFile=1", base})

	// --- 1. Weight combinations (10 configs) ---
	weightSets := []struct {
		name                         string
		ppr, bm25, bet, ind, sem float64
	}{
		{"heavy-bm25", 0.15, 0.50, 0.10, 0.10, 0.15},
		{"heavy-ppr", 0.50, 0.20, 0.10, 0.10, 0.10},
		{"balanced", 0.25, 0.35, 0.15, 0.10, 0.15},
		{"bm25-focus", 0.20, 0.45, 0.10, 0.10, 0.15},
		{"ppr-focus", 0.45, 0.25, 0.10, 0.10, 0.10},
		{"no-semantic", 0.40, 0.30, 0.15, 0.15, 0.00},
		{"no-indegree", 0.35, 0.30, 0.20, 0.00, 0.15},
		{"bm25-ppr-only", 0.50, 0.50, 0.00, 0.00, 0.00},
		{"bm25-dominant", 0.10, 0.60, 0.10, 0.10, 0.10},
		{"graph-heavy", 0.30, 0.20, 0.25, 0.15, 0.10},
	}
	for _, ws := range weightSets {
		c := base
		c.WeightPPR = ws.ppr
		c.WeightBM25 = ws.bm25
		c.WeightBetweenness = ws.bet
		c.WeightInDegree = ws.ind
		c.WeightSemantic = ws.sem
		configs = append(configs, sweepConfig{"p2/weights/" + ws.name, c})
	}

	// --- 2. Boost combinations (6 configs) ---
	for _, rb := range []float64{3.0, 4.0, 5.0} {
		c := base
		c.RouteBoost = rb
		configs = append(configs, sweepConfig{fmt.Sprintf("p2/boost/route=%.1f", rb), c})
	}
	for _, cb := range []float64{1.0, 2.0} {
		c := base
		c.ClassBoost = cb
		configs = append(configs, sweepConfig{fmt.Sprintf("p2/boost/class=%.1f", cb), c})
	}
	{
		c := base
		c.RouteMethodBoost = 2.0
		configs = append(configs, sweepConfig{"p2/boost/routeMethod=2.0", c})
	}

	// --- 3. Expansion combinations (6 configs) ---
	for _, sc := range []int{3, 7, 10} {
		c := base
		c.ExpansionSeedCount = sc
		configs = append(configs, sweepConfig{fmt.Sprintf("p2/expand/seeds=%d", sc), c})
	}
	for _, mn := range []int{10, 30} {
		c := base
		c.ExpansionMaxNeighbors = mn
		configs = append(configs, sweepConfig{fmt.Sprintf("p2/expand/neighbors=%d", mn), c})
	}
	{
		c := base
		c.ExpansionBonus = 0.20
		configs = append(configs, sweepConfig{"p2/expand/bonus=0.20", c})
	}

	// --- 4. Pipeline combos (4 configs) ---
	for _, floor := range []float64{0.00, 0.10} {
		c := base
		c.BM25ScoreFloor = floor
		configs = append(configs, sweepConfig{fmt.Sprintf("p2/pipeline/floor=%.2f", floor), c})
	}
	for _, cm := range []int{3, 8} {
		c := base
		c.CandidateMultiplier = cm
		configs = append(configs, sweepConfig{fmt.Sprintf("p2/pipeline/candidates=%dx", cm), c})
	}

	// --- 5. Multi-param combos (8 configs) ---
	// no-semantic + route=4.0
	{
		c := base
		c.WeightPPR = 0.40
		c.WeightBM25 = 0.30
		c.WeightBetweenness = 0.15
		c.WeightInDegree = 0.15
		c.WeightSemantic = 0.00
		c.RouteBoost = 4.0
		configs = append(configs, sweepConfig{"p2/combo/no-semantic+route=4.0", c})
	}
	// no-indegree + seeds=10
	{
		c := base
		c.WeightPPR = 0.35
		c.WeightBM25 = 0.30
		c.WeightBetweenness = 0.20
		c.WeightInDegree = 0.00
		c.WeightSemantic = 0.15
		c.ExpansionSeedCount = 10
		configs = append(configs, sweepConfig{"p2/combo/no-indegree+seeds=10", c})
	}
	// heavy-bm25 + route=3.0
	{
		c := base
		c.WeightPPR = 0.15
		c.WeightBM25 = 0.50
		c.WeightBetweenness = 0.10
		c.WeightInDegree = 0.10
		c.WeightSemantic = 0.15
		c.RouteBoost = 3.0
		configs = append(configs, sweepConfig{"p2/combo/heavy-bm25+route=3.0", c})
	}
	// bm25-ppr-only + class=2.0
	{
		c := base
		c.WeightPPR = 0.50
		c.WeightBM25 = 0.50
		c.WeightBetweenness = 0.00
		c.WeightInDegree = 0.00
		c.WeightSemantic = 0.00
		c.ClassBoost = 2.0
		configs = append(configs, sweepConfig{"p2/combo/bm25-ppr-only+class=2.0", c})
	}
	// no-semantic + seeds=7 + bonus=0.20
	{
		c := base
		c.WeightPPR = 0.40
		c.WeightBM25 = 0.30
		c.WeightBetweenness = 0.15
		c.WeightInDegree = 0.15
		c.WeightSemantic = 0.00
		c.ExpansionSeedCount = 7
		c.ExpansionBonus = 0.20
		configs = append(configs, sweepConfig{"p2/combo/no-semantic+seeds=7+bonus=0.20", c})
	}
	// heavy-ppr + route=5.0 + seeds=10
	{
		c := base
		c.WeightPPR = 0.50
		c.WeightBM25 = 0.20
		c.WeightBetweenness = 0.10
		c.WeightInDegree = 0.10
		c.WeightSemantic = 0.10
		c.RouteBoost = 5.0
		c.ExpansionSeedCount = 10
		configs = append(configs, sweepConfig{"p2/combo/heavy-ppr+route=5.0+seeds=10", c})
	}
	// balanced + floor=0.10 + candidates=8x
	{
		c := base
		c.WeightPPR = 0.25
		c.WeightBM25 = 0.35
		c.WeightBetweenness = 0.15
		c.WeightInDegree = 0.10
		c.WeightSemantic = 0.15
		c.BM25ScoreFloor = 0.10
		c.CandidateMultiplier = 8
		configs = append(configs, sweepConfig{"p2/combo/balanced+floor=0.10+cand=8x", c})
	}
	// bm25-focus + class=1.0 + bonus=0.15
	{
		c := base
		c.WeightPPR = 0.20
		c.WeightBM25 = 0.45
		c.WeightBetweenness = 0.10
		c.WeightInDegree = 0.10
		c.WeightSemantic = 0.15
		c.ClassBoost = 1.0
		c.ExpansionBonus = 0.15
		configs = append(configs, sweepConfig{"p2/combo/bm25-focus+class=1.0+bonus=0.15", c})
	}

	return configs
}

// TestParameterSweepPhase3 fine-tunes around the Phase 2 winner:
// MaxPerFile=1, InDegree=0, Seeds=10, BM25=0.30, Betweenness=0.20
func TestParameterSweepPhase3(t *testing.T) {
	if os.Getenv("SWEEP") == "" {
		t.Skip("set SWEEP=1 to run parameter sweep phase 3")
	}

	env := getSharedEnv(t)
	answerKey := parseAnswerKey(t)
	limits := map[string]int{"A": 20, "B": 40, "C": 40}

	configs := generatePhase3Configs()

	type result struct {
		idx    int
		name   string
		config search.SearchConfig
		score  benchmarkResult
	}

	var results []result
	baseline := 45 // Phase 2 winner

	for i, sc := range configs {
		eng := search.NewWithConfig(env.store, env.embedder, env.graphEngine, sc.config)
		score := runBenchmarkWith(eng, answerKey, limits)
		results = append(results, result{
			idx: i, name: sc.name, config: sc.config, score: score,
		})

		marker := ""
		if score.BCHits > baseline {
			marker = fmt.Sprintf(" >>> +%d IMPROVEMENT", score.BCHits-baseline)
		}
		t.Logf("[%02d] %-50s B+C=%d/%d (%.1f%%)%s",
			i, sc.name, score.BCHits, score.BCTotal, score.BCRate, marker)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score.BCHits > results[j].score.BCHits
	})

	t.Log("\n=== TOP 10 PHASE 3 CONFIGURATIONS ===")
	for i := 0; i < 10 && i < len(results); i++ {
		r := results[i]
		t.Logf("#%d: %-50s B+C=%d/%d (%.1f%%)", i+1, r.name, r.score.BCHits, r.score.BCTotal, r.score.BCRate)
		t.Logf("    Weights: PPR=%.2f BM25=%.2f Bet=%.2f InD=%.2f Sem=%.2f",
			r.config.WeightPPR, r.config.WeightBM25, r.config.WeightBetweenness,
			r.config.WeightInDegree, r.config.WeightSemantic)
		t.Logf("    Boosts: Route=%.1f Method=%.1f Class=%.1f Func=%.1f",
			r.config.RouteBoost, r.config.RouteMethodBoost, r.config.ClassBoost, r.config.FunctionBoost)
		t.Logf("    Expansion: Seeds=%d Neighbors=%d Bonus=%.2f",
			r.config.ExpansionSeedCount, r.config.ExpansionMaxNeighbors, r.config.ExpansionBonus)
		t.Logf("    Pipeline: MaxPerFile=%d Floor=%.2f Candidates=%dx",
			r.config.MaxPerFile, r.config.BM25ScoreFloor, r.config.CandidateMultiplier)
		if i == 0 {
			t.Log("    Per-query:")
			for _, q := range r.score.Queries {
				t.Logf("      [%s] %d/%d %q", q.ID, q.Hits, q.Total, q.Query)
			}
		}
	}
}

// generatePhase3Configs fine-tunes around the Phase 2 winner config.
func generatePhase3Configs() []sweepConfig {
	// Phase 2 winner: MaxPerFile=1, InDegree=0, Seeds=10, BM25=0.30, Bet=0.20
	winner := search.DefaultConfig()
	winner.MaxPerFile = 1
	winner.WeightInDegree = 0.00
	winner.WeightBM25 = 0.30
	winner.WeightBetweenness = 0.20
	winner.ExpansionSeedCount = 10

	var configs []sweepConfig
	configs = append(configs, sweepConfig{"p3/baseline/phase2-winner", winner})

	// Fine-tune PPR weight (redistribute from PPR)
	for _, ppr := range []float64{0.25, 0.30, 0.40, 0.45} {
		c := winner
		c.WeightPPR = ppr
		c.WeightBM25 = 1.0 - ppr - c.WeightBetweenness - c.WeightInDegree - c.WeightSemantic
		configs = append(configs, sweepConfig{fmt.Sprintf("p3/weight/ppr=%.2f+bm25=%.2f", ppr, c.WeightBM25), c})
	}

	// Fine-tune BM25 weight (redistribute from Bet)
	for _, bm25 := range []float64{0.25, 0.35, 0.40} {
		c := winner
		c.WeightBM25 = bm25
		c.WeightBetweenness = 1.0 - c.WeightPPR - bm25 - c.WeightInDegree - c.WeightSemantic
		configs = append(configs, sweepConfig{fmt.Sprintf("p3/weight/bm25=%.2f+bet=%.2f", bm25, c.WeightBetweenness), c})
	}

	// Fine-tune semantic (redistribute from Bet)
	for _, sem := range []float64{0.00, 0.05, 0.10, 0.20, 0.25} {
		c := winner
		c.WeightSemantic = sem
		c.WeightBetweenness = 1.0 - c.WeightPPR - c.WeightBM25 - c.WeightInDegree - sem
		configs = append(configs, sweepConfig{fmt.Sprintf("p3/weight/sem=%.2f+bet=%.2f", sem, c.WeightBetweenness), c})
	}

	// Fine-tune small InDegree
	for _, ind := range []float64{0.03, 0.05, 0.08} {
		c := winner
		c.WeightInDegree = ind
		c.WeightBetweenness = 1.0 - c.WeightPPR - c.WeightBM25 - ind - c.WeightSemantic
		configs = append(configs, sweepConfig{fmt.Sprintf("p3/weight/ind=%.2f+bet=%.2f", ind, c.WeightBetweenness), c})
	}

	// Fine-tune seeds around 10
	for _, seeds := range []int{8, 12, 15, 20} {
		c := winner
		c.ExpansionSeedCount = seeds
		configs = append(configs, sweepConfig{fmt.Sprintf("p3/expand/seeds=%d", seeds), c})
	}

	// Fine-tune neighbors around 20
	for _, n := range []int{15, 25, 30, 40} {
		c := winner
		c.ExpansionMaxNeighbors = n
		configs = append(configs, sweepConfig{fmt.Sprintf("p3/expand/neighbors=%d", n), c})
	}

	// Fine-tune expansion bonus
	for _, b := range []float64{0.05, 0.15, 0.20, 0.25} {
		c := winner
		c.ExpansionBonus = b
		configs = append(configs, sweepConfig{fmt.Sprintf("p3/expand/bonus=%.2f", b), c})
	}

	// Route boost variations
	for _, rb := range []float64{2.0, 3.0, 3.5} {
		c := winner
		c.RouteBoost = rb
		configs = append(configs, sweepConfig{fmt.Sprintf("p3/boost/route=%.1f", rb), c})
	}

	// Candidate multiplier
	for _, cm := range []int{3, 7, 10} {
		c := winner
		c.CandidateMultiplier = cm
		configs = append(configs, sweepConfig{fmt.Sprintf("p3/pipeline/candidates=%dx", cm), c})
	}

	// BM25 floor
	for _, f := range []float64{0.00, 0.02, 0.08, 0.10, 0.15} {
		c := winner
		c.BM25ScoreFloor = f
		configs = append(configs, sweepConfig{fmt.Sprintf("p3/pipeline/floor=%.2f", f), c})
	}

	// Multi-param combos
	{
		c := winner
		c.WeightPPR = 0.40
		c.WeightBM25 = 0.25
		c.ExpansionSeedCount = 12
		configs = append(configs, sweepConfig{"p3/combo/ppr=0.40+seeds=12", c})
	}
	{
		c := winner
		c.WeightSemantic = 0.00
		c.WeightBetweenness = 0.35
		c.ExpansionSeedCount = 12
		configs = append(configs, sweepConfig{"p3/combo/no-sem+bet=0.35+seeds=12", c})
	}
	{
		c := winner
		c.WeightPPR = 0.40
		c.WeightBM25 = 0.35
		c.WeightBetweenness = 0.10
		c.WeightSemantic = 0.15
		configs = append(configs, sweepConfig{"p3/combo/ppr=0.40+bm25=0.35+bet=0.10", c})
	}
	{
		c := winner
		c.RouteBoost = 3.0
		c.ExpansionSeedCount = 12
		c.ExpansionBonus = 0.15
		configs = append(configs, sweepConfig{"p3/combo/route=3.0+seeds=12+bonus=0.15", c})
	}
	{
		c := winner
		c.WeightPPR = 0.30
		c.WeightBM25 = 0.35
		c.WeightBetweenness = 0.20
		c.WeightSemantic = 0.15
		c.ExpansionSeedCount = 12
		configs = append(configs, sweepConfig{"p3/combo/bm25=0.35+seeds=12", c})
	}
	{
		c := winner
		c.CandidateMultiplier = 7
		c.ExpansionSeedCount = 12
		c.RouteBoost = 3.0
		configs = append(configs, sweepConfig{"p3/combo/cand=7x+seeds=12+route=3.0", c})
	}

	return configs
}

// TestParameterSweepPhase4 combines top findings from Phase 3:
// floor=0.00, neighbors=40, ppr-shifts, candidates=7-10x, no-semantic
func TestParameterSweepPhase4(t *testing.T) {
	if os.Getenv("SWEEP") == "" {
		t.Skip("set SWEEP=1 to run parameter sweep phase 4")
	}

	env := getSharedEnv(t)
	answerKey := parseAnswerKey(t)
	limits := map[string]int{"A": 20, "B": 40, "C": 40}

	configs := generatePhase4Configs()

	type result struct {
		idx    int
		name   string
		config search.SearchConfig
		score  benchmarkResult
	}

	var results []result
	baseline := 46

	for i, sc := range configs {
		eng := search.NewWithConfig(env.store, env.embedder, env.graphEngine, sc.config)
		score := runBenchmarkWith(eng, answerKey, limits)
		results = append(results, result{
			idx: i, name: sc.name, config: sc.config, score: score,
		})

		marker := ""
		if score.BCHits > baseline {
			marker = fmt.Sprintf(" >>> +%d IMPROVEMENT", score.BCHits-baseline)
		}
		t.Logf("[%02d] %-55s B+C=%d/%d (%.1f%%)%s",
			i, sc.name, score.BCHits, score.BCTotal, score.BCRate, marker)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score.BCHits > results[j].score.BCHits
	})

	t.Log("\n=== TOP 10 PHASE 4 CONFIGURATIONS ===")
	for i := 0; i < 10 && i < len(results); i++ {
		r := results[i]
		t.Logf("#%d: %-55s B+C=%d/%d (%.1f%%)", i+1, r.name, r.score.BCHits, r.score.BCTotal, r.score.BCRate)
		t.Logf("    Weights: PPR=%.2f BM25=%.2f Bet=%.2f InD=%.2f Sem=%.2f",
			r.config.WeightPPR, r.config.WeightBM25, r.config.WeightBetweenness,
			r.config.WeightInDegree, r.config.WeightSemantic)
		t.Logf("    Boosts: Route=%.1f Method=%.1f Class=%.1f Func=%.1f",
			r.config.RouteBoost, r.config.RouteMethodBoost, r.config.ClassBoost, r.config.FunctionBoost)
		t.Logf("    Expansion: Seeds=%d Neighbors=%d Bonus=%.2f",
			r.config.ExpansionSeedCount, r.config.ExpansionMaxNeighbors, r.config.ExpansionBonus)
		t.Logf("    Pipeline: MaxPerFile=%d Floor=%.2f Candidates=%dx",
			r.config.MaxPerFile, r.config.BM25ScoreFloor, r.config.CandidateMultiplier)
		if i == 0 {
			t.Log("    Per-query:")
			for _, q := range r.score.Queries {
				t.Logf("      [%s] %d/%d %q", q.ID, q.Hits, q.Total, q.Query)
			}
		}
	}
}

func generatePhase4Configs() []sweepConfig {
	// Phase 3 winner: MaxPerFile=1, InDegree=0, Seeds=10, BM25=0.30, Bet=0.20, Floor=0.00
	best := search.DefaultConfig()
	best.MaxPerFile = 1
	best.WeightInDegree = 0.00
	best.WeightBM25 = 0.30
	best.WeightBetweenness = 0.20
	best.ExpansionSeedCount = 10
	best.BM25ScoreFloor = 0.00

	var configs []sweepConfig
	configs = append(configs, sweepConfig{"p4/baseline/phase3-winner", best})

	// Combine floor=0 with the other Phase 3 near-winners
	// neighbors=40
	{
		c := best
		c.ExpansionMaxNeighbors = 40
		configs = append(configs, sweepConfig{"p4/floor0+neighbors=40", c})
	}
	// ppr=0.30 bm25=0.35
	{
		c := best
		c.WeightPPR = 0.30
		c.WeightBM25 = 0.35
		configs = append(configs, sweepConfig{"p4/floor0+ppr=0.30+bm25=0.35", c})
	}
	// ppr=0.40 bm25=0.25
	{
		c := best
		c.WeightPPR = 0.40
		c.WeightBM25 = 0.25
		configs = append(configs, sweepConfig{"p4/floor0+ppr=0.40+bm25=0.25", c})
	}
	// candidates=7x
	{
		c := best
		c.CandidateMultiplier = 7
		configs = append(configs, sweepConfig{"p4/floor0+candidates=7x", c})
	}
	// candidates=10x
	{
		c := best
		c.CandidateMultiplier = 10
		configs = append(configs, sweepConfig{"p4/floor0+candidates=10x", c})
	}
	// no-semantic + bet=0.35
	{
		c := best
		c.WeightSemantic = 0.00
		c.WeightBetweenness = 0.35
		configs = append(configs, sweepConfig{"p4/floor0+no-sem+bet=0.35", c})
	}
	// floor0 + neighbors=40 + candidates=7x
	{
		c := best
		c.ExpansionMaxNeighbors = 40
		c.CandidateMultiplier = 7
		configs = append(configs, sweepConfig{"p4/floor0+neighbors=40+cand=7x", c})
	}
	// floor0 + ppr=0.30 + bm25=0.35 + neighbors=40
	{
		c := best
		c.WeightPPR = 0.30
		c.WeightBM25 = 0.35
		c.ExpansionMaxNeighbors = 40
		configs = append(configs, sweepConfig{"p4/floor0+ppr=0.30+bm25=0.35+n=40", c})
	}
	// floor0 + ppr=0.40 + neighbors=40
	{
		c := best
		c.WeightPPR = 0.40
		c.WeightBM25 = 0.25
		c.ExpansionMaxNeighbors = 40
		configs = append(configs, sweepConfig{"p4/floor0+ppr=0.40+n=40", c})
	}
	// floor0 + no-sem + neighbors=40
	{
		c := best
		c.WeightSemantic = 0.00
		c.WeightBetweenness = 0.35
		c.ExpansionMaxNeighbors = 40
		configs = append(configs, sweepConfig{"p4/floor0+no-sem+n=40", c})
	}
	// floor0 + candidates=7x + ppr=0.30 + bm25=0.35
	{
		c := best
		c.CandidateMultiplier = 7
		c.WeightPPR = 0.30
		c.WeightBM25 = 0.35
		configs = append(configs, sweepConfig{"p4/floor0+cand=7x+ppr=0.30+bm25=0.35", c})
	}
	// floor0 + neighbors=40 + no-sem + candidates=7x
	{
		c := best
		c.ExpansionMaxNeighbors = 40
		c.WeightSemantic = 0.00
		c.WeightBetweenness = 0.35
		c.CandidateMultiplier = 7
		configs = append(configs, sweepConfig{"p4/floor0+n=40+no-sem+cand=7x", c})
	}
	// floor0 + seeds=15
	{
		c := best
		c.ExpansionSeedCount = 15
		configs = append(configs, sweepConfig{"p4/floor0+seeds=15", c})
	}
	// floor0 + seeds=15 + neighbors=40
	{
		c := best
		c.ExpansionSeedCount = 15
		c.ExpansionMaxNeighbors = 40
		configs = append(configs, sweepConfig{"p4/floor0+seeds=15+n=40", c})
	}
	// floor0 + route=3.5
	{
		c := best
		c.RouteBoost = 3.5
		configs = append(configs, sweepConfig{"p4/floor0+route=3.5", c})
	}
	// floor0 + bonus=0.05
	{
		c := best
		c.ExpansionBonus = 0.05
		configs = append(configs, sweepConfig{"p4/floor0+bonus=0.05", c})
	}
	// floor0 + ind=0.08 + bet=0.12
	{
		c := best
		c.WeightInDegree = 0.08
		c.WeightBetweenness = 0.12
		configs = append(configs, sweepConfig{"p4/floor0+ind=0.08+bet=0.12", c})
	}
	// The kitchen sink: floor=0 + n=40 + cand=7x + ppr=0.30 + bm25=0.35
	{
		c := best
		c.ExpansionMaxNeighbors = 40
		c.CandidateMultiplier = 7
		c.WeightPPR = 0.30
		c.WeightBM25 = 0.35
		configs = append(configs, sweepConfig{"p4/kitchen-sink/n=40+cand=7x+ppr=0.30", c})
	}
	// kitchen sink + no-sem
	{
		c := best
		c.ExpansionMaxNeighbors = 40
		c.CandidateMultiplier = 7
		c.WeightSemantic = 0.00
		c.WeightBetweenness = 0.35
		configs = append(configs, sweepConfig{"p4/kitchen-sink/n=40+cand=7x+no-sem", c})
	}

	return configs
}
