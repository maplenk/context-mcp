package parser

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/naman/qb-context/internal/types"
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/php"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// walkTree recursively walks a tree-sitter node tree, calling fn for each named node.
// If fn returns true, the walker recurses into children; if false, it skips them.
func walkTree(node *sitter.Node, fn func(n *sitter.Node) bool) {
	if node == nil {
		return
	}
	if !fn(node) {
		return // skip children
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(int(i))
		walkTree(child, fn)
	}
}

// Parser extracts structural nodes and edges from source files
type Parser struct{}

// New creates a new Parser
func New() *Parser {
	return &Parser{}
}

// ParseResult contains the extracted nodes and edges from a file
type ParseResult struct {
	Nodes []types.ASTNode
	Edges []types.ASTEdge
}

// ParseFile parses a source file and extracts AST nodes and edges
func (p *Parser) ParseFile(filePath string, repoRoot string) (*ParseResult, error) {
	// M4: Read file first, then check size to avoid TOCTOU race
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading file %s: %w", filePath, err)
	}
	if len(content) > 5*1024*1024 {
		return nil, fmt.Errorf("file %s too large (%d bytes), skipping", filePath, len(content))
	}

	relPath, err := filepath.Rel(repoRoot, filePath)
	if err != nil {
		relPath = filePath
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".go":
		return p.parseGo(content, relPath)
	case ".js", ".jsx", ".ts", ".tsx":
		return p.parseJavaScript(content, relPath)
	case ".php":
		return p.parsePHP(content, relPath)
	default:
		return nil, fmt.Errorf("unsupported file extension: %s", ext)
	}
}

// SupportedExtensions returns the list of supported file extensions
func SupportedExtensions() []string {
	return []string{".go", ".js", ".jsx", ".ts", ".tsx", ".php"}
}

// IsSupported returns true if the file extension is supported for parsing
func IsSupported(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	for _, supported := range SupportedExtensions() {
		if ext == supported {
			return true
		}
	}
	return false
}

// exprToString renders a Go AST expression as source text using go/printer.
// Falls back to fmt.Sprintf if printing fails.
func exprToString(fset *token.FileSet, expr ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, expr); err != nil {
		return fmt.Sprintf("%v", expr)
	}
	return buf.String()
}

// parseGo uses Go's native AST parser for accurate Go file parsing
func (p *Parser) parseGo(content []byte, relPath string) (*ParseResult, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, relPath, content, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing Go file: %w", err)
	}

	result := &ParseResult{}

	// C1: Create file-level node so import edges have a valid source node
	fileNode := types.ASTNode{
		ID:         types.GenerateNodeID(relPath, relPath),
		FilePath:   relPath,
		SymbolName: relPath,
		NodeType:   types.NodeTypeFile,
		StartByte:  0,
		EndByte:    uint32(len(content)),
		ContentSum: relPath,
	}
	result.Nodes = append(result.Nodes, fileNode)

	// H1: Extract import edges from Go import statements
	for _, imp := range file.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		result.Edges = append(result.Edges, types.ASTEdge{
			SourceID: types.GenerateNodeID(relPath, relPath),             // this file
			TargetID: types.GenerateNodeID(importPath, importPath),       // target file's file node
			EdgeType: types.EdgeTypeImports,
		})
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch decl := n.(type) {
		case *ast.FuncDecl:
			name := decl.Name.Name
			nodeType := types.NodeTypeFunction

			// Check if it's a method (has receiver)
			if decl.Recv != nil && decl.Recv.NumFields() > 0 {
				nodeType = types.NodeTypeMethod
				// Prefix with receiver type
				recvType := extractReceiverType(decl.Recv)
				if recvType != "" {
					name = recvType + "." + name
				}
			}

			startByte := uint32(fset.Position(decl.Pos()).Offset)
			endByte := uint32(fset.Position(decl.End()).Offset)

			// M13: Build content summary from signature (including param types) + doc comment
			contentSum := name
			if decl.Type != nil && decl.Type.Params != nil {
				var params []string
				for _, param := range decl.Type.Params.List {
					params = append(params, exprToString(fset, param.Type))
				}
				contentSum = name + "(" + strings.Join(params, ", ") + ")"
			}
			if decl.Doc != nil {
				contentSum = decl.Doc.Text() + " " + contentSum
			}

			node := types.ASTNode{
				ID:         types.GenerateNodeID(relPath, name),
				FilePath:   relPath,
				SymbolName: name,
				NodeType:   nodeType,
				StartByte:  startByte,
				EndByte:    endByte,
				ContentSum: strings.TrimSpace(contentSum),
			}
			result.Nodes = append(result.Nodes, node)

			// DEFINES edge: file → function/method
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID: fileNode.ID,
				TargetID: node.ID,
				EdgeType: types.EdgeTypeDefines,
			})

			// DEFINES_METHOD edge: receiverType → method
			if nodeType == types.NodeTypeMethod && decl.Recv != nil && decl.Recv.NumFields() > 0 {
				recvType := extractReceiverType(decl.Recv)
				if recvType != "" {
					result.Edges = append(result.Edges, types.ASTEdge{
						SourceID: types.GenerateNodeID(relPath, recvType),
						TargetID: node.ID,
						EdgeType: types.EdgeTypeDefinesMethod,
					})
				}
			}

			// Extract function calls within the body (M9: deduplicate)
			if decl.Body != nil {
				calls := extractGoCalls(decl.Body)
				callSeen := map[string]bool{}
				for _, call := range calls {
					targetID := types.GenerateNodeID(relPath, call)
					edgeKey := node.ID + ":" + targetID
					if callSeen[edgeKey] {
						continue
					}
					callSeen[edgeKey] = true
					edge := types.ASTEdge{
						SourceID: node.ID,
						TargetID: targetID,
						EdgeType: types.EdgeTypeCalls,
					}
					result.Edges = append(result.Edges, edge)
				}
			}

		case *ast.GenDecl:
			if decl.Tok == token.TYPE {
				for _, spec := range decl.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}

					name := typeSpec.Name.Name
					var nodeType types.NodeType

					switch typeSpec.Type.(type) {
					case *ast.StructType:
						nodeType = types.NodeTypeStruct
					case *ast.InterfaceType:
						nodeType = types.NodeTypeInterface // H22: use distinct interface type
					default:
						// H21: Type aliases and named types (e.g., type Handler func(...), type UserID string)
						nodeType = types.NodeTypeFunction
					}

					startByte := uint32(fset.Position(typeSpec.Pos()).Offset)
					endByte := uint32(fset.Position(typeSpec.End()).Offset)

					contentSum := name
					doc := typeSpec.Doc
					if doc == nil {
						doc = decl.Doc // fall back to group doc if individual has none
					}
					if doc != nil {
						contentSum = doc.Text() + " " + name
					}

					node := types.ASTNode{
						ID:         types.GenerateNodeID(relPath, name),
						FilePath:   relPath,
						SymbolName: name,
						NodeType:   nodeType,
						StartByte:  startByte,
						EndByte:    endByte,
						ContentSum: strings.TrimSpace(contentSum),
					}
					result.Nodes = append(result.Nodes, node)

					// DEFINES edge: file → type
					result.Edges = append(result.Edges, types.ASTEdge{
						SourceID: fileNode.ID,
						TargetID: node.ID,
						EdgeType: types.EdgeTypeDefines,
					})
				}
			}
		}
		return true
	})

	return result, nil
}

