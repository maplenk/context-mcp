package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/naman/qb-context/internal/types"
)

// writeFile creates a temporary file with the given content and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing temp file %q: %v", path, err)
	}
	return path
}

// hasNodeType returns true if any node in the slice has the given NodeType.
func hasNodeType(nodes []types.ASTNode, nt types.NodeType) bool {
	for _, n := range nodes {
		if n.NodeType == nt {
			return true
		}
	}
	return false
}

// findNodeBySymbol returns the first node whose SymbolName equals name, or nil.
func findNodeBySymbol(nodes []types.ASTNode, name string) *types.ASTNode {
	for i, n := range nodes {
		if n.SymbolName == name {
			return &nodes[i]
		}
	}
	return nil
}

// ---- IsSupported ----

func TestIsSupported_SupportedExtensions(t *testing.T) {
	supported := []string{
		"main.go",
		"app.js",
		"component.jsx",
		"service.ts",
		"page.tsx",
		"index.php",
	}
	for _, f := range supported {
		if !IsSupported(f) {
			t.Errorf("IsSupported(%q) = false, want true", f)
		}
	}
}

func TestIsSupported_UnsupportedExtensions(t *testing.T) {
	unsupported := []string{
		"notes.txt",
		"data.json",
		"style.css",
		"template.html",
		"build.py",
		"Makefile",
		"",
	}
	for _, f := range unsupported {
		if IsSupported(f) {
			t.Errorf("IsSupported(%q) = true, want false", f)
		}
	}
}

// ---- SupportedExtensions ----

func TestSupportedExtensions(t *testing.T) {
	exts := SupportedExtensions()
	if len(exts) == 0 {
		t.Fatal("SupportedExtensions returned empty slice")
	}
	want := map[string]bool{
		".go":  true,
		".js":  true,
		".jsx": true,
		".ts":  true,
		".tsx": true,
		".php": true,
	}
	for _, e := range exts {
		if !want[e] {
			t.Errorf("unexpected extension %q in SupportedExtensions", e)
		}
		delete(want, e)
	}
	for missing := range want {
		t.Errorf("missing extension %q from SupportedExtensions", missing)
	}
}

// ---- ParseFile: Go ----

const sampleGoContent = `package sample

// MyStruct is a sample struct.
type MyStruct struct {
	Value int
}

// Greet says hello.
func Greet(name string) string {
	return "Hello, " + name
}

// Sum adds two numbers.
func Sum(a, b int) int {
	return a + b
}

// (MyStruct).Describe is a method on MyStruct.
func (m *MyStruct) Describe() string {
	return Greet("world")
}
`

func TestParseFile_Go_NodeCount(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "sample.go", sampleGoContent)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile Go: %v", err)
	}

	// Expect: MyStruct (struct), Greet (function), Sum (function), MyStruct.Describe (method)
	if len(result.Nodes) != 4 {
		t.Errorf("expected 4 nodes, got %d:", len(result.Nodes))
		for _, n := range result.Nodes {
			t.Logf("  node: SymbolName=%q NodeType=%v", n.SymbolName, n.NodeType)
		}
	}
}

func TestParseFile_Go_NodeTypes(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "sample.go", sampleGoContent)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile Go: %v", err)
	}

	if !hasNodeType(result.Nodes, types.NodeTypeFunction) {
		t.Error("expected at least one NodeTypeFunction")
	}
	if !hasNodeType(result.Nodes, types.NodeTypeStruct) {
		t.Error("expected at least one NodeTypeStruct")
	}
	if !hasNodeType(result.Nodes, types.NodeTypeMethod) {
		t.Error("expected at least one NodeTypeMethod")
	}
}

func TestParseFile_Go_SpecificNodes(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "sample.go", sampleGoContent)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile Go: %v", err)
	}

	if findNodeBySymbol(result.Nodes, "Greet") == nil {
		t.Error("expected node 'Greet' not found")
	}
	if findNodeBySymbol(result.Nodes, "Sum") == nil {
		t.Error("expected node 'Sum' not found")
	}
	if findNodeBySymbol(result.Nodes, "MyStruct") == nil {
		t.Error("expected node 'MyStruct' not found")
	}
	if findNodeBySymbol(result.Nodes, "MyStruct.Describe") == nil {
		t.Error("expected node 'MyStruct.Describe' not found")
	}
}

func TestParseFile_Go_Edges(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "sample.go", sampleGoContent)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile Go: %v", err)
	}

	// MyStruct.Describe calls Greet — at least one edge should have EdgeTypeCalls
	if len(result.Edges) == 0 {
		t.Error("expected at least one edge (function call), got none")
	}
	hasCalls := false
	for _, e := range result.Edges {
		if e.EdgeType == types.EdgeTypeCalls {
			hasCalls = true
			break
		}
	}
	if !hasCalls {
		t.Error("expected at least one EdgeTypeCalls edge")
	}
}

func TestParseFile_Go_NodeIDs_AreDeterministic(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "sample.go", sampleGoContent)

	p := New()
	r1, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile Go (first): %v", err)
	}
	r2, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile Go (second): %v", err)
	}

	for i := range r1.Nodes {
		if r1.Nodes[i].ID != r2.Nodes[i].ID {
			t.Errorf("node[%d] ID not deterministic: %q vs %q", i, r1.Nodes[i].ID, r2.Nodes[i].ID)
		}
	}
}

