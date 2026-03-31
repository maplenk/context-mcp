package parser

import (
	"os"
	"path/filepath"
	"strings"
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

	// Expect: sample.go (file), MyStruct (struct), Greet (function), Sum (function), MyStruct.Describe (method)
	if len(result.Nodes) != 5 {
		t.Errorf("expected 5 nodes, got %d:", len(result.Nodes))
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

// ===========================================================================
// C1: findBlockEnd state machine — handles strings, comments, regex, template literals
// ===========================================================================

func TestFindBlockEnd_BracesInString(t *testing.T) {
	// The naive brace counter would be confused by braces inside strings
	src := []byte(`function test() { let s = "{ not a block }"; return s; }`)
	end := findBlockEnd([]byte(src), 0)
	// Should find the final } at the end, not the one inside the string
	expected := uint32(len(src))
	if end != expected {
		t.Errorf("findBlockEnd with braces in string: got %d, want %d", end, expected)
	}
}

func TestFindBlockEnd_BracesInSingleQuoteString(t *testing.T) {
	src := []byte(`function f() { let s = '{}{}{}'; return 1; }`)
	end := findBlockEnd(src, 0)
	expected := uint32(len(src))
	if end != expected {
		t.Errorf("findBlockEnd with braces in single-quote string: got %d, want %d", end, expected)
	}
}

func TestFindBlockEnd_BracesInLineComment(t *testing.T) {
	src := []byte("function f() {\n  // this { has a brace\n  return 1;\n}")
	end := findBlockEnd(src, 0)
	expected := uint32(len(src))
	if end != expected {
		t.Errorf("findBlockEnd with braces in line comment: got %d, want %d", end, expected)
	}
}

func TestFindBlockEnd_BracesInBlockComment(t *testing.T) {
	src := []byte("function f() {\n  /* { } */\n  return 1;\n}")
	end := findBlockEnd(src, 0)
	expected := uint32(len(src))
	if end != expected {
		t.Errorf("findBlockEnd with braces in block comment: got %d, want %d", end, expected)
	}
}

func TestFindBlockEnd_BracesInTemplateLiteral(t *testing.T) {
	src := []byte("function f() { let s = `template {}`; return s; }")
	end := findBlockEnd(src, 0)
	expected := uint32(len(src))
	if end != expected {
		t.Errorf("findBlockEnd with braces in template literal: got %d, want %d", end, expected)
	}
}

func TestFindBlockEnd_EscapedQuotesInString(t *testing.T) {
	src := []byte(`function f() { let s = "escaped \" { brace"; return s; }`)
	end := findBlockEnd(src, 0)
	expected := uint32(len(src))
	if end != expected {
		t.Errorf("findBlockEnd with escaped quote: got %d, want %d", end, expected)
	}
}

func TestFindBlockEnd_RegexLiteral(t *testing.T) {
	src := []byte("function f() { let re = /{}/; return re; }")
	end := findBlockEnd(src, 0)
	expected := uint32(len(src))
	if end != expected {
		t.Errorf("findBlockEnd with regex literal: got %d, want %d", end, expected)
	}
}

// ===========================================================================
// C1: JS/TS class method extraction
// ===========================================================================

func TestParseFile_JS_ClassMethods(t *testing.T) {
	dir := t.TempDir()
	const jsWithMethods = `
class UserService {
    getUser(id) {
        return this.db.find(id);
    }

    async saveUser(user) {
        return this.db.save(user);
    }
}
`
	path := writeFile(t, dir, "service.js", jsWithMethods)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile JS with methods: %v", err)
	}

	if findNodeBySymbol(result.Nodes, "UserService") == nil {
		t.Error("expected class node 'UserService'")
	}
	if findNodeBySymbol(result.Nodes, "UserService.getUser") == nil {
		t.Error("expected method node 'UserService.getUser'")
	}
	if findNodeBySymbol(result.Nodes, "UserService.saveUser") == nil {
		t.Error("expected method node 'UserService.saveUser'")
	}

	// Methods should be NodeTypeMethod
	for _, node := range result.Nodes {
		if node.SymbolName == "UserService.getUser" || node.SymbolName == "UserService.saveUser" {
			if node.NodeType != types.NodeTypeMethod {
				t.Errorf("expected %s to be NodeTypeMethod, got %v", node.SymbolName, node.NodeType)
			}
		}
	}
}

// ===========================================================================
// C1: JS/TS indented declarations (no line-start anchoring)
// ===========================================================================

func TestParseFile_JS_IndentedFunction(t *testing.T) {
	dir := t.TempDir()
	const jsIndented = `
  function indentedFunc(x) {
    return x * 2;
  }

  const indentedArrow = (y) => {
    return y + 1;
  };
`
	path := writeFile(t, dir, "indented.js", jsIndented)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile JS indented: %v", err)
	}

	if findNodeBySymbol(result.Nodes, "indentedFunc") == nil {
		t.Error("expected 'indentedFunc' — indented function declaration not found")
	}
	if findNodeBySymbol(result.Nodes, "indentedArrow") == nil {
		t.Error("expected 'indentedArrow' — indented arrow function not found")
	}
}