// extractReceiverType gets the type name from a method receiver.
// M2: Handles generic receivers like Stack[T] (IndexExpr) and Map[K,V] (IndexListExpr).
func extractReceiverType(recv *ast.FieldList) string {
	if recv == nil || recv.NumFields() == 0 {
		return ""
	}
	field := recv.List[0]
	return extractBaseTypeName(field.Type)
}

// extractBaseTypeName recursively unwraps pointer, generic index, and paren expressions
// to find the base type name identifier.
func extractBaseTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return extractBaseTypeName(t.X)
	case *ast.IndexExpr:
		// Generic receiver: Stack[T]
		return extractBaseTypeName(t.X)
	case *ast.IndexListExpr:
		// Generic receiver with multiple type params: Map[K, V]
		return extractBaseTypeName(t.X)
	case *ast.ParenExpr:
		return extractBaseTypeName(t.X)
	}
	return ""
}

// extractGoCalls finds function/method calls within a Go AST block.
// H2/H3: Call edges are inherently file-local. Cross-package/cross-file calls would
// require an import resolution system. The graph connects these via import edges instead.
func extractGoCalls(body *ast.BlockStmt) []string {
	var calls []string
	ast.Inspect(body, func(n ast.Node) bool {
		callExpr, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		switch fn := callExpr.Fun.(type) {
		case *ast.Ident:
			// Simple function call: funcName()
			calls = append(calls, fn.Name)
		case *ast.SelectorExpr:
			// Method or package call: obj.Method() or pkg.Func()
			// M10: Recursively unwrap chained SelectorExpr to find the root identifier
			rootIdent := unwrapSelectorRoot(fn.X)
			if rootIdent != "" {
				calls = append(calls, rootIdent+"."+fn.Sel.Name)
			} else {
				// Chained call without identifiable root — just use the final method name
				calls = append(calls, fn.Sel.Name)
			}
		}
		return true
	})
	return calls
}

// unwrapSelectorRoot recursively walks a SelectorExpr chain to find the root *ast.Ident name.
// For example: a.b.c.Method() → returns "a" (the leftmost identifier).
func unwrapSelectorRoot(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return unwrapSelectorRoot(x.X)
	case *ast.CallExpr:
		// Chained call: foo().Bar() — try to resolve the function being called
		switch fn := x.Fun.(type) {
		case *ast.Ident:
			return fn.Name
		case *ast.SelectorExpr:
			return unwrapSelectorRoot(fn.X)
		}
	}
	return ""
}

// JavaScript/TypeScript regex patterns (kept for independent helper tests)
// Note: removed (?m)^ line-start anchoring so indented declarations are found
var (
	jsFuncDeclRe    = regexp.MustCompile(`(?m)(?:^|\n)\s*(?:export\s+)?(?:async\s+)?function\s+(\w+)\s*[(<]`)
	jsArrowFuncRe   = regexp.MustCompile(`(?m)(?:^|\n)\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?(?:\([^)]*\)|[a-zA-Z_]\w*)\s*(?::\s*[^=]+?)?\s*=>`)
	jsClassDeclRe   = regexp.MustCompile(`(?m)(?:^|\n)\s*(?:export\s+)?(?:default\s+)?class\s+(\w+)`)
	jsMethodDeclRe  = regexp.MustCompile(`(?m)(?:^|\n)\s+(?:async\s+)?(\w+)\s*\([^)]*\)\s*\{`)
	jsCallExprRe    = regexp.MustCompile(`(?:^|[^.\w])(\w+)\s*\(`)
	jsImportFromRe  = regexp.MustCompile(`(?m)import\s+(?:(?:[\w{},\s*]+)\s+from\s+)?['"]([^'"]+)['"]`)
	jsRequireRe     = regexp.MustCompile(`require\s*\(\s*['"]([^'"]+)['"]\s*\)`)
)

// L17: TypeScript-specific regex patterns (kept for independent helper tests)
var (
	tsInterfaceDeclRe = regexp.MustCompile(`(?m)(?:^|\n)\s*(?:export\s+)?interface\s+(\w+)`)
	tsEnumDeclRe      = regexp.MustCompile(`(?m)(?:^|\n)\s*(?:export\s+)?(?:const\s+)?enum\s+(\w+)`)
	tsTypeDeclRe      = regexp.MustCompile(`(?m)(?:^|\n)\s*(?:export\s+)?type\s+(\w+)\s*[=<]`)
)

