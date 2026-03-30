package types

import (
	"crypto/sha256"
	"fmt"
)

// NodeType represents the type of an AST node
type NodeType uint8

const (
	NodeTypeFunction NodeType = iota + 1
	NodeTypeClass
	NodeTypeStruct
	NodeTypeMethod
)

// String returns the string representation of a NodeType
func (nt NodeType) String() string {
	switch nt {
	case NodeTypeFunction:
		return "function"
	case NodeTypeClass:
		return "class"
	case NodeTypeStruct:
		return "struct"
	case NodeTypeMethod:
		return "method"
	default:
		return "unknown"
	}
}

// EdgeType represents the type of relationship between AST nodes
type EdgeType uint8

const (
	EdgeTypeCalls EdgeType = iota + 1
	EdgeTypeImports
	EdgeTypeImplements
	EdgeTypeInstantiates
)

// String returns the string representation of an EdgeType
func (et EdgeType) String() string {
	switch et {
	case EdgeTypeCalls:
		return "calls"
	case EdgeTypeImports:
		return "imports"
	case EdgeTypeImplements:
		return "implements"
	case EdgeTypeInstantiates:
		return "instantiates"
	default:
		return "unknown"
	}
}

// ASTNode represents a parsed code symbol (function, class, struct, method)
type ASTNode struct {
	ID         string   `json:"id"`
	FilePath   string   `json:"file_path"`
	SymbolName string   `json:"symbol_name"`
	NodeType   NodeType `json:"node_type"`
	StartByte  uint32   `json:"start_byte"`
	EndByte    uint32   `json:"end_byte"`
	ContentSum string   `json:"content_sum"`
}

// ASTEdge represents a relationship between two AST nodes
type ASTEdge struct {
	SourceID string   `json:"source_id"`
	TargetID string   `json:"target_id"`
	EdgeType EdgeType `json:"edge_type"`
}

// FileEvent represents a filesystem change event
type FileEventAction uint8

const (
	FileEventCreated FileEventAction = iota + 1
	FileEventModified
	FileEventDeleted
)

type FileEvent struct {
	Path   string
	Action FileEventAction
}

// GenerateNodeID creates a deterministic SHA-256 hash ID from file path and symbol name
func GenerateNodeID(filePath, symbolName string) string {
	h := sha256.New()
	h.Write([]byte(filePath + ":" + symbolName))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// SearchResult represents a ranked result from hybrid search
type SearchResult struct {
	Node  ASTNode `json:"node"`
	Score float64 `json:"score"`
}

// RiskLevel represents the severity of impact from a change
type RiskLevel string

const (
	RiskCritical RiskLevel = "CRITICAL"
	RiskHigh     RiskLevel = "HIGH"
	RiskMedium   RiskLevel = "MEDIUM"
	RiskLow      RiskLevel = "LOW"
)

// NodeScore holds precomputed graph metrics for a node
type NodeScore struct {
	NodeID      string  `json:"node_id"`
	PageRank    float64 `json:"pagerank"`
	Betweenness float64 `json:"betweenness"`
}

// Community represents a group of tightly coupled code symbols detected by Louvain
type Community struct {
	ID      int      `json:"id"`
	NodeIDs []string `json:"node_ids"`
}

// ProjectSummary represents an architecture decision record or project summary
type ProjectSummary struct {
	Project    string `json:"project"`
	Summary    string `json:"summary"`
	SourceHash string `json:"source_hash"`
}