// ===========================================================================
// H1: Go import edge extraction
// ===========================================================================

func TestParseFile_Go_ImportEdges(t *testing.T) {
	dir := t.TempDir()
	const goWithImports = `package main

import (
	"fmt"
	"strings"
	"os"
)

func main() {
	fmt.Println("hello")
}
`
	path := writeFile(t, dir, "main.go", goWithImports)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile Go imports: %v", err)
	}

	importCount := 0
	importPaths := map[string]bool{}
	for _, e := range result.Edges {
		if e.EdgeType == types.EdgeTypeImports {
			importCount++
			importPaths[e.TargetID] = true
		}
	}
	if importCount != 3 {
		t.Errorf("expected 3 import edges, got %d", importCount)
	}

	// C1: Import edge targets now reference the target file's file node
	fmtID := types.GenerateNodeID("fmt", "fmt")
	stringsID := types.GenerateNodeID("strings", "strings")
	osID := types.GenerateNodeID("os", "os")
	if !importPaths[fmtID] {
		t.Error("missing import edge for 'fmt'")
	}
	if !importPaths[stringsID] {
		t.Error("missing import edge for 'strings'")
	}
	if !importPaths[osID] {
		t.Error("missing import edge for 'os'")
	}
}

// ===========================================================================
// H1: JS/TS import edge extraction
// ===========================================================================

func TestParseFile_JS_ImportEdges(t *testing.T) {
	dir := t.TempDir()
	const jsWithImports = `
import React from 'react';
import { useState, useEffect } from 'react';
import * as lodash from 'lodash';
const fs = require('fs');

function App() {
    return null;
}
`
	path := writeFile(t, dir, "app.js", jsWithImports)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile JS imports: %v", err)
	}

	importCount := 0
	for _, e := range result.Edges {
		if e.EdgeType == types.EdgeTypeImports {
			importCount++
		}
	}
	// react (from first import), react (from second import), lodash, fs
	if importCount < 3 {
		t.Errorf("expected at least 3 import edges, got %d", importCount)
	}
}

func TestParseFile_TS_ImportEdges(t *testing.T) {
	dir := t.TempDir()
	const tsWithImports = `
import { Router } from 'express';
import type { Request, Response } from 'express';

export function handler(req: Request, res: Response) {
    return res.json({});
}
`
	path := writeFile(t, dir, "handler.ts", tsWithImports)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile TS imports: %v", err)
	}

	hasImport := false
	for _, e := range result.Edges {
		if e.EdgeType == types.EdgeTypeImports {
			hasImport = true
			break
		}
	}
	if !hasImport {
		t.Error("expected at least one import edge from TS file")
	}
}

// ===========================================================================
// H1: PHP import (use) edge extraction
// ===========================================================================

func TestParseFile_PHP_ImportEdges(t *testing.T) {
	dir := t.TempDir()
	const phpWithUse = `<?php

use App\Models\User;
use App\Services\AuthService;
use Illuminate\Support\Facades\Log;

class UserController {
    public function index() {
        $user = new User();
        return $user;
    }
}
`
	path := writeFile(t, dir, "controller.php", phpWithUse)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile PHP imports: %v", err)
	}

	importCount := 0
	for _, e := range result.Edges {
		if e.EdgeType == types.EdgeTypeImports {
			importCount++
		}
	}
	if importCount != 3 {
		t.Errorf("expected 3 PHP import (use) edges, got %d", importCount)
	}
}

// ===========================================================================
// M5: PHP call edge extraction
// ===========================================================================