// parseJavaScript uses tree-sitter for JS/TS file parsing.
// It falls back to regex if tree-sitter parsing fails.
func (p *Parser) parseJavaScript(content []byte, relPath string) (*ParseResult, error) {
	result := &ParseResult{}
	lines := strings.Split(string(content), "\n")

	// C1: Create file-level node so import edges have a valid source node
	fileNode := types.ASTNode{
		ID:         types.GenerateNodeID(relPath, relPath),
		FilePath:   relPath,
		SymbolName: relPath,
		NodeType:   types.NodeTypeFile,
		StartByte:  0,
		EndByte:    uint32(len(content)),
		ContentSum: relPath,
	}
	result.Nodes = append(result.Nodes, fileNode)

	// Determine if this is a TypeScript file
	ext := strings.ToLower(filepath.Ext(relPath))
	isTS := ext == ".ts" || ext == ".tsx"

	// Select the appropriate language grammar
	var lang *sitter.Language
	if isTS {
		if ext == ".tsx" {
			lang = tsx.GetLanguage()
		} else {
			lang = typescript.GetLanguage()
		}
	} else {
		lang = javascript.GetLanguage()
	}

	tsParser := sitter.NewParser()
	defer tsParser.Close()
	tsParser.SetLanguage(lang)
	tree, err := tsParser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		log.Printf("parser: tree-sitter failed for %s: %v — returning file-level node only", relPath, err)
		return result, nil
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil {
		log.Printf("parser: tree-sitter returned nil root for %s — returning file-level node only", relPath)
		return result, nil
	}

	// M3: Track names already added to avoid duplicates, keyed by name:nodeType
	// to avoid dropping same-name different-type symbols
	seen := map[string]bool{}

	// Walk the tree to extract declarations
	walkTree(root, func(n *sitter.Node) bool {
		if !n.IsNamed() {
			return true // continue
		}
		nodeType := n.Type()

		switch nodeType {
		case "function_declaration":
			// function name(...) { ... }
			nameNode := n.ChildByFieldName("name")
			// M9: Handle anonymous default exports — use "default" as synthetic name
			var name string
			if nameNode == nil {
				name = "default"
			} else {
				name = nameNode.Content(content)
			}
			seenKey := name + ":function"
			if seen[seenKey] {
				return true // continue
			}
			seen[seenKey] = true
			startByte := int(n.StartByte())
			endByte := n.EndByte()
			contentSum := buildContentSum(lines, startByte, name)
			result.Nodes = append(result.Nodes, types.ASTNode{
				ID:         types.GenerateNodeID(relPath, name),
				FilePath:   relPath,
				SymbolName: name,
				NodeType:   types.NodeTypeFunction,
				StartByte:  n.StartByte(),
				EndByte:    endByte,
				ContentSum: contentSum,
			})
			// DEFINES edge: file → function
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID: fileNode.ID,
				TargetID: types.GenerateNodeID(relPath, name),
				EdgeType: types.EdgeTypeDefines,
			})
			return false // skip children

		case "lexical_declaration", "variable_declaration":
			// const name = (...) => { ... } — arrow functions
			for i := 0; i < int(n.ChildCount()); i++ {
				child := n.Child(i)
				if child == nil || !child.IsNamed() {
					continue
				}
				if child.Type() != "variable_declarator" {
					continue
				}
				nameNode := child.ChildByFieldName("name")
				valueNode := child.ChildByFieldName("value")
				if nameNode == nil || valueNode == nil {
					continue
				}
				// M5: Also detect function expressions (const fn = function() {})
				valType := valueNode.Type()
				if valType != "arrow_function" && valType != "function" && valType != "function_expression" {
					continue
				}
				name := nameNode.Content(content)
				seenKey := name + ":function"
				if seen[seenKey] {
					continue
				}
				seen[seenKey] = true
				startByte := int(n.StartByte())
				endByte := n.EndByte()
				contentSum := buildContentSum(lines, startByte, name)
				result.Nodes = append(result.Nodes, types.ASTNode{
					ID:         types.GenerateNodeID(relPath, name),
					FilePath:   relPath,
					SymbolName: name,
					NodeType:   types.NodeTypeFunction,
					StartByte:  n.StartByte(),
					EndByte:    endByte,
					ContentSum: contentSum,
				})
				// DEFINES edge: file → arrow function
				result.Edges = append(result.Edges, types.ASTEdge{
					SourceID: fileNode.ID,
					TargetID: types.GenerateNodeID(relPath, name),
					EdgeType: types.EdgeTypeDefines,
				})
			}
			return false // skip children

		case "class_declaration":
			// class Name { methods... }
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				return true // continue
			}
			className := nameNode.Content(content)
			seenKey := className + ":class"
			if seen[seenKey] {
				return false // skip children
			}
			seen[seenKey] = true
			startByte := int(n.StartByte())
			endByte := n.EndByte()
			contentSum := buildContentSum(lines, startByte, className)
			result.Nodes = append(result.Nodes, types.ASTNode{
				ID:         types.GenerateNodeID(relPath, className),
				FilePath:   relPath,
				SymbolName: className,
				NodeType:   types.NodeTypeClass,
				StartByte:  n.StartByte(),
				EndByte:    endByte,
				ContentSum: contentSum,
			})
			// DEFINES edge: file → class
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID: fileNode.ID,
				TargetID: types.GenerateNodeID(relPath, className),
				EdgeType: types.EdgeTypeDefines,
			})

			// Check for extends (INHERITS edge)
			superNode := n.ChildByFieldName("superclass")
			if superNode != nil {
				parentName := superNode.Content(content)
				if parentName != "" {
					result.Edges = append(result.Edges, types.ASTEdge{
						SourceID:     types.GenerateNodeID(relPath, className),
						TargetID:     types.GenerateNodeID(relPath, parentName),
						EdgeType:     types.EdgeTypeInherits,
						TargetSymbol: parentName,
					})
				}
			}

			// Extract methods from class body
			bodyNode := n.ChildByFieldName("body")
			if bodyNode != nil {
				// M8: Collect decorators for the class itself
				for ci := 0; ci < int(n.ChildCount()); ci++ {
					deco := n.Child(ci)
					if deco != nil && deco.Type() == "decorator" {
						decoText := deco.Content(content)
						// Prepend decorator to class content summary
						classNode := findNodeBySymbolInSlice(result.Nodes, className)
						if classNode != nil {
							classNode.ContentSum = decoText + " " + classNode.ContentSum
						}
					}
				}

				for i := 0; i < int(bodyNode.ChildCount()); i++ {
					memberNode := bodyNode.Child(i)
					if memberNode == nil || !memberNode.IsNamed() {
						continue
					}
					memberType := memberNode.Type()

					// M8: Collect decorators for methods/fields
					var decoratorText string
					if memberType == "method_definition" || memberType == "field_definition" || memberType == "public_field_definition" {
						// Check preceding siblings for decorators
						for di := i - 1; di >= 0; di-- {
							prev := bodyNode.Child(di)
							if prev != nil && prev.Type() == "decorator" {
								if decoratorText == "" {
									decoratorText = prev.Content(content)
								} else {
									decoratorText = prev.Content(content) + " " + decoratorText
								}
							} else {
								break
							}
						}
					}

					switch memberType {
					case "method_definition":
						methNameNode := memberNode.ChildByFieldName("name")
						if methNameNode == nil {
							continue
						}
						methodName := methNameNode.Content(content)
						// Skip constructor and keywords
						if methodName == "constructor" || methodName == "if" || methodName == "for" ||
							methodName == "while" || methodName == "switch" || methodName == "catch" ||
							methodName == "function" {
							continue
						}
						qualifiedName := className + "." + methodName
						methSeenKey := qualifiedName + ":method"
						if seen[methSeenKey] {
							continue
						}
						seen[methSeenKey] = true
						mStartByte := int(memberNode.StartByte())
						mEndByte := memberNode.EndByte()
						mContentSum := buildContentSum(lines, mStartByte, qualifiedName)
						if decoratorText != "" {
							mContentSum = decoratorText + " " + mContentSum
						}
						result.Nodes = append(result.Nodes, types.ASTNode{
							ID:         types.GenerateNodeID(relPath, qualifiedName),
							FilePath:   relPath,
							SymbolName: qualifiedName,
							NodeType:   types.NodeTypeMethod,
							StartByte:  memberNode.StartByte(),
							EndByte:    mEndByte,
							ContentSum: mContentSum,
						})
						// DEFINES_METHOD edge: class → method
						result.Edges = append(result.Edges, types.ASTEdge{
							SourceID: types.GenerateNodeID(relPath, className),
							TargetID: types.GenerateNodeID(relPath, qualifiedName),
							EdgeType: types.EdgeTypeDefinesMethod,
						})

					case "field_definition", "public_field_definition":
						// H2: Class property arrow functions (bar = () => {})
						propNameNode := memberNode.ChildByFieldName("name")
						if propNameNode == nil {
							// Try "property" field name as alternative
							propNameNode = memberNode.ChildByFieldName("property")
						}
						if propNameNode == nil {
							continue
						}
						propValueNode := memberNode.ChildByFieldName("value")
						if propValueNode == nil {
							continue
						}
						valType := propValueNode.Type()
						if valType != "arrow_function" && valType != "function" && valType != "function_expression" {
							continue
						}
						propName := propNameNode.Content(content)
						qualifiedName := className + "." + propName
						propSeenKey := qualifiedName + ":method"
						if seen[propSeenKey] {
							continue
						}
						seen[propSeenKey] = true
						pStartByte := int(memberNode.StartByte())
						pEndByte := memberNode.EndByte()
						pContentSum := buildContentSum(lines, pStartByte, qualifiedName)
						if decoratorText != "" {
							pContentSum = decoratorText + " " + pContentSum
						}
						result.Nodes = append(result.Nodes, types.ASTNode{
							ID:         types.GenerateNodeID(relPath, qualifiedName),
							FilePath:   relPath,
							SymbolName: qualifiedName,
							NodeType:   types.NodeTypeMethod,
							StartByte:  memberNode.StartByte(),
							EndByte:    pEndByte,
							ContentSum: pContentSum,
						})
						// DEFINES_METHOD edge: class → arrow function property
						result.Edges = append(result.Edges, types.ASTEdge{
							SourceID: types.GenerateNodeID(relPath, className),
							TargetID: types.GenerateNodeID(relPath, qualifiedName),
							EdgeType: types.EdgeTypeDefinesMethod,
						})
					}
				}
			}
			return false // skip children

		case "export_statement":
			// export class/function/interface/enum/type — recurse into children
			return true // continue

		// L17: TypeScript-specific constructs
		case "interface_declaration":
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				return true // continue
			}
			name := nameNode.Content(content)
			seenKey := name + ":interface"
			if seen[seenKey] {
				return false // skip children
			}
			seen[seenKey] = true
			startByte := int(n.StartByte())
			endByte := n.EndByte()
			contentSum := buildContentSum(lines, startByte, name)
			result.Nodes = append(result.Nodes, types.ASTNode{
				ID:         types.GenerateNodeID(relPath, name),
				FilePath:   relPath,
				SymbolName: name,
				NodeType:   types.NodeTypeInterface,
				StartByte:  n.StartByte(),
				EndByte:    endByte,
				ContentSum: contentSum,
			})
			// DEFINES edge: file → interface
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID: fileNode.ID,
				TargetID: types.GenerateNodeID(relPath, name),
				EdgeType: types.EdgeTypeDefines,
			})
			return false // skip children

		case "enum_declaration":
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				return true // continue
			}
			name := nameNode.Content(content)
			seenKey := name + ":enum"
			if seen[seenKey] {
				return false // skip children
			}
			seen[seenKey] = true
			startByte := int(n.StartByte())
			endByte := n.EndByte()
			contentSum := buildContentSum(lines, startByte, name)
			result.Nodes = append(result.Nodes, types.ASTNode{
				ID:         types.GenerateNodeID(relPath, name),
				FilePath:   relPath,
				SymbolName: name,
				NodeType:   types.NodeTypeStruct,
				StartByte:  n.StartByte(),
				EndByte:    endByte,
				ContentSum: contentSum,
			})
			// DEFINES edge: file → enum
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID: fileNode.ID,
				TargetID: types.GenerateNodeID(relPath, name),
				EdgeType: types.EdgeTypeDefines,
			})
			return false // skip children

		case "type_alias_declaration":
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				return true // continue
			}
			name := nameNode.Content(content)
			seenKey := name + ":type_alias"
			if seen[seenKey] {
				return false // skip children
			}
			seen[seenKey] = true
			startByte := int(n.StartByte())
			endByte := n.EndByte()
			contentSum := buildContentSum(lines, startByte, name)
			result.Nodes = append(result.Nodes, types.ASTNode{
				ID:         types.GenerateNodeID(relPath, name),
				FilePath:   relPath,
				SymbolName: name,
				NodeType:   types.NodeTypeFunction,
				StartByte:  n.StartByte(),
				EndByte:    endByte,
				ContentSum: contentSum,
			})
			// DEFINES edge: file → type alias
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID: fileNode.ID,
				TargetID: types.GenerateNodeID(relPath, name),
				EdgeType: types.EdgeTypeDefines,
			})
			return false // skip children
		}

		return true // continue
	})

	// M12: Collect string literal ranges to filter out false regex matches
	jsStringRanges := collectStringRanges(root)

	// Extract call edges using regex on node bodies (M9: deduplicate)
	callSeen := map[string]bool{}
	for i, node := range result.Nodes {
		if node.NodeType == types.NodeTypeFile || node.NodeType == types.NodeTypeClass || node.NodeType == types.NodeTypeStruct || node.NodeType == types.NodeTypeInterface {
			continue
		}
		if node.EndByte > uint32(len(content)) {
			continue
		}
		bodyText := string(content[node.StartByte:node.EndByte])
		for _, matchIndices := range jsCallExprRe.FindAllStringSubmatchIndex(bodyText, -1) {
			// matchIndices[2] and matchIndices[3] are the start/end of capture group 1
			if len(matchIndices) < 4 {
				continue
			}
			target := bodyText[matchIndices[2]:matchIndices[3]]
			// M12: Skip matches inside string literals
			absOffset := node.StartByte + uint32(matchIndices[2])
			if isInStringRange(absOffset, jsStringRanges) {
				continue
			}
			if isJSKeyword(target) || target == node.SymbolName || target == extractBaseName(node.SymbolName) {
				continue
			}
			targetID := types.GenerateNodeID(relPath, target)
			edgeKey := result.Nodes[i].ID + ":" + targetID
			if callSeen[edgeKey] {
				continue
			}
			callSeen[edgeKey] = true
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID: result.Nodes[i].ID,
				TargetID: targetID,
				EdgeType: types.EdgeTypeCalls,
			})
		}
	}

	// H1: Extract import edges from tree-sitter nodes
	walkTree(root, func(n *sitter.Node) bool {
		if !n.IsNamed() {
			return true // continue
		}
		nodeType := n.Type()

		switch nodeType {
		case "import_statement":
			// import ... from 'module' — find the string source child
			sourceNode := n.ChildByFieldName("source")
			if sourceNode != nil {
				modulePath := extractStringContent(sourceNode, content)
				if modulePath != "" {
					result.Edges = append(result.Edges, types.ASTEdge{
						SourceID: types.GenerateNodeID(relPath, relPath),
						TargetID: types.GenerateNodeID(modulePath, modulePath),
						EdgeType: types.EdgeTypeImports,
					})
				}
				return false // skip children
			}
			// Fallback: walk children to find the string node
			for i := 0; i < int(n.ChildCount()); i++ {
				child := n.Child(i)
				if child == nil {
					continue
				}
				childType := child.Type()
				if childType == "string" || childType == "string_fragment" {
					modulePath := extractStringContent(child, content)
					if modulePath != "" {
						result.Edges = append(result.Edges, types.ASTEdge{
							SourceID: types.GenerateNodeID(relPath, relPath),
							TargetID: types.GenerateNodeID(modulePath, modulePath),
							EdgeType: types.EdgeTypeImports,
						})
					}
					break
				}
			}
			return false // skip children

		case "call_expression":
			// require('module')
			fnNode := n.ChildByFieldName("function")
			if fnNode != nil && fnNode.Content(content) == "require" {
				argsNode := n.ChildByFieldName("arguments")
				if argsNode != nil {
					for i := 0; i < int(argsNode.ChildCount()); i++ {
						argChild := argsNode.Child(i)
						if argChild == nil || !argChild.IsNamed() {
							continue
						}
						argType := argChild.Type()
						if argType == "string" || argType == "string_fragment" {
							modulePath := extractStringContent(argChild, content)
							if modulePath != "" {
								result.Edges = append(result.Edges, types.ASTEdge{
									SourceID: types.GenerateNodeID(relPath, relPath),
									TargetID: types.GenerateNodeID(modulePath, modulePath),
									EdgeType: types.EdgeTypeImports,
								})
							}
							break
						}
					}
				}
			}
			return true // continue
		}

		return true // continue
	})

	return result, nil
}

