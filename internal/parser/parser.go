package parser

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/naman/qb-context/internal/types"
)

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
	// Check file size to prevent memory bloat (skip files > 5MB)
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("stat file %s: %w", filePath, err)
	}
	if info.Size() > 5*1024*1024 {
		return nil, fmt.Errorf("file %s too large (%d bytes), skipping", filePath, info.Size())
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading file %s: %w", filePath, err)
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

// parseGo uses Go's native AST parser for accurate Go file parsing
func (p *Parser) parseGo(content []byte, relPath string) (*ParseResult, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, relPath, content, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing Go file: %w", err)
	}

	result := &ParseResult{}

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

			// Build content summary from signature + doc comment
			contentSum := name
			if decl.Doc != nil {
				contentSum = decl.Doc.Text() + " " + name
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

			// Extract function calls within the body
			if decl.Body != nil {
				calls := extractGoCalls(decl.Body)
				for _, call := range calls {
					edge := types.ASTEdge{
						SourceID: node.ID,
						TargetID: types.GenerateNodeID(relPath, call),
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
						nodeType = types.NodeTypeClass // Use Class for interfaces
					default:
						continue
					}

					startByte := uint32(fset.Position(decl.Pos()).Offset)
					endByte := uint32(fset.Position(decl.End()).Offset)

					contentSum := name
					if decl.Doc != nil {
						contentSum = decl.Doc.Text() + " " + name
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
				}
			}
		}
		return true
	})

	return result, nil
}

// extractReceiverType gets the type name from a method receiver
func extractReceiverType(recv *ast.FieldList) string {
	if recv == nil || recv.NumFields() == 0 {
		return ""
	}
	field := recv.List[0]
	switch t := field.Type.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return ident.Name
		}
	}
	return ""
}

// extractGoCalls finds function/method calls within a Go AST block
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
			if ident, ok := fn.X.(*ast.Ident); ok {
				calls = append(calls, ident.Name+"."+fn.Sel.Name)
			}
		}
		return true
	})
	return calls
}

// JavaScript/TypeScript regex patterns
var (
	jsFuncDeclRe  = regexp.MustCompile(`(?m)^(?:export\s+)?(?:async\s+)?function\s+(\w+)\s*\(`)
	jsArrowFuncRe = regexp.MustCompile(`(?m)^(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\(`)
	jsClassDeclRe = regexp.MustCompile(`(?m)^(?:export\s+)?class\s+(\w+)`)
	jsCallExprRe  = regexp.MustCompile(`(?:^|[^.\w])(\w+)\s*\(`)
)

// parseJavaScript uses regex-based extraction for JS/TS files
func (p *Parser) parseJavaScript(content []byte, relPath string) (*ParseResult, error) {
	result := &ParseResult{}
	text := string(content)
	lines := strings.Split(text, "\n")

	// Extract function declarations
	for _, match := range jsFuncDeclRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		startByte := uint32(match[0])
		endByte := findBlockEnd(content, match[0])
		contentSum := buildContentSum(lines, match[0], name)

		result.Nodes = append(result.Nodes, types.ASTNode{
			ID:         types.GenerateNodeID(relPath, name),
			FilePath:   relPath,
			SymbolName: name,
			NodeType:   types.NodeTypeFunction,
			StartByte:  startByte,
			EndByte:    endByte,
			ContentSum: contentSum,
		})
	}

	// Extract arrow functions
	for _, match := range jsArrowFuncRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		startByte := uint32(match[0])
		endByte := findBlockEnd(content, match[0])
		contentSum := buildContentSum(lines, match[0], name)

		result.Nodes = append(result.Nodes, types.ASTNode{
			ID:         types.GenerateNodeID(relPath, name),
			FilePath:   relPath,
			SymbolName: name,
			NodeType:   types.NodeTypeFunction,
			StartByte:  startByte,
			EndByte:    endByte,
			ContentSum: contentSum,
		})
	}

	// Extract classes
	for _, match := range jsClassDeclRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		startByte := uint32(match[0])
		endByte := findBlockEnd(content, match[0])
		contentSum := buildContentSum(lines, match[0], name)

		result.Nodes = append(result.Nodes, types.ASTNode{
			ID:         types.GenerateNodeID(relPath, name),
			FilePath:   relPath,
			SymbolName: name,
			NodeType:   types.NodeTypeClass,
			StartByte:  startByte,
			EndByte:    endByte,
			ContentSum: contentSum,
		})
	}

	// Extract call edges (connecting nodes found in this file to their calls)
	for i, node := range result.Nodes {
		bodyText := string(content[node.StartByte:node.EndByte])
		for _, callMatch := range jsCallExprRe.FindAllStringSubmatch(bodyText, -1) {
			target := callMatch[1]
			// Skip common keywords
			if isJSKeyword(target) || target == node.SymbolName {
				continue
			}
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID: result.Nodes[i].ID,
				TargetID: types.GenerateNodeID(relPath, target),
				EdgeType: types.EdgeTypeCalls,
			})
		}
	}

	return result, nil
}