func TestParseFile_PHP_CallEdges(t *testing.T) {
	dir := t.TempDir()
	const phpWithCalls = `<?php

class OrderService {
    public function processOrder($order) {
        $this->validate($order);
        $total = $this->calculateTotal($order);
        Logger::info("Processing");
        return $total;
    }

    private function validate($order) {
        return true;
    }

    private function calculateTotal($order) {
        return 100;
    }
}

function helperFunc() {
    $svc = new OrderService();
    $svc->processOrder(null);
    strlen("hello");
    return true;
}
`
	path := writeFile(t, dir, "order.php", phpWithCalls)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile PHP calls: %v", err)
	}

	callCount := 0
	hasMethodCall := false
	hasStaticCall := false
	hasFuncCall := false

	for _, e := range result.Edges {
		if e.EdgeType == types.EdgeTypeCalls {
			callCount++
			// Check for $this->validate call
			validateID := types.GenerateNodeID("order.php", "validate")
			loggerInfoID := types.GenerateNodeID("order.php", "Logger.info")
			strlenID := types.GenerateNodeID("order.php", "strlen")
			if e.TargetID == validateID {
				hasMethodCall = true
			}
			if e.TargetID == loggerInfoID {
				hasStaticCall = true
			}
			if e.TargetID == strlenID {
				hasFuncCall = true
			}
		}
	}

	if callCount == 0 {
		t.Error("expected PHP call edges, got none")
	}
	if !hasMethodCall {
		t.Error("expected $this->validate() method call edge")
	}
	if !hasStaticCall {
		t.Error("expected Logger::info() static call edge")
	}
	if !hasFuncCall {
		t.Error("expected strlen() function call edge")
	}
}

// ===========================================================================
// L1: buildContentSum captures full doc blocks
// ===========================================================================

func TestBuildContentSum_JSDoc(t *testing.T) {
	lines := []string{
		"/**",
		" * Fetches user data from the API.",
		" * @param {string} id - The user ID",
		" * @returns {Promise<User>}",
		" */",
		"function fetchUser(id) {",
		"    return fetch('/api/users/' + id);",
		"}",
	}
	// byte offset of "function fetchUser(id) {" line
	offset := 0
	for i := 0; i < 5; i++ {
		offset += len(lines[i]) + 1 // +1 for newline
	}

	summary := buildContentSum(lines, offset, "fetchUser")
	// Should contain content from the doc block, not just the last line
	if !strings.Contains(summary, "Fetches user data") {
		t.Errorf("expected doc block content in summary, got: %q", summary)
	}
	if !strings.Contains(summary, "@param") {
		t.Errorf("expected @param in summary, got: %q", summary)
	}
	if !strings.Contains(summary, "fetchUser") {
		t.Errorf("expected function name in summary, got: %q", summary)
	}
}

func TestBuildContentSum_PHPDoc(t *testing.T) {
	lines := []string{
		"<?php",
		"",
		"/**",
		" * Process a payment transaction.",
		" * @param float $amount",
		" */",
		"function processPayment($amount) {",
		"    return true;",
		"}",
	}
	offset := 0
	for i := 0; i < 6; i++ {
		offset += len(lines[i]) + 1
	}

	summary := buildContentSum(lines, offset, "processPayment")
	if !strings.Contains(summary, "Process a payment") {
		t.Errorf("expected PHPDoc content in summary, got: %q", summary)
	}
}

func TestBuildContentSum_SingleLineComments(t *testing.T) {
	lines := []string{
		"// Helper function to validate input",
		"// Returns true if the input is valid",
		"function validateInput(data) {",
		"    return true;",
		"}",
	}
	offset := 0
	for i := 0; i < 2; i++ {
		offset += len(lines[i]) + 1
	}

	summary := buildContentSum(lines, offset, "validateInput")
	if !strings.Contains(summary, "validate input") {
		t.Errorf("expected first comment line content, got: %q", summary)
	}
	if !strings.Contains(summary, "Returns true") {
		t.Errorf("expected second comment line content, got: %q", summary)
	}
}

// ===========================================================================
// Integration: full-file parse produces expected node+edge counts
// ===========================================================================