// extractStringContent extracts the text content from a string node,
// stripping quotes if present.
func extractStringContent(n *sitter.Node, source []byte) string {
	if n == nil {
		return ""
	}
	text := n.Content(source)
	// If the node contains a string_fragment child, use that
	for i := 0; i < int(n.ChildCount()); i++ {
		child := n.Child(i)
		if child != nil && child.IsNamed() {
			childText := child.Content(source)
			if childText != "" {
				return childText
			}
		}
	}
	// Otherwise strip quotes from the string literal
	text = strings.TrimPrefix(text, "'")
	text = strings.TrimSuffix(text, "'")
	text = strings.TrimPrefix(text, "\"")
	text = strings.TrimSuffix(text, "\"")
	return text
}

// PHP regex patterns (kept for edge extraction and independent helper tests)
var (
	phpClassDeclRe    = regexp.MustCompile(`(?m)(?:^|\n)\s*(?:abstract\s+)?class\s+(\w+)`)
	phpMethodDeclRe   = regexp.MustCompile(`(?m)^[ \t]+(?:(?:public|protected|private)\s+)?(?:static\s+)?function\s+(\w+)\s*\(`)
	phpFuncDeclRe     = regexp.MustCompile(`(?m)(?:^|\n)\s*function\s+(\w+)\s*\(`)
	phpNewExprRe      = regexp.MustCompile(`new\s+(\w+)\s*\(`)
	phpUseRe          = regexp.MustCompile(`(?m)^use\s+([\w\\]+)`)
	phpMethodCallRe   = regexp.MustCompile(`(\$this|\$\w+|self|static|parent)\s*(?:->|::)\s*(\w+)\s*\(`)
	phpStaticCallRe   = regexp.MustCompile(`([A-Z]\w+)\s*::\s*(\w+)\s*\(`)
	phpFuncCallRe     = regexp.MustCompile(`(?:^|[^>\w])(\w+)\s*\(`)
)

