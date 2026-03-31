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
					params = append(params, fmt.Sprintf("%v", param.Type))
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
			if ident, ok := fn.X.(*ast.Ident); ok {
				calls = append(calls, ident.Name+"."+fn.Sel.Name)
			}
		}
		return true
	})
	return calls
}

// JavaScript/TypeScript regex patterns
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

// parseJavaScript uses regex-based extraction for JS/TS files
// L17: TypeScript-specific regex patterns
var (
	tsInterfaceDeclRe = regexp.MustCompile(`(?m)(?:^|\n)\s*(?:export\s+)?interface\s+(\w+)`)
	tsEnumDeclRe      = regexp.MustCompile(`(?m)(?:^|\n)\s*(?:export\s+)?(?:const\s+)?enum\s+(\w+)`)
	tsTypeDeclRe      = regexp.MustCompile(`(?m)(?:^|\n)\s*(?:export\s+)?type\s+(\w+)\s*[=<]`)
)

func (p *Parser) parseJavaScript(content []byte, relPath string) (*ParseResult, error) {
	result := &ParseResult{}
	text := string(content)
	lines := strings.Split(text, "\n")

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

	// Track names already added to avoid duplicates
	seen := map[string]bool{}

	// Extract function declarations
	for _, match := range jsFuncDeclRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		if seen[name] {
			continue
		}
		seen[name] = true
		// Compute startByte: skip any leading newline captured by the regex
		rawStart := match[0]
		startByte := skipLeadingNewline(content, rawStart)
		endByte := findBlockEnd(content, startByte)
		contentSum := buildContentSum(lines, startByte, name)

		result.Nodes = append(result.Nodes, types.ASTNode{
			ID:         types.GenerateNodeID(relPath, name),
			FilePath:   relPath,
			SymbolName: name,
			NodeType:   types.NodeTypeFunction,
			StartByte:  uint32(startByte),
			EndByte:    endByte,
			ContentSum: contentSum,
		})
	}

	// Extract arrow functions
	for _, match := range jsArrowFuncRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		if seen[name] {
			continue
		}
		seen[name] = true
		rawStart := match[0]
		startByte := skipLeadingNewline(content, rawStart)
		endByte := findBlockEnd(content, startByte)
		contentSum := buildContentSum(lines, startByte, name)

		result.Nodes = append(result.Nodes, types.ASTNode{
			ID:         types.GenerateNodeID(relPath, name),
			FilePath:   relPath,
			SymbolName: name,
			NodeType:   types.NodeTypeFunction,
			StartByte:  uint32(startByte),
			EndByte:    endByte,
			ContentSum: contentSum,
		})
	}

	// Extract classes and their methods
	for _, match := range jsClassDeclRe.FindAllStringSubmatchIndex(text, -1) {
		className := text[match[2]:match[3]]
		if seen[className] {
			continue
		}
		seen[className] = true
		rawStart := match[0]
		startByte := skipLeadingNewline(content, rawStart)
		endByte := findBlockEnd(content, startByte)
		contentSum := buildContentSum(lines, startByte, className)

		classNode := types.ASTNode{
			ID:         types.GenerateNodeID(relPath, className),
			FilePath:   relPath,
			SymbolName: className,
			NodeType:   types.NodeTypeClass,
			StartByte:  uint32(startByte),
			EndByte:    endByte,
			ContentSum: contentSum,
		}
		result.Nodes = append(result.Nodes, classNode)

		// Extract methods inside this class body
		classBody := text[startByte:endByte]
		for _, methodMatch := range jsMethodDeclRe.FindAllStringSubmatchIndex(classBody, -1) {
			methodName := classBody[methodMatch[2]:methodMatch[3]]
			// Skip constructor and keywords
			if methodName == "constructor" || methodName == "if" || methodName == "for" || methodName == "while" || methodName == "switch" || methodName == "catch" || methodName == "function" {
				continue
			}
			qualifiedName := className + "." + methodName
			if seen[qualifiedName] {
				continue
			}
			seen[qualifiedName] = true

			methodAbsStart := startByte + methodMatch[0]
			methodAbsStart = skipLeadingNewline(content, methodAbsStart)
			methodEndByte := findBlockEnd(content, methodAbsStart)
			methodContentSum := buildContentSum(lines, methodAbsStart, qualifiedName)

			result.Nodes = append(result.Nodes, types.ASTNode{
				ID:         types.GenerateNodeID(relPath, qualifiedName),
				FilePath:   relPath,
				SymbolName: qualifiedName,
				NodeType:   types.NodeTypeMethod,
				StartByte:  uint32(methodAbsStart),
				EndByte:    methodEndByte,
				ContentSum: methodContentSum,
			})
		}
	}

	// L17: Extract TypeScript-specific constructs (interfaces, enums, type aliases)
	for _, match := range tsInterfaceDeclRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		if seen[name] {
			continue
		}
		seen[name] = true
		rawStart := match[0]
		startByte := skipLeadingNewline(content, rawStart)
		endByte := findBlockEnd(content, startByte)
		contentSum := buildContentSum(lines, startByte, name)

		result.Nodes = append(result.Nodes, types.ASTNode{
			ID:         types.GenerateNodeID(relPath, name),
			FilePath:   relPath,
			SymbolName: name,
			NodeType:   types.NodeTypeInterface,
			StartByte:  uint32(startByte),
			EndByte:    endByte,
			ContentSum: contentSum,
		})
	}

	for _, match := range tsEnumDeclRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		if seen[name] {
			continue
		}
		seen[name] = true
		rawStart := match[0]
		startByte := skipLeadingNewline(content, rawStart)
		endByte := findBlockEnd(content, startByte)
		contentSum := buildContentSum(lines, startByte, name)

		result.Nodes = append(result.Nodes, types.ASTNode{
			ID:         types.GenerateNodeID(relPath, name),
			FilePath:   relPath,
			SymbolName: name,
			NodeType:   types.NodeTypeStruct,
			StartByte:  uint32(startByte),
			EndByte:    endByte,
			ContentSum: contentSum,
		})
	}

	for _, match := range tsTypeDeclRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		if seen[name] {
			continue
		}
		seen[name] = true
		rawStart := match[0]
		startByte := skipLeadingNewline(content, rawStart)
		// Type aliases may not have braces, use a simpler end detection
		endByte := findBlockEnd(content, startByte)
		contentSum := buildContentSum(lines, startByte, name)

		result.Nodes = append(result.Nodes, types.ASTNode{
			ID:         types.GenerateNodeID(relPath, name),
			FilePath:   relPath,
			SymbolName: name,
			NodeType:   types.NodeTypeFunction,
			StartByte:  uint32(startByte),
			EndByte:    endByte,
			ContentSum: contentSum,
		})
	}

	// Extract call edges (connecting nodes found in this file to their calls)
	// M9: deduplicate call edges
	callSeen := map[string]bool{}
	for i, node := range result.Nodes {
		if node.NodeType == types.NodeTypeFile {
			continue // skip file node for call extraction
		}
		if node.EndByte > uint32(len(content)) {
			continue
		}
		bodyText := string(content[node.StartByte:node.EndByte])
		for _, callMatch := range jsCallExprRe.FindAllStringSubmatch(bodyText, -1) {
			target := callMatch[1]
			// Skip common keywords
			if isJSKeyword(target) || target == node.SymbolName {
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

	// H1: Extract import edges
	// C1: TargetID references the target file's file node
	// import ... from 'module'
	for _, match := range jsImportFromRe.FindAllStringSubmatch(text, -1) {
		modulePath := match[1]
		result.Edges = append(result.Edges, types.ASTEdge{
			SourceID: types.GenerateNodeID(relPath, relPath),           // this file
			TargetID: types.GenerateNodeID(modulePath, modulePath),     // target file's file node
			EdgeType: types.EdgeTypeImports,
		})
	}
	// require('module')
	for _, match := range jsRequireRe.FindAllStringSubmatch(text, -1) {
		modulePath := match[1]
		result.Edges = append(result.Edges, types.ASTEdge{
			SourceID: types.GenerateNodeID(relPath, relPath),           // this file
			TargetID: types.GenerateNodeID(modulePath, modulePath),     // target file's file node
			EdgeType: types.EdgeTypeImports,
		})
	}

	return result, nil
}

// skipLeadingNewline advances past a leading newline character at pos
func skipLeadingNewline(content []byte, pos int) int {
	if pos < len(content) && content[pos] == '\n' {
		return pos + 1
	}
	return pos
}

// PHP regex patterns
var (
	phpClassDeclRe    = regexp.MustCompile(`(?m)(?:^|\n)\s*(?:abstract\s+)?class\s+(\w+)`)
	phpMethodDeclRe   = regexp.MustCompile(`(?m)^[ \t]+(?:(?:public|protected|private)\s+)?(?:static\s+)?function\s+(\w+)\s*\(`)
	phpFuncDeclRe     = regexp.MustCompile(`(?m)(?:^|\n)\s*function\s+(\w+)\s*\(`)
	phpNewExprRe      = regexp.MustCompile(`new\s+(\w+)\s*\(`)
	phpUseRe          = regexp.MustCompile(`(?m)^use\s+([\w\\]+)`)
	phpMethodCallRe   = regexp.MustCompile(`(?:\$this|\$\w+|self|static|parent)\s*(?:->|::)\s*(\w+)\s*\(`)
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

// parsePHP uses regex-based extraction for PHP files
func (p *Parser) parsePHP(content []byte, relPath string) (*ParseResult, error) {
	result := &ParseResult{}
	text := string(content)
	lines := strings.Split(text, "\n")

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

	// C11: Track seen names to avoid duplicate nodes
	seen := map[string]bool{}

	// Extract classes
	for _, match := range phpClassDeclRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[match[2]:match[3]]
		if seen[name] {
			continue
		}
		seen[name] = true
		rawStart := match[0]
		startPos := skipLeadingNewline(content, rawStart)
		startByte := uint32(startPos)
		endByte := findBlockEnd(content, startPos)
		contentSum := buildContentSum(lines, startPos, name)

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

		// C11: skip duplicates
		if seen[qualifiedName] {
			continue
		}
		seen[qualifiedName] = true

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
		rawStart := match[0]
		startPos := skipLeadingNewline(content, rawStart)

		// C11: Skip if this function is inside a class body (already captured as a method)
		insideClass := false
		for _, node := range result.Nodes {
			if node.NodeType == types.NodeTypeClass && uint32(startPos) >= node.StartByte && uint32(startPos) < node.EndByte {
				insideClass = true
				break
			}
		}
		if insideClass {
			continue
		}

		if seen[name] {
			continue
		}
		seen[name] = true

		startByte := uint32(startPos)
		endByte := findBlockEnd(content, startPos)
		contentSum := buildContentSum(lines, startPos, name)

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

	// Extract instantiation edges (new ClassName) and call edges
	// M9: deduplicate call edges
	callSeen := map[string]bool{}
	for _, node := range result.Nodes {
		if node.NodeType == types.NodeTypeFile {
			continue // skip file node for call extraction
		}
		if node.EndByte > uint32(len(content)) {
			continue
		}
		bodyText := string(content[node.StartByte:node.EndByte])

		// Instantiation edges: new ClassName()
		for _, newMatch := range phpNewExprRe.FindAllStringSubmatch(bodyText, -1) {
			target := newMatch[1]
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
		for _, callMatch := range phpMethodCallRe.FindAllStringSubmatch(bodyText, -1) {
			target := callMatch[1]
			if !phpCallKeywords[target] {
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

		// M5: Static call edges — ClassName::method()
		for _, callMatch := range phpStaticCallRe.FindAllStringSubmatch(bodyText, -1) {
			className := callMatch[1]
			methodName := callMatch[2]
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
			for _, callMatch := range phpFuncCallRe.FindAllStringSubmatch(bodyText, -1) {
				target := callMatch[1]
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

	// H1: Extract PHP import (use) edges
	// C1: TargetID references the target file's file node
	for _, match := range phpUseRe.FindAllStringSubmatch(text, -1) {
		usePath := match[1]
		result.Edges = append(result.Edges, types.ASTEdge{
			SourceID: types.GenerateNodeID(relPath, relPath),       // this file
			TargetID: types.GenerateNodeID(usePath, usePath),       // target file's file node
			EdgeType: types.EdgeTypeImports,
		})
	}

	return result, nil
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

	// Check for single-line comment block (// or lines starting with *)
	for j >= 0 {
		line := strings.TrimSpace(lines[j])
		if strings.HasPrefix(line, "//") {
			collected = append([]string{cleanCommentLine(line)}, collected...)
			j--
		} else if strings.HasPrefix(line, "*") || strings.HasPrefix(line, "/**") {
			// Inside a doc block — keep going up
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

