package types

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
)

// NodeType represents the type of an AST node
type NodeType uint8

const (
	NodeTypeFunction  NodeType = iota + 1
	NodeTypeClass
	NodeTypeStruct
	NodeTypeMethod
	NodeTypeInterface // H22: distinct type for interfaces
	NodeTypeFile      // C1: file-level node for import edge anchoring
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
	case NodeTypeInterface:
		return "interface"
	case NodeTypeFile:
		return "file"
	default:
		return "unknown"
	}
}

// MarshalJSON encodes a NodeType as its string name (H46).
func (nt NodeType) MarshalJSON() ([]byte, error) {
	return json.Marshal(nt.String())
}

// UnmarshalJSON decodes a NodeType from its string name or numeric value (H46).
func (nt *NodeType) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		switch s {
		case "function":
			*nt = NodeTypeFunction
		case "class":
			*nt = NodeTypeClass
		case "struct":
			*nt = NodeTypeStruct
		case "method":
			*nt = NodeTypeMethod
		case "interface":
			*nt = NodeTypeInterface
		case "file":
			*nt = NodeTypeFile
		case "unknown":
			*nt = 0
		default:
			return fmt.Errorf("unknown NodeType: %q", s)
		}
		return nil
	}
	// Numeric fallback for backwards compatibility
	var n uint8
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("cannot unmarshal NodeType from %s", string(data))
	}
	// M54: Validate numeric value is within the valid range
	candidate := NodeType(n)
	if candidate.String() == "unknown" && n != 0 {
		return fmt.Errorf("invalid NodeType numeric value: %d (valid range: %d–%d)", n, NodeTypeFunction, NodeTypeFile)
	}
	*nt = candidate
	return nil
}

// nodeTypeFromString parses a string into a NodeType (used internally).
func nodeTypeFromString(s string) (NodeType, error) {
	var nt NodeType
	err := nt.UnmarshalJSON([]byte(strconv.Quote(s)))
	return nt, err
}

// EdgeType represents the type of relationship between AST nodes
type EdgeType uint8

const (
	EdgeTypeCalls EdgeType = iota + 1
	EdgeTypeImports
	EdgeTypeImplements
	EdgeTypeInstantiates
	EdgeTypeDefines       // file → class/function (containment)
	EdgeTypeDefinesMethod // class → method (containment)
	EdgeTypeInherits      // class → parent class (extends)
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
	case EdgeTypeDefines:
		return "defines"
	case EdgeTypeDefinesMethod:
		return "defines_method"
	case EdgeTypeInherits:
		return "inherits"
	default:
		return "unknown"
	}
}

// MarshalJSON encodes an EdgeType as its string name (H46).
func (et EdgeType) MarshalJSON() ([]byte, error) {
	return json.Marshal(et.String())
}

// UnmarshalJSON decodes an EdgeType from its string name or numeric value (H46).
func (et *EdgeType) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		switch s {
		case "calls":
			*et = EdgeTypeCalls
		case "imports":
			*et = EdgeTypeImports
		case "implements":
			*et = EdgeTypeImplements
		case "instantiates":
			*et = EdgeTypeInstantiates
		case "defines":
			*et = EdgeTypeDefines
		case "defines_method":
			*et = EdgeTypeDefinesMethod
		case "inherits":
			*et = EdgeTypeInherits
		case "unknown":
			*et = 0
		default:
			return fmt.Errorf("unknown EdgeType: %q", s)
		}
		return nil
	}
	// Numeric fallback for backwards compatibility
	var n uint8
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("cannot unmarshal EdgeType from %s", string(data))
	}
	// M54: Validate numeric value is within the valid range
	candidate := EdgeType(n)
	if candidate.String() == "unknown" && n != 0 {
		return fmt.Errorf("invalid EdgeType numeric value: %d (valid range: %d–%d)", n, EdgeTypeCalls, EdgeTypeInherits)
	}
	*et = candidate
	return nil
}

// edgeTypeFromString parses a string into an EdgeType (used internally).
func edgeTypeFromString(s string) (EdgeType, error) {
	var et EdgeType
	err := et.UnmarshalJSON([]byte(strconv.Quote(s)))
	return et, err
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
	SourceID     string   `json:"source_id"`
	TargetID     string   `json:"target_id"`
	EdgeType     EdgeType `json:"edge_type"`
	TargetSymbol string   `json:"target_symbol,omitempty"` // raw symbol name for cross-file resolution
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

// GenerateNodeID creates a deterministic SHA-256 hash ID from file path and symbol name.
// Uses a null byte separator to prevent collisions (e.g., "a:b"+"c" vs "a"+":b:c").
func GenerateNodeID(filePath, symbolName string) string {
	h := sha256.New()
	h.Write([]byte(filePath))
	h.Write([]byte{0}) // null byte separator prevents collision
	h.Write([]byte(symbolName))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// SearchResult represents a ranked result from hybrid search
type SearchResult struct {
	Node      ASTNode        `json:"node"`
	Score     float64        `json:"score"`
	Breakdown ScoreBreakdown `json:"breakdown,omitempty"`
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

// ScoreBreakdown holds the per-signal normalized scores that compose the final composite score.
type ScoreBreakdown struct {
	PPR         float64 `json:"ppr"`
	BM25        float64 `json:"bm25"`
	Betweenness float64 `json:"betweenness"`
	InDegree    float64 `json:"in_degree"`
	Semantic    float64 `json:"semantic"`
}

// Inspectable is a ranked discovery result returned by all discovery tools.
type Inspectable struct {
	Rank       int               `json:"rank"`
	TargetType string            `json:"target_type"`
	Name       string            `json:"name"`
	FilePath   string            `json:"file_path"`
	ID         string            `json:"id,omitempty"`
	Score      float64           `json:"score"`
	Reason     string            `json:"reason"`
	WhyNow     string            `json:"why_now,omitempty"`
	NextTool   string            `json:"next_tool"`
	NextArgs   map[string]string `json:"next_args,omitempty"`
}

// InspectableResponse wraps discovery tool output with metadata.
type InspectableResponse struct {
	Inspectables []Inspectable `json:"inspectables"`
	Total        int           `json:"total"`
	Query        string        `json:"query,omitempty"`
	Summary      string        `json:"summary"`
}