// phpCallKeywords are PHP keywords/constructs that look like function calls but aren't
var phpCallKeywords = map[string]bool{
	"if": true, "else": true, "elseif": true, "for": true, "foreach": true,
	"while": true, "do": true, "switch": true, "case": true, "catch": true,
	"return": true, "echo": true, "print": true, "throw": true, "try": true,
	"finally": true, "new": true, "class": true, "function": true, "array": true,
	"list": true, "isset": true, "unset": true, "empty": true, "die": true,
	"exit": true, "include": true, "require": true, "include_once": true,
	"require_once": true, "use": true, "namespace": true, "public": true,
	"private": true, "protected": true, "static": true, "abstract": true,
	"true": true, "false": true, "null": true, "self": true, "parent": true,
}

// parsePHP uses tree-sitter for PHP file parsing.
func (p *Parser) parsePHP(content []byte, relPath string) (*ParseResult, error) {
	result := &ParseResult{}
	lines := strings.Split(string(content), "\n")

	// C1: Create file-level node so import edges have a valid source node
	fileNode := types.ASTNode{
		ID:         types.GenerateNodeID(relPath, relPath),
		FilePath:   relPath,
		SymbolName: relPath,
		NodeType:   types.NodeTypeFile,
		StartByte:  0,
		EndByte:    uint32(len(content)),
		ContentSum: relPath,
	}
	result.Nodes = append(result.Nodes, fileNode)

	phpLang := php.GetLanguage()
	phpParser := sitter.NewParser()
	defer phpParser.Close()
	phpParser.SetLanguage(phpLang)
	tree, err := phpParser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		log.Printf("parser: tree-sitter failed for %s: %v — returning file-level node only", relPath, err)
		return result, nil
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil {
		log.Printf("parser: tree-sitter returned nil root for %s — returning file-level node only", relPath)
		return result, nil
	}

	// C11: Track seen names to avoid duplicate nodes
	seen := map[string]bool{}

	// Walk tree to extract classes, methods, functions, use statements
	walkTree(root, func(n *sitter.Node) bool {
		if !n.IsNamed() {
			return true // continue
		}
		nodeType := n.Type()

		switch nodeType {
		case "class_declaration", "trait_declaration":
			// M1: Handle both class and trait declarations
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				return true // continue
			}
			className := nameNode.Content(content)
			seenKey := className + ":class"
			if seen[seenKey] {
				return false // skip children
			}
			seen[seenKey] = true
			startByte := int(n.StartByte())
			endByte := n.EndByte()
			contentSum := buildContentSum(lines, startByte, className)

			result.Nodes = append(result.Nodes, types.ASTNode{
				ID:         types.GenerateNodeID(relPath, className),
				FilePath:   relPath,
				SymbolName: className,
				NodeType:   types.NodeTypeClass,
				StartByte:  n.StartByte(),
				EndByte:    endByte,
				ContentSum: contentSum,
			})
			// DEFINES edge: file → class/trait
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID: fileNode.ID,
				TargetID: types.GenerateNodeID(relPath, className),
				EdgeType: types.EdgeTypeDefines,
			})

			// Check for extends and implements
			for ci := 0; ci < int(n.ChildCount()); ci++ {
				child := n.Child(ci)
				if child == nil || !child.IsNamed() {
					continue
				}
				if child.Type() == "base_clause" {
					for cj := 0; cj < int(child.ChildCount()); cj++ {
						nameChild := child.Child(cj)
						if nameChild != nil && nameChild.IsNamed() && (nameChild.Type() == "name" || nameChild.Type() == "qualified_name") {
							parentName := nameChild.Content(content)
							if idx := strings.LastIndex(parentName, "\\"); idx >= 0 {
								parentName = parentName[idx+1:]
							}
							if parentName != "" {
								result.Edges = append(result.Edges, types.ASTEdge{
									SourceID:     types.GenerateNodeID(relPath, className),
									TargetID:     types.GenerateNodeID(relPath, parentName),
									EdgeType:     types.EdgeTypeInherits,
									TargetSymbol: parentName,
								})
							}
							break
						}
					}
				}
				if child.Type() == "class_interface_clause" {
					for cj := 0; cj < int(child.ChildCount()); cj++ {
						ifaceChild := child.Child(cj)
						if ifaceChild != nil && ifaceChild.IsNamed() && (ifaceChild.Type() == "name" || ifaceChild.Type() == "qualified_name") {
							ifaceName := ifaceChild.Content(content)
							if idx := strings.LastIndex(ifaceName, "\\"); idx >= 0 {
								ifaceName = ifaceName[idx+1:]
							}
							if ifaceName != "" {
								result.Edges = append(result.Edges, types.ASTEdge{
									SourceID:     types.GenerateNodeID(relPath, className),
									TargetID:     types.GenerateNodeID(relPath, ifaceName),
									EdgeType:     types.EdgeTypeImplements,
									TargetSymbol: ifaceName,
								})
							}
						}
					}
				}
			}

			// Extract methods from declaration_list body
			bodyNode := n.ChildByFieldName("body")
			if bodyNode != nil {
				for i := 0; i < int(bodyNode.ChildCount()); i++ {
					methodNode := bodyNode.Child(i)
					if methodNode == nil || !methodNode.IsNamed() {
						continue
					}
					if methodNode.Type() != "method_declaration" {
						continue
					}
					methNameNode := methodNode.ChildByFieldName("name")
					if methNameNode == nil {
						continue
					}
					methodName := methNameNode.Content(content)
					qualifiedName := className + "." + methodName
					methSeenKey := qualifiedName + ":method"
					if seen[methSeenKey] {
						continue
					}
					seen[methSeenKey] = true
					mStartByte := int(methodNode.StartByte())
					mEndByte := methodNode.EndByte()
					mContentSum := buildContentSum(lines, mStartByte, qualifiedName)
					result.Nodes = append(result.Nodes, types.ASTNode{
						ID:         types.GenerateNodeID(relPath, qualifiedName),
						FilePath:   relPath,
						SymbolName: qualifiedName,
						NodeType:   types.NodeTypeMethod,
						StartByte:  methodNode.StartByte(),
						EndByte:    mEndByte,
						ContentSum: mContentSum,
					})
					// DEFINES_METHOD edge: class → method
					result.Edges = append(result.Edges, types.ASTEdge{
						SourceID: types.GenerateNodeID(relPath, className),
						TargetID: types.GenerateNodeID(relPath, qualifiedName),
						EdgeType: types.EdgeTypeDefinesMethod,
					})
				}
			}
			return false // skip children

		case "function_definition":
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				return true // continue
			}
			name := nameNode.Content(content)
			seenKey := name + ":function"
			if seen[seenKey] {
				return false // skip children
			}
			seen[seenKey] = true
			startByte := int(n.StartByte())
			endByte := n.EndByte()
			contentSum := buildContentSum(lines, startByte, name)
			result.Nodes = append(result.Nodes, types.ASTNode{
				ID:         types.GenerateNodeID(relPath, name),
				FilePath:   relPath,
				SymbolName: name,
				NodeType:   types.NodeTypeFunction,
				StartByte:  n.StartByte(),
				EndByte:    endByte,
				ContentSum: contentSum,
			})
			// DEFINES edge: file → function
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID: fileNode.ID,
				TargetID: types.GenerateNodeID(relPath, name),
				EdgeType: types.EdgeTypeDefines,
			})
			return false // skip children

		case "namespace_use_declaration":
			// use App\Models\User;
			for i := 0; i < int(n.ChildCount()); i++ {
				child := n.Child(i)
				if child == nil || !child.IsNamed() {
					continue
				}
				if child.Type() == "namespace_use_clause" {
					usePath := child.Content(content)
					if usePath != "" {
						result.Edges = append(result.Edges, types.ASTEdge{
							SourceID: types.GenerateNodeID(relPath, relPath),
							TargetID: types.GenerateNodeID(usePath, usePath),
							EdgeType: types.EdgeTypeImports,
						})
					}
				}
			}
			return false // skip children
		}

		return true // continue
	})

	// M12: Collect string literal ranges to filter out false regex matches in PHP
	phpStringRanges := collectStringRanges(root)

	// H1: Build parent class lookup from INHERITS edges for parent:: resolution
	parentClassMap := map[string]string{} // childClass -> parentClass
	for _, e := range result.Edges {
		if e.EdgeType == types.EdgeTypeInherits && e.TargetSymbol != "" {
			// Find the child class from its node ID
			for _, nd := range result.Nodes {
				if nd.ID == e.SourceID && nd.NodeType == types.NodeTypeClass {
					parentClassMap[nd.SymbolName] = e.TargetSymbol
					break
				}
			}
		}
	}

	// Extract instantiation edges and call edges using regex on node bodies
	// M9: deduplicate call edges
	callSeen := map[string]bool{}
	for _, node := range result.Nodes {
		if node.NodeType == types.NodeTypeFile || node.NodeType == types.NodeTypeClass || node.NodeType == types.NodeTypeStruct || node.NodeType == types.NodeTypeInterface {
			continue
		}
		if node.EndByte > uint32(len(content)) {
			continue
		}
		bodyText := string(content[node.StartByte:node.EndByte])

		// Instantiation edges: new ClassName()
		for _, matchIdx := range phpNewExprRe.FindAllStringSubmatchIndex(bodyText, -1) {
			if len(matchIdx) < 4 {
				continue
			}
			target := bodyText[matchIdx[2]:matchIdx[3]]
			// M12: Skip matches inside string literals
			if isInStringRange(node.StartByte+uint32(matchIdx[2]), phpStringRanges) {
				continue
			}
			targetID := types.GenerateNodeID(relPath, target)
			edgeKey := node.ID + ":inst:" + targetID
			if callSeen[edgeKey] {
				continue
			}
			callSeen[edgeKey] = true
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID: node.ID,
				TargetID: targetID,
				EdgeType: types.EdgeTypeInstantiates,
			})
		}

		// M5: Method call edges — $this->method(), $obj->method(), self::method()
		// Determine enclosing class from the node's SymbolName (e.g., "User.save" -> "User")
		var enclosingClass string
		if node.NodeType == types.NodeTypeMethod {
			if dotIdx := strings.Index(node.SymbolName, "."); dotIdx >= 0 {
				enclosingClass = node.SymbolName[:dotIdx]
			}
		}
		for _, matchIdx := range phpMethodCallRe.FindAllStringSubmatchIndex(bodyText, -1) {
			if len(matchIdx) < 6 {
				continue
			}
			caller := bodyText[matchIdx[2]:matchIdx[3]]     // "$this", "$obj", "self", "static", "parent"
			methodName := bodyText[matchIdx[4]:matchIdx[5]]  // bare method name
			// M12: Skip matches inside string literals
			if isInStringRange(node.StartByte+uint32(matchIdx[0]), phpStringRanges) {
				continue
			}
			if phpCallKeywords[methodName] {
				continue
			}
			// H1: When caller is "parent", resolve to the parent class name
			isSelfCall := caller == "$this" || caller == "self" || caller == "static"
			isParentCall := caller == "parent"
			var target string
			var targetSymbol string
			if isParentCall && enclosingClass != "" {
				parentClass := parentClassMap[enclosingClass]
				if parentClass != "" {
					target = parentClass + "." + methodName
				} else {
					// No parent found in this file — use TargetSymbol for cross-file resolution
					target = methodName
					targetSymbol = "parent." + methodName
				}
			} else if isSelfCall && enclosingClass != "" {
				// $this->method() / self::method() — qualify with enclosing class
				target = enclosingClass + "." + methodName
			} else {
				// $obj->method() — use bare name for local lookup, TargetSymbol for cross-file
				target = methodName
				targetSymbol = methodName
			}
			targetID := types.GenerateNodeID(relPath, target)
			edgeKey := node.ID + ":" + targetID
			if callSeen[edgeKey] {
				continue
			}
			callSeen[edgeKey] = true
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID:     node.ID,
				TargetID:     targetID,
				EdgeType:     types.EdgeTypeCalls,
				TargetSymbol: targetSymbol,
			})
		}

		// M5: Static call edges — ClassName::method()
		for _, matchIdx := range phpStaticCallRe.FindAllStringSubmatchIndex(bodyText, -1) {
			if len(matchIdx) < 6 {
				continue
			}
			className := bodyText[matchIdx[2]:matchIdx[3]]
			methodName := bodyText[matchIdx[4]:matchIdx[5]]
			// M12: Skip matches inside string literals
			if isInStringRange(node.StartByte+uint32(matchIdx[0]), phpStringRanges) {
				continue
			}
			target := className + "." + methodName
			targetID := types.GenerateNodeID(relPath, target)
			edgeKey := node.ID + ":" + targetID
			if callSeen[edgeKey] {
				continue
			}
			callSeen[edgeKey] = true
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID: node.ID,
				TargetID: targetID,
				EdgeType: types.EdgeTypeCalls,
			})
		}

		// M5: Plain function call edges — functionName()
		if node.NodeType == types.NodeTypeFunction || node.NodeType == types.NodeTypeMethod {
			for _, matchIdx := range phpFuncCallRe.FindAllStringSubmatchIndex(bodyText, -1) {
				if len(matchIdx) < 4 {
					continue
				}
				target := bodyText[matchIdx[2]:matchIdx[3]]
				// M12: Skip matches inside string literals
				if isInStringRange(node.StartByte+uint32(matchIdx[2]), phpStringRanges) {
					continue
				}
				if phpCallKeywords[target] || target == extractBaseName(node.SymbolName) {
					continue
				}
				targetID := types.GenerateNodeID(relPath, target)
				edgeKey := node.ID + ":" + targetID
				if callSeen[edgeKey] {
					continue
				}
				callSeen[edgeKey] = true
				result.Edges = append(result.Edges, types.ASTEdge{
					SourceID: node.ID,
					TargetID: targetID,
					EdgeType: types.EdgeTypeCalls,
				})
			}
		}
	}

	return result, nil
}