func TestParseFile_JS_FullIntegration(t *testing.T) {
	dir := t.TempDir()
	const jsFile = `
import { db } from './database';
import logger from 'winston';

/**
 * UserService handles user operations.
 */
class UserService {
    getAll() {
        return db.findAll();
    }

    getById(id) {
        logger.info("fetching user");
        return db.find(id);
    }
}

export function createService() {
    return new UserService();
}
`
	path := writeFile(t, dir, "users.js", jsFile)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile JS full integration: %v", err)
	}

	// Should have: UserService (class), UserService.getAll (method),
	// UserService.getById (method), createService (function)
	if findNodeBySymbol(result.Nodes, "UserService") == nil {
		t.Error("missing UserService class")
	}
	if findNodeBySymbol(result.Nodes, "UserService.getAll") == nil {
		t.Error("missing UserService.getAll method")
	}
	if findNodeBySymbol(result.Nodes, "UserService.getById") == nil {
		t.Error("missing UserService.getById method")
	}
	if findNodeBySymbol(result.Nodes, "createService") == nil {
		t.Error("missing createService function")
	}

	// Should have import edges
	hasImport := false
	for _, e := range result.Edges {
		if e.EdgeType == types.EdgeTypeImports {
			hasImport = true
			break
		}
	}
	if !hasImport {
		t.Error("expected import edges from JS file")
	}

	// UserService doc block should be captured in content summary
	usNode := findNodeBySymbol(result.Nodes, "UserService")
	if usNode != nil && !strings.Contains(usNode.ContentSum, "user operations") {
		t.Errorf("expected UserService content summary to contain doc block, got: %q", usNode.ContentSum)
	}
}

// ===========================================================================
// H3: PHP indented class and function declarations
// ===========================================================================

func TestParsePHP_IndentedClass(t *testing.T) {
	dir := t.TempDir()
	const phpIndented = `<?php

namespace App\Controllers;

    class IndentedController {
        public function handle() {
            return true;
        }
    }

    function indentedHelper() {
        return 42;
    }
`
	path := writeFile(t, dir, "indented.php", phpIndented)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile PHP indented: %v", err)
	}

	if findNodeBySymbol(result.Nodes, "IndentedController") == nil {
		t.Error("expected indented PHP class 'IndentedController' to be found")
	}
	if findNodeBySymbol(result.Nodes, "indentedHelper") == nil {
		t.Error("expected indented PHP function 'indentedHelper' to be found")
	}
}

// ===========================================================================
// C1: File-level nodes for import edge anchoring
// ===========================================================================

func TestParseFile_Go_FileNode(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "sample.go", sampleGoContent)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile Go: %v", err)
	}

	fileNode := findNodeBySymbol(result.Nodes, "sample.go")
	if fileNode == nil {
		t.Fatal("expected file-level node 'sample.go'")
	}
	if fileNode.NodeType != types.NodeTypeFile {
		t.Errorf("file node type = %v, want file", fileNode.NodeType)
	}
	if fileNode.StartByte != 0 {
		t.Errorf("file node StartByte = %d, want 0", fileNode.StartByte)
	}
	if fileNode.ID != types.GenerateNodeID("sample.go", "sample.go") {
		t.Error("file node ID does not match GenerateNodeID(relPath, relPath)")
	}
}

func TestParseFile_JS_FileNode(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "app.js", sampleJSContent)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile JS: %v", err)
	}

	fileNode := findNodeBySymbol(result.Nodes, "app.js")
	if fileNode == nil {
		t.Fatal("expected file-level node 'app.js'")
	}
	if fileNode.NodeType != types.NodeTypeFile {
		t.Errorf("file node type = %v, want file", fileNode.NodeType)
	}
}

func TestParseFile_PHP_FileNode(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "service.php", samplePHPContent)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile PHP: %v", err)
	}

	fileNode := findNodeBySymbol(result.Nodes, "service.php")
	if fileNode == nil {
		t.Fatal("expected file-level node 'service.php'")
	}
	if fileNode.NodeType != types.NodeTypeFile {
		t.Errorf("file node type = %v, want file", fileNode.NodeType)
	}
}

// ===========================================================================
// C1: Import edges reference target file nodes
// ===========================================================================

func TestParseFile_Go_ImportEdgeTargets(t *testing.T) {
	dir := t.TempDir()
	const goWithImports = `package main

import "fmt"

func main() { fmt.Println("hi") }
`
	path := writeFile(t, dir, "main.go", goWithImports)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	for _, e := range result.Edges {
		if e.EdgeType == types.EdgeTypeImports {
			// Source should be this file's file node
			if e.SourceID != types.GenerateNodeID("main.go", "main.go") {
				t.Errorf("import edge source = %q, want file node ID", e.SourceID)
			}
			// Target should be the imported package's file node
			expected := types.GenerateNodeID("fmt", "fmt")
			if e.TargetID != expected {
				t.Errorf("import edge target = %q, want GenerateNodeID('fmt','fmt')=%q", e.TargetID, expected)
			}
		}
	}
}