func TestParseFile_Go_RelativePath(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "sample.go", sampleGoContent)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile Go: %v", err)
	}

	for _, n := range result.Nodes {
		if filepath.IsAbs(n.FilePath) {
			t.Errorf("node FilePath is absolute, expected relative: %q", n.FilePath)
		}
	}
}

// ---- ParseFile: JavaScript ----

const sampleJSContent = `
function greetUser(name) {
    return "Hello " + name;
}

const computeTotal = (items) => {
    let total = 0;
    return total;
};

class ShoppingCart {
    addItem(item) {
        greetUser(item.name);
    }
}
`

func TestParseFile_JS_Nodes(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "app.js", sampleJSContent)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile JS: %v", err)
	}

	if len(result.Nodes) == 0 {
		t.Fatal("expected nodes from JS file, got none")
	}

	if findNodeBySymbol(result.Nodes, "greetUser") == nil {
		t.Error("expected node 'greetUser' not found in JS parse result")
	}
	if findNodeBySymbol(result.Nodes, "computeTotal") == nil {
		t.Error("expected node 'computeTotal' (arrow function) not found in JS parse result")
	}
	if findNodeBySymbol(result.Nodes, "ShoppingCart") == nil {
		t.Error("expected node 'ShoppingCart' (class) not found in JS parse result")
	}
}

func TestParseFile_JS_NodeTypes(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "app.js", sampleJSContent)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile JS: %v", err)
	}

	if !hasNodeType(result.Nodes, types.NodeTypeFunction) {
		t.Error("expected NodeTypeFunction for JS function/arrow-function declarations")
	}
	if !hasNodeType(result.Nodes, types.NodeTypeClass) {
		t.Error("expected NodeTypeClass for JS class declaration")
	}
}

func TestParseFile_JS_TypeScriptExtension(t *testing.T) {
	dir := t.TempDir()
	const tsContent = `
export async function fetchData(url: string): Promise<string> {
    return url;
}
`
	path := writeFile(t, dir, "service.ts", tsContent)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile TS: %v", err)
	}
	if findNodeBySymbol(result.Nodes, "fetchData") == nil {
		t.Error("expected 'fetchData' node in TS file")
	}
}

// ---- ParseFile: PHP ----

const samplePHPContent = `<?php

class UserService {
    public function getUser($id) {
        $repo = new UserRepository();
        return $repo;
    }

    private function validate($data) {
        return true;
    }
}

function bootstrapApp() {
    $svc = new UserService();
    return $svc;
}
`

func TestParseFile_PHP_Nodes(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "service.php", samplePHPContent)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile PHP: %v", err)
	}

	if len(result.Nodes) == 0 {
		t.Fatal("expected nodes from PHP file, got none")
	}

	if findNodeBySymbol(result.Nodes, "UserService") == nil {
		t.Error("expected class node 'UserService'")
	}

	// Methods should be qualified as ClassName.methodName
	if findNodeBySymbol(result.Nodes, "UserService.getUser") == nil {
		t.Error("expected method node 'UserService.getUser'")
	}
	if findNodeBySymbol(result.Nodes, "UserService.validate") == nil {
		t.Error("expected method node 'UserService.validate'")
	}

	if findNodeBySymbol(result.Nodes, "bootstrapApp") == nil {
		t.Error("expected standalone function node 'bootstrapApp'")
	}
}

func TestParseFile_PHP_NodeTypes(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "service.php", samplePHPContent)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile PHP: %v", err)
	}

	if !hasNodeType(result.Nodes, types.NodeTypeClass) {
		t.Error("expected NodeTypeClass for PHP class")
	}
	if !hasNodeType(result.Nodes, types.NodeTypeMethod) {
		t.Error("expected NodeTypeMethod for PHP class methods")
	}
	if !hasNodeType(result.Nodes, types.NodeTypeFunction) {
		t.Error("expected NodeTypeFunction for PHP standalone function")
	}
}

func TestParseFile_PHP_Edges(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "service.php", samplePHPContent)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile PHP: %v", err)
	}

	// bootstrapApp uses "new UserService()" → EdgeTypeInstantiates
	hasInstantiates := false
	for _, e := range result.Edges {
		if e.EdgeType == types.EdgeTypeInstantiates {
			hasInstantiates = true
			break
		}
	}
	if !hasInstantiates {
		t.Error("expected at least one EdgeTypeInstantiates edge from 'new' expressions in PHP")
	}
}

// ---- ParseFile: unsupported extension ----

func TestParseFile_UnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "notes.txt", "just some text")

	p := New()
	_, err := p.ParseFile(path, dir)
	if err == nil {
		t.Error("expected error for unsupported extension .txt, got nil")
	}
}

// ---- ParseFile: file not found ----

func TestParseFile_FileNotFound(t *testing.T) {
	p := New()
	_, err := p.ParseFile("/nonexistent/path/file.go", "/nonexistent/path")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}