// byteRange represents a start/end byte range for string literal filtering.
type byteRange struct {
	start, end uint32
}

// collectStringRanges walks a tree-sitter AST and collects the byte ranges of all
// string literal nodes, used for filtering out false regex matches inside strings (M12).
func collectStringRanges(root *sitter.Node) []byteRange {
	var ranges []byteRange
	walkTree(root, func(n *sitter.Node) bool {
		if !n.IsNamed() {
			return true
		}
		nt := n.Type()
		if nt == "string" || nt == "string_fragment" || nt == "template_string" ||
			nt == "encapsed_string" || nt == "string_value" || nt == "heredoc" {
			ranges = append(ranges, byteRange{n.StartByte(), n.EndByte()})
			return false // no need to recurse into string children
		}
		return true
	})
	return ranges
}

// isInStringRange checks if a byte offset falls within any of the given string ranges.
func isInStringRange(offset uint32, ranges []byteRange) bool {
	for _, r := range ranges {
		if offset >= r.start && offset < r.end {
			return true
		}
	}
	return false
}

// findNodeBySymbolInSlice returns a pointer to the first node with the given SymbolName, or nil.
func findNodeBySymbolInSlice(nodes []types.ASTNode, name string) *types.ASTNode {
	for i := range nodes {
		if nodes[i].SymbolName == name {
			return &nodes[i]
		}
	}
	return nil
}

