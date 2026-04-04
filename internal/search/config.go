package search

// SearchConfig holds all tunable parameters for the hybrid search pipeline.
type SearchConfig struct {
	// Composite scoring weights (should sum to ~1.0)
	WeightPPR         float64
	WeightBM25        float64
	WeightBetweenness float64
	WeightInDegree    float64
	WeightSemantic    float64

	// Node-type boost multipliers (applied when query intent matches node type)
	RouteBoost       float64 // route query + NodeTypeRoute
	RouteMethodBoost float64 // route query + NodeTypeMethod
	ClassBoost       float64 // class query + NodeTypeClass/Interface
	FunctionBoost    float64 // function query + NodeTypeFunction

	// Path penalty multipliers (lower = more penalty)
	PenaltyGenerated float64 // _ide_helper, .d.ts, generated/
	PenaltyVendor    float64 // vendor/, node_modules/, lib/
	PenaltyMigration float64 // migrations/, migration/
	PenaltyTest      float64 // tests/, test/, *_test.go, etc.
	PenaltyExample   float64 // examples/
	PenaltyConfig    float64 // config/

	// Graph expansion parameters
	ExpansionSeedCount       int     // top-N results to expand from
	ExpansionMaxNeighbors    int     // max neighbors to consider per expansion
	ExpansionMaxAddedDivisor int     // maxAdded = limit / this value
	ExpansionBonus           float64 // fraction of seed score for connectivity bonus

	// Search pipeline parameters
	BM25ScoreFloor      float64 // min normalized BM25 for nodes with positive raw score
	CandidateMultiplier int     // candidateLimit = limit * this
	CandidateMinimum    int     // minimum candidateLimit
	MaxPerFile          int     // default max results per file_path
}

// DefaultConfig returns the current production configuration.
func DefaultConfig() SearchConfig {
	return SearchConfig{
		WeightPPR:         0.35,
		WeightBM25:        0.30,
		WeightBetweenness: 0.20,
		WeightInDegree:    0.00,
		WeightSemantic:    0.15,

		RouteBoost:       2.5,
		RouteMethodBoost: 1.3,
		ClassBoost:       1.5,
		FunctionBoost:    1.2,

		PenaltyGenerated: 0.3,
		PenaltyVendor:    0.3,
		PenaltyMigration: 0.2,
		PenaltyTest:      0.3,
		PenaltyExample:   0.6,
		PenaltyConfig:    0.8,

		ExpansionSeedCount:       10,
		ExpansionMaxNeighbors:    20,
		ExpansionMaxAddedDivisor: 4,
		ExpansionBonus:           0.10,

		BM25ScoreFloor:      0.00,
		CandidateMultiplier: 5,
		CandidateMinimum:    100,
		MaxPerFile:          1,
	}
}