// ===========================================================================
// H22: NodeTypeInterface in Go parser
// ===========================================================================

func TestParseFile_Go_Interface(t *testing.T) {
	dir := t.TempDir()
	const goWithInterface = `package sample

type Reader interface {
	Read(p []byte) (n int, err error)
}

type Writer interface {
	Write(p []byte) (n int, err error)
}
`
	path := writeFile(t, dir, "iface.go", goWithInterface)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile Go interfaces: %v", err)
	}

	if !hasNodeType(result.Nodes, types.NodeTypeInterface) {
		t.Error("expected NodeTypeInterface for Go interface declarations")
	}
	reader := findNodeBySymbol(result.Nodes, "Reader")
	if reader == nil {
		t.Fatal("expected 'Reader' interface node")
	}
	if reader.NodeType != types.NodeTypeInterface {
		t.Errorf("Reader node type = %v, want interface", reader.NodeType)
	}
}

// ===========================================================================
// H21: Go named types (type aliases)
// ===========================================================================

func TestParseFile_Go_NamedTypes(t *testing.T) {
	dir := t.TempDir()
	const goWithNamedTypes = `package sample

type Handler func(w http.ResponseWriter, r *http.Request)

type UserID string

type Count int64
`
	path := writeFile(t, dir, "named.go", goWithNamedTypes)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile Go named types: %v", err)
	}

	// Named types should be captured (not skipped)
	if findNodeBySymbol(result.Nodes, "Handler") == nil {
		t.Error("expected 'Handler' named type node")
	}
	if findNodeBySymbol(result.Nodes, "UserID") == nil {
		t.Error("expected 'UserID' named type node")
	}
	if findNodeBySymbol(result.Nodes, "Count") == nil {
		t.Error("expected 'Count' named type node")
	}
}

// ===========================================================================
// H4: findBlockEnd handles template literal ${} interpolation
// ===========================================================================

func TestFindBlockEnd_TemplateLiteralInterpolation(t *testing.T) {
	src := []byte("function f() { let s = `hello ${name + `nested`} world`; return s; }")
	end := findBlockEnd(src, 0)
	expected := uint32(len(src))
	if end != expected {
		t.Errorf("findBlockEnd with template literal interpolation: got %d, want %d", end, expected)
	}
}

func TestFindBlockEnd_TemplateLiteralBracesInInterpolation(t *testing.T) {
	src := []byte("function f() { let s = `${obj.map(x => { return x; })}`; return s; }")
	end := findBlockEnd(src, 0)
	expected := uint32(len(src))
	if end != expected {
		t.Errorf("findBlockEnd with nested braces in template interpolation: got %d, want %d", end, expected)
	}
}

// ===========================================================================
// H5: PHP methods without visibility keywords
// ===========================================================================

func TestParsePHP_MethodsWithoutVisibility(t *testing.T) {
	dir := t.TempDir()
	const phpNoVisibility = `<?php

class MyController {
    function handle() {
        return true;
    }

    static function create() {
        return new self();
    }

    public function destroy() {
        return false;
    }
}
`
	path := writeFile(t, dir, "ctrl.php", phpNoVisibility)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile PHP no visibility: %v", err)
	}

	if findNodeBySymbol(result.Nodes, "MyController.handle") == nil {
		t.Error("expected method 'MyController.handle' without visibility keyword")
	}
	if findNodeBySymbol(result.Nodes, "MyController.create") == nil {
		t.Error("expected method 'MyController.create' with static but no visibility")
	}
	if findNodeBySymbol(result.Nodes, "MyController.destroy") == nil {
		t.Error("expected method 'MyController.destroy' with public visibility")
	}
}

// ===========================================================================
// C11: PHP parser deduplication
// ===========================================================================

func TestParsePHP_NoDuplicateNodes(t *testing.T) {
	dir := t.TempDir()
	const phpWithFuncs = `<?php

class Service {
    public function process() {
        return true;
    }

    function helper() {
        return false;
    }
}

function standalone() {
    return 42;
}
`
	path := writeFile(t, dir, "dedup.php", phpWithFuncs)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile PHP dedup: %v", err)
	}

	// Count occurrences of each symbol name
	nameCount := map[string]int{}
	for _, n := range result.Nodes {
		nameCount[n.SymbolName]++
	}
	for name, count := range nameCount {
		if count > 1 {
			t.Errorf("duplicate node for %q: appeared %d times", name, count)
		}
	}
}