// extractBaseName returns the part after the last "." in a qualified name,
// or the whole name if there is no ".".
func extractBaseName(name string) string {
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

// findBlockEnd finds the matching closing brace for a code block.
// It uses a state machine to correctly skip braces inside strings,
// comments, regex literals, and template literals.
func findBlockEnd(content []byte, startPos int) uint32 {
	type scanState int
	const (
		stateCode scanState = iota
		stateSingleQuote
		stateDoubleQuote
		stateBacktick // JS template literal
		stateLineComment
		stateBlockComment
		stateRegex
	)

	state := stateCode
	depth := 0
	started := false
	n := len(content)

	for i := startPos; i < n; i++ {
		ch := content[i]

		switch state {
		case stateCode:
			switch ch {
			case '\'':
				state = stateSingleQuote
			case '"':
				state = stateDoubleQuote
			case '`':
				state = stateBacktick
			case '/':
				if i+1 < n {
					if content[i+1] == '/' {
						state = stateLineComment
						i++ // skip second /
					} else if content[i+1] == '*' {
						state = stateBlockComment
						i++ // skip *
					} else if looksLikeRegex(content, i) {
						state = stateRegex
					}
				}
			case '{':
				depth++
				started = true
			case '}':
				depth--
				if started && depth == 0 {
					return uint32(i + 1)
				}
			}

		case stateSingleQuote:
			if ch == '\\' && i+1 < n {
				i++ // skip escaped character
			} else if ch == '\'' {
				state = stateCode
			}

		case stateDoubleQuote:
			if ch == '\\' && i+1 < n {
				i++ // skip escaped character
			} else if ch == '"' {
				state = stateCode
			}

		case stateBacktick:
			if ch == '\\' && i+1 < n {
				i++ // skip escaped character
			} else if ch == '`' {
				state = stateCode
			} else if ch == '$' && i+1 < n && content[i+1] == '{' {
				// H4: Template literal interpolation ${...}: track nested braces
				i++ // skip {
				braceDepth := 1
				for i++; i < n && braceDepth > 0; i++ {
					switch content[i] {
					case '{':
						braceDepth++
					case '}':
						braceDepth--
					case '\\':
						i++ // skip escaped char
					}
				}
				i-- // outer for loop will increment
			}

		case stateLineComment:
			if ch == '\n' {
				state = stateCode
			}

		case stateBlockComment:
			if ch == '*' && i+1 < n && content[i+1] == '/' {
				state = stateCode
				i++ // skip /
			}

		case stateRegex:
			if ch == '\\' && i+1 < n {
				i++ // skip escaped character
			} else if ch == '/' {
				state = stateCode
			} else if ch == '\n' {
				// regex can't span lines — treat as end
				state = stateCode
			}
		}
	}

	// M10: If no matching brace found, return end of content
	return uint32(len(content))
}

// looksLikeRegex determines if a '/' at position i is likely the start of a
// regex literal rather than a division operator, by looking at the preceding
// non-whitespace character.
func looksLikeRegex(content []byte, i int) bool {
	// Walk backwards to find the previous non-whitespace character
	for j := i - 1; j >= 0; j-- {
		ch := content[j]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			continue
		}
		// After these characters, '/' starts a regex:
		// = ( [ { , ; ! & | ? : ~ ^ % + - * < > \n (start of statement)
		switch ch {
		case '=', '(', '[', '{', ',', ';', '!', '&', '|', '?', ':', '~', '^', '%', '+', '-', '*', '<', '>':
			return true
		}
		// After a keyword like return/typeof/in/instanceof, '/' starts a regex
		// but after an identifier or ')' or ']' or a number, it's division
		return false
	}
	// At start of content, '/' is a regex
	return true
}