// PHP regex patterns
var (
	phpClassDeclRe  = regexp.MustCompile(`(?m)^(?:abstract\s+)?class\s+(\w+)`)
	phpMethodDeclRe = regexp.MustCompile(`(?m)^\s+(?:public|protected|private)\s+(?:static\s+)?function\s+(\w+)\s*\(`)
	phpFuncDeclRe   = regexp.MustCompile(`(?m)^function\s+(\w+)\s*\(`)
	phpNewExprRe    = regexp.MustCompile(`new\s+(\w+)\s*\(`)
)

// parsePHP uses regex-based extraction for PHP files
func (p *Parser) parsePHP(content []byte, relPath string) (*ParseResult, error) {
	result := &ParseResult{}
	text := string(content)
	lines := strings.Split(text, "\n")

	// Extract classes
	for _, match := range phpClassDeclRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		startByte := uint32(match[0])
		endByte := findBlockEnd(content, match[0])
		contentSum := buildContentSum(lines, match[0], name)

		result.Nodes = append(result.Nodes, types.ASTNode{
			ID:         types.GenerateNodeID(relPath, name),
			FilePath:   relPath,
			SymbolName: name,
			NodeType:   types.NodeTypeClass,
			StartByte:  startByte,
			EndByte:    endByte,
			ContentSum: contentSum,
		})
	}

	// Extract methods — determine which class they belong to by byte position
	for _, match := range phpMethodDeclRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		methodStart := uint32(match[0])

		// Find the enclosing class by checking byte ranges
		qualifiedName := name
		for _, node := range result.Nodes {
			if node.NodeType == types.NodeTypeClass && methodStart >= node.StartByte && methodStart < node.EndByte {
				qualifiedName = node.SymbolName + "." + name
				break
			}
		}

		startByte := uint32(match[0])
		endByte := findBlockEnd(content, match[0])
		contentSum := buildContentSum(lines, match[0], qualifiedName)

		result.Nodes = append(result.Nodes, types.ASTNode{
			ID:         types.GenerateNodeID(relPath, qualifiedName),
			FilePath:   relPath,
			SymbolName: qualifiedName,
			NodeType:   types.NodeTypeMethod,
			StartByte:  startByte,
			EndByte:    endByte,
			ContentSum: contentSum,
		})
	}

	// Extract standalone functions
	for _, match := range phpFuncDeclRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		startByte := uint32(match[0])
		endByte := findBlockEnd(content, match[0])
		contentSum := buildContentSum(lines, match[0], name)

		result.Nodes = append(result.Nodes, types.ASTNode{
			ID:         types.GenerateNodeID(relPath, name),
			FilePath:   relPath,
			SymbolName: name,
			NodeType:   types.NodeTypeFunction,
			StartByte:  startByte,
			EndByte:    endByte,
			ContentSum: contentSum,
		})
	}

	// Extract instantiation edges (new ClassName)
	for _, node := range result.Nodes {
		if node.EndByte > uint32(len(content)) {
			continue
		}
		bodyText := string(content[node.StartByte:node.EndByte])
		for _, newMatch := range phpNewExprRe.FindAllStringSubmatch(bodyText, -1) {
			target := newMatch[1]
			result.Edges = append(result.Edges, types.ASTEdge{
				SourceID: node.ID,
				TargetID: types.GenerateNodeID(relPath, target),
				EdgeType: types.EdgeTypeInstantiates,
			})
		}
	}

	return result, nil
}

// findBlockEnd finds the matching closing brace for a code block
func findBlockEnd(content []byte, startPos int) uint32 {
	depth := 0
	started := false
	for i := startPos; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
			started = true
		case '}':
			depth--
			if started && depth == 0 {
				return uint32(i + 1)
			}
		}
	}
	// If no matching brace found, return end of content or a reasonable limit
	end := startPos + 5000
	if end > len(content) {
		end = len(content)
	}
	return uint32(end)
}

// buildContentSum creates a summary from the line containing the declaration
func buildContentSum(lines []string, byteOffset int, name string) string {
	// Find the line number for this byte offset
	currentOffset := 0
	for i, line := range lines {
		nextOffset := currentOffset + len(line) + 1 // +1 for newline
		if currentOffset <= byteOffset && byteOffset < nextOffset {
			// Check for preceding comment (JSDoc/PHPDoc style)
			summary := name
			if i > 0 {
				prevLine := strings.TrimSpace(lines[i-1])
				if strings.HasPrefix(prevLine, "//") || strings.HasPrefix(prevLine, "*") || strings.HasPrefix(prevLine, "/**") {
					summary = prevLine + " " + name
				}
			}
			return strings.TrimSpace(summary)
		}
		currentOffset = nextOffset
	}
	return name
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

