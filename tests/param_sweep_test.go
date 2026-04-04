//go:build fts5 && realrepo

package tests

import (
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/naman/qb-context/internal/search"
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
	baseline := 39 // current B+C hits

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