// buildContentSum creates a summary from the declaration and any preceding
// doc block (JSDoc /** ... */, PHPDoc, or consecutive // comments).
func buildContentSum(lines []string, byteOffset int, name string) string {
	// Find the line number for this byte offset
	// M11: strings.Split on "\n" preserves \r from \r\n endings in each line element,
	// so len(line)+1 correctly accounts for both \n and \r\n line endings.
	currentOffset := 0
	for i, line := range lines {
		nextOffset := currentOffset + len(line) + 1 // +1 for newline
		if currentOffset <= byteOffset && byteOffset < nextOffset {
			// Walk backwards to capture the full doc comment block
			docLines := collectDocBlock(lines, i)
			if len(docLines) > 0 {
				return strings.TrimSpace(strings.Join(docLines, " ") + " " + name)
			}
			return name
		}
		currentOffset = nextOffset
	}
	return name
}

// collectDocBlock walks backwards from the line before declLine, collecting
// contiguous comment lines that form a doc block. It handles:
//   - JSDoc/PHPDoc blocks: /** ... */
//   - Single-line comments: // ...
//
// Returns the collected comment text lines (in top-to-bottom order).
func collectDocBlock(lines []string, declLine int) []string {
	if declLine <= 0 {
		return nil
	}

	// M14: If the line immediately before the declaration is blank,
	// don't capture a doc block from further above
	prevLine := strings.TrimSpace(lines[declLine-1])
	if prevLine == "" {
		return nil
	}

	var collected []string
	j := declLine - 1

	// Check if we're at the end of a block comment (line contains */)
	trimmed := strings.TrimSpace(lines[j])
	if strings.HasSuffix(trimmed, "*/") || trimmed == "*/" {
		// Walk backwards through the block comment to find /**
		for j >= 0 {
			line := strings.TrimSpace(lines[j])
			collected = append([]string{cleanCommentLine(line)}, collected...)
			if strings.HasPrefix(line, "/**") || strings.HasPrefix(line, "/*") {
				break
			}
			j--
		}
		return collected
	}

	// Check for single-line comment block (// lines only)
	// M7: Only collect lines that are actual comments, not stray *-prefixed lines
	for j >= 0 {
		line := strings.TrimSpace(lines[j])
		if strings.HasPrefix(line, "//") {
			collected = append([]string{cleanCommentLine(line)}, collected...)
			j--
		} else if strings.HasPrefix(line, "* ") || line == "*" || strings.HasPrefix(line, "/**") || strings.HasPrefix(line, "/*") {
			// Inside a block comment — only match lines that look like doc comment content
			// " * text" or bare "*" (common in multi-line block comments)
			collected = append([]string{cleanCommentLine(line)}, collected...)
			if strings.HasPrefix(line, "/**") || strings.HasPrefix(line, "/*") {
				break
			}
			j--
		} else {
			break
		}
	}

	return collected
}

// cleanCommentLine strips comment prefix characters from a line.
func cleanCommentLine(line string) string {
	line = strings.TrimSpace(line)
	// Strip block comment markers
	line = strings.TrimPrefix(line, "/**")
	line = strings.TrimPrefix(line, "/*")
	line = strings.TrimSuffix(line, "*/")
	// Strip leading * (common in multi-line block comments)
	line = strings.TrimPrefix(line, "*")
	// Strip // prefix
	line = strings.TrimPrefix(line, "//")
	return strings.TrimSpace(line)
}

// jsKeywords is the set of common JS keywords and builtins used to filter call edges.
var jsKeywords = map[string]bool{
	"if": true, "else": true, "for": true, "while": true, "do": true,
	"switch": true, "case": true, "break": true, "continue": true,
	"return": true, "throw": true, "try": true, "catch": true, "finally": true,
	"new": true, "delete": true, "typeof": true, "void": true, "instanceof": true,
	"var": true, "let": true, "const": true, "function": true, "class": true,
	"import": true, "export": true, "default": true, "from": true,
	"async": true, "await": true, "yield": true,
	"true": true, "false": true, "null": true, "undefined": true,
	"this": true, "super": true, "console": true, "require": true,
	"setTimeout": true, "setInterval": true, "Promise": true,
	"Array": true, "Object": true, "String": true, "Number": true,
	"Math": true, "JSON": true, "Date": true, "Error": true,
	"Map": true, "Set": true, "RegExp": true,
}

// isJSKeyword returns true if the name is a common JS keyword/builtin
func isJSKeyword(name string) bool {
	return jsKeywords[name]
}