// ===========================================================================
// M9: Call edge deduplication
// ===========================================================================

func TestParseFile_Go_CallEdgeDedup(t *testing.T) {
	dir := t.TempDir()
	const goWithRepeatedCalls = `package main

func process() {
	validate()
	validate()
	validate()
	transform()
}

func validate() {}
func transform() {}
`
	path := writeFile(t, dir, "dedup.go", goWithRepeatedCalls)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile Go dedup: %v", err)
	}

	// Count call edges from process
	processID := types.GenerateNodeID("dedup.go", "process")
	callCount := 0
	for _, e := range result.Edges {
		if e.SourceID == processID && e.EdgeType == types.EdgeTypeCalls {
			callCount++
		}
	}
	// Should be 2 (validate + transform), not 4 (validate x3 + transform)
	if callCount != 2 {
		t.Errorf("expected 2 deduplicated call edges from process, got %d", callCount)
	}
}

// ===========================================================================
// M14: buildContentSum blank-line separation
// ===========================================================================

func TestBuildContentSum_BlankLineSeparation(t *testing.T) {
	lines := []string{
		"// This is an unrelated comment about something else",
		"// It should NOT be included",
		"",
		"function targetFunc() {",
		"    return true;",
		"}",
	}
	// byte offset of "function targetFunc() {" line
	offset := 0
	for i := 0; i < 3; i++ {
		offset += len(lines[i]) + 1
	}

	summary := buildContentSum(lines, offset, "targetFunc")
	// The comment block is separated by a blank line, so it should NOT be captured
	if strings.Contains(summary, "unrelated") {
		t.Errorf("expected blank line to prevent doc block capture, got: %q", summary)
	}
}

// ===========================================================================
// L17: TypeScript constructs (interfaces, enums, type aliases)
// ===========================================================================

func TestParseFile_TS_Constructs(t *testing.T) {
	dir := t.TempDir()
	const tsContent = `
export interface UserProfile {
    name: string;
    email: string;
}

export const enum Status {
    Active = "active",
    Inactive = "inactive",
}

export type UserID = string;

export type Handler<T> = (req: T) => void;

function processUser(id: UserID): void {
    return;
}
`
	path := writeFile(t, dir, "types.ts", tsContent)
	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile TS constructs: %v", err)
	}

	if findNodeBySymbol(result.Nodes, "UserProfile") == nil {
		t.Error("expected TS interface 'UserProfile'")
	}
	if findNodeBySymbol(result.Nodes, "Status") == nil {
		t.Error("expected TS enum 'Status'")
	}
	if findNodeBySymbol(result.Nodes, "UserID") == nil {
		t.Error("expected TS type alias 'UserID'")
	}
	if findNodeBySymbol(result.Nodes, "Handler") == nil {
		t.Error("expected TS generic type alias 'Handler'")
	}

	// Check correct node types
	up := findNodeBySymbol(result.Nodes, "UserProfile")
	if up != nil && up.NodeType != types.NodeTypeInterface {
		t.Errorf("UserProfile type = %v, want interface", up.NodeType)
	}
	st := findNodeBySymbol(result.Nodes, "Status")
	if st != nil && st.NodeType != types.NodeTypeStruct {
		t.Errorf("Status type = %v, want struct", st.NodeType)
	}
}

// ===========================================================================
// M10: findBlockEnd fallback returns end of content
// ===========================================================================

func TestFindBlockEnd_NoBlock(t *testing.T) {
	src := []byte("no braces at all here")
	end := findBlockEnd(src, 0)
	expected := uint32(len(src))
	if end != expected {
		t.Errorf("findBlockEnd with no braces: got %d, want %d (len of content)", end, expected)
	}
}

// ===========================================================================
// M15: GenerateNodeID null byte separator
// ===========================================================================

func TestGenerateNodeID_SeparatorCollision(t *testing.T) {
	// With colon separator: "a:b" + "c" and "a" + "b:c" would collide
	// With null byte separator they should not
	id1 := types.GenerateNodeID("a:b", "c")
	id2 := types.GenerateNodeID("a", "b:c")
	if id1 == id2 {
		t.Error("GenerateNodeID should not collide for inputs with colons")
	}
}
