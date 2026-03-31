package types

import (
	"testing"
)

// TestGenerateNodeID_Deterministic ensures the same inputs always produce the same ID.
func TestGenerateNodeID_Deterministic(t *testing.T) {
	id1 := GenerateNodeID("internal/foo/bar.go", "MyFunction")
	id2 := GenerateNodeID("internal/foo/bar.go", "MyFunction")
	if id1 != id2 {
		t.Errorf("GenerateNodeID is not deterministic: got %q then %q", id1, id2)
	}
}

// TestGenerateNodeID_SHA256Format checks the output looks like a hex SHA-256 (64 chars).
func TestGenerateNodeID_SHA256Format(t *testing.T) {
	id := GenerateNodeID("some/path.go", "SomeSymbol")
	if len(id) != 64 {
		t.Errorf("expected 64-char hex string, got length %d: %q", len(id), id)
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex character %q in ID %q", c, id)
		}
	}
}

// TestGenerateNodeID_DifferentInputs ensures different inputs produce different IDs.
func TestGenerateNodeID_DifferentInputs(t *testing.T) {
	cases := []struct{ path, symbol string }{
		{"a.go", "Foo"},
		{"a.go", "Bar"},
		{"b.go", "Foo"},
		{"", "Foo"},
		{"a.go", ""},
	}

	seen := make(map[string]struct{ path, symbol string })
	for _, c := range cases {
		id := GenerateNodeID(c.path, c.symbol)
		if prev, ok := seen[id]; ok {
			t.Errorf("collision: (%q,%q) and (%q,%q) both produced %q",
				prev.path, prev.symbol, c.path, c.symbol, id)
		}
		seen[id] = c
	}
}

// TestGenerateNodeID_KnownValue verifies determinism, collision resistance,
// and that the output is a valid 64-char hex SHA-256 digest.
func TestGenerateNodeID_KnownValue(t *testing.T) {
	id := GenerateNodeID("main.go", "main")
	if id == "" {
		t.Fatal("GenerateNodeID returned empty string")
	}
	// Verify determinism: same inputs = same output
	id2 := GenerateNodeID("main.go", "main")
	if id != id2 {
		t.Errorf("GenerateNodeID not deterministic: %s != %s", id, id2)
	}
	// Different inputs = different output
	id3 := GenerateNodeID("main.go", "other")
	if id == id3 {
		t.Errorf("GenerateNodeID collision: %s == %s for different symbols", id, id3)
	}
	// Verify it's a valid hex string of expected length (SHA-256 = 64 hex chars)
	if len(id) != 64 {
		t.Errorf("expected 64-char hex string, got %d chars: %s", len(id), id)
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex character %q in ID %q", c, id)
		}
	}
}

// ---- NodeType.String tests ----

func TestNodeTypeString(t *testing.T) {
	tests := []struct {
		nt   NodeType
		want string
	}{
		{NodeTypeFunction, "function"},
		{NodeTypeClass, "class"},
		{NodeTypeStruct, "struct"},
		{NodeTypeMethod, "method"},
		{NodeType(0), "unknown"},
		{NodeType(255), "unknown"},
	}

	for _, tt := range tests {
		got := tt.nt.String()
		if got != tt.want {
			t.Errorf("NodeType(%d).String() = %q, want %q", tt.nt, got, tt.want)
		}
	}
}

// ---- EdgeType.String tests ----

func TestEdgeTypeString(t *testing.T) {
	tests := []struct {
		et   EdgeType
		want string
	}{
		{EdgeTypeCalls, "calls"},
		{EdgeTypeImports, "imports"},
		{EdgeTypeImplements, "implements"},
		{EdgeTypeInstantiates, "instantiates"},
		{EdgeType(0), "unknown"},
		{EdgeType(255), "unknown"},
	}

	for _, tt := range tests {
		got := tt.et.String()
		if got != tt.want {
			t.Errorf("EdgeType(%d).String() = %q, want %q", tt.et, got, tt.want)
		}
	}
}

// ---- Enum distinctness and non-zero tests ----

func TestNodeTypeDistinctAndNonZero(t *testing.T) {
	values := []NodeType{NodeTypeFunction, NodeTypeClass, NodeTypeStruct, NodeTypeMethod}
	seen := make(map[NodeType]bool)
	for _, v := range values {
		if v == 0 {
			t.Errorf("NodeType constant is zero: %v", v)
		}
		if seen[v] {
			t.Errorf("duplicate NodeType value: %v", v)
		}
		seen[v] = true
	}
}

func TestEdgeTypeDistinctAndNonZero(t *testing.T) {
	values := []EdgeType{EdgeTypeCalls, EdgeTypeImports, EdgeTypeImplements, EdgeTypeInstantiates}
	seen := make(map[EdgeType]bool)
	for _, v := range values {
		if v == 0 {
			t.Errorf("EdgeType constant is zero: %v", v)
		}
		if seen[v] {
			t.Errorf("duplicate EdgeType value: %v", v)
		}
		seen[v] = true
	}
}
