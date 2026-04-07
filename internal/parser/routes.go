package parser

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/maplenk/context-mcp/internal/types"
)

var (
	routeGroupPrefixRe = regexp.MustCompile(`Route::group\s*\(\s*\[.*?['"]prefix['"]\s*=>\s*['"]([^'"]+)['"]`)
	routeUsesRe        = regexp.MustCompile(`['"]uses['"]\s*=>\s*['"]([\w\\]+)@(\w+)['"]`)
	routeStringHandler = regexp.MustCompile(`['"]([\w\\]+)@(\w+)['"]`)
	routeArrayHandler  = regexp.MustCompile(`\[\s*([\w\\]+)::class\s*,\s*['"](\w+)['"]\s*\]`)
	routeInvokable     = regexp.MustCompile(`\b([\w\\]+)::class\b`)
	controllerClassRe  = regexp.MustCompile(`@Controller\(([^)]*)\)\s*(?:export\s+)?class\s+(\w+)`)
	nestMethodRe       = regexp.MustCompile(`@(Get|Post|Put|Patch|Delete|All)\(([^)]*)\)\s*(?:(?:public|private|protected|static|async)\s+)*(\w+)\s*\(`)
	jsRouterDeclRe     = regexp.MustCompile(`\b(?:const|let|var)\s+(\w+)\s*=\s*(?:\w+\.)?Router\s*\(`)
)

type routeEmission struct {
	Method       string
	Path         string
	Handler      string
	Start        int
	End          int
	TargetSymbol string
}

type groupPrefix struct {
	prefix   string
	startPos int
	endPos   int
}

type mountedPrefix struct {
	parent string
	prefix string
}

type goPrefix struct {
	parent string
	prefix string
}

// ExtractRoutes extracts Laravel route definitions from PHP source code.
func ExtractRoutes(content []byte, relPath string) ([]types.ASTNode, []types.ASTEdge) {
	src := string(content)
	groupPrefixes := extractGroupPrefixes(src)

	var nodes []types.ASTNode
	var edges []types.ASTEdge
	for _, call := range scanPHPRouteCalls(src) {
		switch strings.ToLower(call.method) {
		case "get", "post", "put", "patch", "delete":
			args := splitTopLevelArgs(call.args)
			if len(args) < 2 {
				continue
			}
			path := normalizePath(applyGroupPrefix(groupPrefixes, call.start, parseQuotedLiteral(args[0])))
			handler := parseLaravelHandler(args[1:])
			emitRoute(&nodes, &edges, relPath, strings.ToUpper(call.method), path, handler, call.start, call.end)
		case "any":
			args := splitTopLevelArgs(call.args)
			if len(args) < 2 {
				continue
			}
			path := normalizePath(applyGroupPrefix(groupPrefixes, call.start, parseQuotedLiteral(args[0])))
			handler := parseLaravelHandler(args[1:])
			for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
				emitRoute(&nodes, &edges, relPath, method, path, handler, call.start, call.end)
			}
		case "match":
			args := splitTopLevelArgs(call.args)
			if len(args) < 3 {
				continue
			}
			methods := parseLaravelMethods(args[0])
			path := normalizePath(applyGroupPrefix(groupPrefixes, call.start, parseQuotedLiteral(args[1])))
			handler := parseLaravelHandler(args[2:])
			for _, method := range methods {
				emitRoute(&nodes, &edges, relPath, method, path, handler, call.start, call.end)
			}
		case "resource", "apiresource":
			args := splitTopLevelArgs(call.args)
			if len(args) < 2 {
				continue
			}
			basePath := normalizePath(applyGroupPrefix(groupPrefixes, call.start, parseQuotedLiteral(args[0])))
			controller := parseClassRef(args[1])
			if controller == "" {
				continue
			}
			emitResourceRoutes(&nodes, &edges, relPath, basePath, controller, strings.EqualFold(call.method, "apiresource"), call.start, call.end)
		}
	}

	return nodes, edges
}

// ExtractJSRoutes extracts route-like definitions from JS/TS source code.
func ExtractJSRoutes(content []byte, relPath string) ([]types.ASTNode, []types.ASTEdge) {
	src := string(content)
	routerVars := make(map[string]bool)
	for _, match := range jsRouterDeclRe.FindAllStringSubmatch(src, -1) {
		routerVars[match[1]] = true
	}

	mounts := make(map[string][]mountedPrefix)
	for _, call := range scanMemberCalls(src, []string{"use"}) {
		args := splitTopLevelArgs(call.args)
		if len(args) < 2 {
			continue
		}
		path := parseQuotedLiteral(args[0])
		child := parseBareIdentifier(args[1])
		if path == "" || child == "" {
			continue
		}
		mounts[child] = append(mounts[child], mountedPrefix{parent: call.object, prefix: normalizePath(path)})
	}

	var nodes []types.ASTNode
	var edges []types.ASTEdge
	for _, call := range scanMemberCalls(src, []string{"get", "post", "put", "patch", "delete", "all"}) {
		args := splitTopLevelArgs(call.args)
		if len(args) < 2 {
			continue
		}
		path := parseQuotedLiteral(args[0])
		if path == "" {
			continue
		}
		handler := parseJSSymbolHandler(args[1:])
		for _, prefix := range resolveMountedPrefixes(call.object, mounts, map[string]bool{}) {
			fullPath := normalizePath(joinRoutePath(prefix, path))
			method := strings.ToUpper(call.method)
			emitRoute(&nodes, &edges, relPath, method, fullPath, handler, call.start, call.end)
		}
	}

	for _, classMatch := range controllerClassRe.FindAllStringSubmatchIndex(src, -1) {
		prefix := normalizePath(parseQuotedLiteral(src[classMatch[2]:classMatch[3]]))
		className := src[classMatch[4]:classMatch[5]]
		openBrace := strings.Index(src[classMatch[1]:], "{")
		if openBrace < 0 {
			continue
		}
		openBrace += classMatch[1]
		closeBrace := int(findBlockEnd(content, openBrace))
		if closeBrace <= openBrace || closeBrace > len(src) {
			continue
		}
		body := src[openBrace:closeBrace]
		for _, mm := range nestMethodRe.FindAllStringSubmatchIndex(body, -1) {
			method := strings.ToUpper(body[mm[2]:mm[3]])
			routePath := normalizePath(joinRoutePath(prefix, parseQuotedLiteral(body[mm[4]:mm[5]])))
			handler := className + "." + body[mm[6]:mm[7]]
			emitRoute(&nodes, &edges, relPath, method, routePath, handler, openBrace+mm[0], openBrace+mm[1])
		}
	}

	return dedupeRouteNodes(nodes), dedupeEdges(edges)
}

// ExtractGoRoutes extracts route-like registrations from Go AST.
func ExtractGoRoutes(file *ast.File, fset *token.FileSet, content []byte, relPath string) ([]types.ASTNode, []types.ASTEdge) {
	prefixVars := make(map[string]goPrefix)
	ast.Inspect(file, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			if len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
				return true
			}
			lhs, ok := stmt.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			call, ok := stmt.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "Group" || len(call.Args) == 0 {
				return true
			}
			prefix := parseGoStringLiteral(call.Args[0])
			if prefix == "" {
				return true
			}
			parent := exprBaseName(sel.X)
			prefixVars[lhs.Name] = goPrefix{parent: parent, prefix: normalizePath(prefix)}
		}
		return true
	})

	var nodes []types.ASTNode
	var edges []types.ASTEdge
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == "Methods" {
			baseCall, ok := sel.X.(*ast.CallExpr)
			if !ok {
				return true
			}
			baseSel, ok := baseCall.Fun.(*ast.SelectorExpr)
			if !ok || baseSel.Sel == nil || baseSel.Sel.Name != "HandleFunc" || len(baseCall.Args) < 2 {
				return true
			}
			path := parseGoStringLiteral(baseCall.Args[0])
			handler := parseGoHandlerSymbol(baseCall.Args[1])
			for _, method := range parseGoMethods(call.Args) {
				emitRoute(&nodes, &edges, relPath, method, normalizePath(path), handler, fset.Position(call.Pos()).Offset, fset.Position(call.End()).Offset)
			}
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return true
		}
		obj := exprBaseName(sel.X)
		methodName := sel.Sel.Name
		switch methodName {
		case "Handle", "HandleFunc":
			if len(call.Args) < 2 {
				return true
			}
			method, path := parseGoRoutePattern(parseGoStringLiteral(call.Args[0]))
			if path == "" {
				return true
			}
			handler := parseGoHandlerSymbol(call.Args[1])
			for _, prefix := range resolveGoPrefixes(obj, prefixVars, map[string]bool{}) {
				emitRoute(&nodes, &edges, relPath, method, normalizePath(joinRoutePath(prefix, path)), handler, fset.Position(call.Pos()).Offset, fset.Position(call.End()).Offset)
			}
		default:
			if httpMethod := parseGoHTTPMethod(methodName); httpMethod != "" && len(call.Args) >= 2 {
				path := parseGoStringLiteral(call.Args[0])
				if path == "" {
					return true
				}
				handler := parseGoHandlerSymbol(call.Args[1])
				for _, prefix := range resolveGoPrefixes(obj, prefixVars, map[string]bool{}) {
					emitRoute(&nodes, &edges, relPath, httpMethod, normalizePath(joinRoutePath(prefix, path)), handler, fset.Position(call.Pos()).Offset, fset.Position(call.End()).Offset)
				}
			}
		}

		return true
	})

	return dedupeRouteNodes(nodes), dedupeEdges(edges)
}

type phpRouteCall struct {
	method string
	args   string
	start  int
	end    int
}

type memberCall struct {
	object string
	method string
	args   string
	start  int
	end    int
}

func scanPHPRouteCalls(src string) []phpRouteCall {
	var calls []phpRouteCall
	for pos := 0; pos < len(src); {
		idx := strings.Index(src[pos:], "Route::")
		if idx < 0 {
			break
		}
		start := pos + idx
		nameStart := start + len("Route::")
		nameEnd := nameStart
		for nameEnd < len(src) && (unicode.IsLetter(rune(src[nameEnd])) || src[nameEnd] == '_') {
			nameEnd++
		}
		open := nameEnd
		for open < len(src) && unicode.IsSpace(rune(src[open])) {
			open++
		}
		if open >= len(src) || src[open] != '(' {
			pos = nameEnd
			continue
		}
		close := findMatchingDelimiter(src, open, '(', ')')
		if close < 0 {
			break
		}
		calls = append(calls, phpRouteCall{
			method: src[nameStart:nameEnd],
			args:   src[open+1 : close],
			start:  start,
			end:    close + 1,
		})
		pos = start + len("Route::")
	}
	return calls
}

func scanMemberCalls(src string, methods []string) []memberCall {
	methodSet := make(map[string]bool, len(methods))
	for _, method := range methods {
		methodSet[strings.ToLower(method)] = true
	}

	var calls []memberCall
	for pos := 0; pos < len(src); pos++ {
		if !isIdentStart(src[pos]) {
			continue
		}
		objStart := pos
		objEnd := pos + 1
		for objEnd < len(src) && isIdentPart(src[objEnd]) {
			objEnd++
		}
		if objEnd+1 >= len(src) || src[objEnd] != '.' {
			pos = objEnd
			continue
		}
		methodStart := objEnd + 1
		methodEnd := methodStart
		for methodEnd < len(src) && isIdentPart(src[methodEnd]) {
			methodEnd++
		}
		if !methodSet[strings.ToLower(src[methodStart:methodEnd])] {
			pos = methodEnd
			continue
		}
		open := methodEnd
		for open < len(src) && unicode.IsSpace(rune(src[open])) {
			open++
		}
		if open >= len(src) || src[open] != '(' {
			pos = methodEnd
			continue
		}
		close := findMatchingDelimiter(src, open, '(', ')')
		if close < 0 {
			break
		}
		calls = append(calls, memberCall{
			object: src[objStart:objEnd],
			method: src[methodStart:methodEnd],
			args:   src[open+1 : close],
			start:  objStart,
			end:    close + 1,
		})
		pos = close
	}
	return calls
}

func extractGroupPrefixes(src string) []groupPrefix {
	var groups []groupPrefix
	matches := routeGroupPrefixRe.FindAllStringSubmatchIndex(src, -1)
	for _, match := range matches {
		prefix := src[match[2]:match[3]]
		searchStart := match[1]
		braceStart := -1
		for i := searchStart; i < len(src); i++ {
			if src[i] == '{' {
				braceStart = i
				break
			}
		}
		if braceStart == -1 {
			continue
		}
		braceEnd := int(findBlockEnd([]byte(src), braceStart))
		if braceEnd <= braceStart {
			braceEnd = len(src)
		}
		groups = append(groups, groupPrefix{
			prefix:   prefix,
			startPos: braceStart,
			endPos:   braceEnd,
		})
	}
	return groups
}

func applyGroupPrefix(groups []groupPrefix, routePos int, routePath string) string {
	var prefixes []string
	for _, group := range groups {
		if routePos > group.startPos && routePos < group.endPos {
			prefixes = append(prefixes, group.prefix)
		}
	}
	if len(prefixes) == 0 {
		return routePath
	}
	combined := strings.Join(prefixes, "/")
	return joinRoutePath(combined, routePath)
}

func parseLaravelMethods(arg string) []string {
	var methods []string
	for _, token := range splitTopLevelArgs(strings.Trim(arg, "[]")) {
		method := strings.ToUpper(parseQuotedLiteral(token))
		if method != "" {
			methods = append(methods, method)
		}
	}
	return methods
}

func parseLaravelHandler(args []string) string {
	if len(args) == 0 {
		return ""
	}
	handlerExpr := strings.TrimSpace(strings.Join(args, ","))
	if strings.Contains(handlerExpr, "function") || strings.Contains(handlerExpr, "fn(") {
		return ""
	}
	if match := routeArrayHandler.FindStringSubmatch(handlerExpr); len(match) == 3 {
		return parseClassRef(match[1]+"::class") + "." + match[2]
	}
	if match := routeUsesRe.FindStringSubmatch(handlerExpr); len(match) == 3 {
		return trimPHPClassName(match[1]) + "." + match[2]
	}
	if match := routeStringHandler.FindStringSubmatch(handlerExpr); len(match) == 3 {
		return trimPHPClassName(match[1]) + "." + match[2]
	}
	if match := routeInvokable.FindStringSubmatch(handlerExpr); len(match) == 2 {
		return trimPHPClassName(match[1]) + ".__invoke"
	}
	return ""
}

func emitResourceRoutes(nodes *[]types.ASTNode, edges *[]types.ASTEdge, relPath, basePath, controller string, apiOnly bool, start, end int) {
	basePath = normalizePath(basePath)
	idPath := joinRoutePath(basePath, "{id}")
	emitRoute(nodes, edges, relPath, "GET", basePath, controller+".index", start, end)
	if !apiOnly {
		emitRoute(nodes, edges, relPath, "GET", joinRoutePath(basePath, "create"), controller+".create", start, end)
	}
	emitRoute(nodes, edges, relPath, "POST", basePath, controller+".store", start, end)
	emitRoute(nodes, edges, relPath, "GET", idPath, controller+".show", start, end)
	if !apiOnly {
		emitRoute(nodes, edges, relPath, "GET", joinRoutePath(idPath, "edit"), controller+".edit", start, end)
	}
	emitRoute(nodes, edges, relPath, "PUT", idPath, controller+".update", start, end)
	emitRoute(nodes, edges, relPath, "DELETE", idPath, controller+".destroy", start, end)
}

func emitRoute(nodes *[]types.ASTNode, edges *[]types.ASTEdge, relPath, method, path, handler string, start, end int) {
	path = normalizePath(path)
	if method == "" || path == "" {
		return
	}
	symbolName := method + " " + path
	contentSum := symbolName
	pathTokens := extractRoutePathTokens(path)
	if len(pathTokens) > 0 {
		contentSum += " " + strings.Join(pathTokens, " ")
	}
	if isAPIRoute(path) {
		contentSum += " api endpoint"
	}
	if handler != "" {
		contentSum += " " + handler
	}

	node := types.ASTNode{
		ID:         types.GenerateNodeID(relPath, symbolName),
		FilePath:   relPath,
		SymbolName: symbolName,
		NodeType:   types.NodeTypeRoute,
		StartByte:  uint32(max(start, 0)),
		EndByte:    uint32(max(end, 0)),
		ContentSum: contentSum,
	}
	*nodes = append(*nodes, node)

	if handler != "" {
		*edges = append(*edges, types.ASTEdge{
			SourceID:     node.ID,
			TargetID:     types.GenerateNodeID(relPath, handler),
			EdgeType:     types.EdgeTypeHandles,
			TargetSymbol: handler,
		})
	}
}

func parseClassRef(expr string) string {
	expr = strings.TrimSpace(expr)
	if idx := strings.Index(expr, "::class"); idx >= 0 {
		return trimPHPClassName(expr[:idx])
	}
	return ""
}

func trimPHPClassName(expr string) string {
	expr = strings.TrimSpace(expr)
	if idx := strings.LastIndex(expr, `\`); idx >= 0 {
		expr = expr[idx+1:]
	}
	return strings.Trim(expr, `'"`)
}

func parseBareIdentifier(expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return ""
	}
	for i, r := range expr {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$') {
			if i == 0 {
				return ""
			}
			return strings.TrimPrefix(expr[:i], "$")
		}
	}
	return strings.TrimPrefix(expr, "$")
}

func parseQuotedLiteral(expr string) string {
	expr = strings.TrimSpace(expr)
	if len(expr) >= 2 {
		if (expr[0] == '\'' && expr[len(expr)-1] == '\'') || (expr[0] == '"' && expr[len(expr)-1] == '"') || (expr[0] == '`' && expr[len(expr)-1] == '`') {
			return expr[1 : len(expr)-1]
		}
	}
	return ""
}

func parseJSSymbolHandler(args []string) string {
	for i := len(args) - 1; i >= 0; i-- {
		expr := strings.TrimSpace(args[i])
		if expr == "" || strings.HasPrefix(expr, "function") || strings.Contains(expr, "=>") {
			continue
		}
		expr = strings.TrimSuffix(expr, ",")
		if strings.Contains(expr, ".") {
			parts := strings.Split(expr, ".")
			last := parts[len(parts)-1]
			if len(parts) >= 2 && isExportLike(parts[len(parts)-2]) {
				return parts[len(parts)-2] + "." + last
			}
			return last
		}
		if isIdentifier(expr) {
			return expr
		}
	}
	return ""
}

func parseGoStringLiteral(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	return strings.Trim(lit.Value, "`\"")
}

func parseGoMethods(args []ast.Expr) []string {
	var methods []string
	for _, arg := range args {
		if method := strings.ToUpper(parseGoStringLiteral(arg)); method != "" {
			methods = append(methods, method)
		}
	}
	return methods
}

func parseGoRoutePattern(pattern string) (string, string) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", ""
	}
	parts := strings.SplitN(pattern, " ", 2)
	if len(parts) == 2 {
		method := strings.ToUpper(strings.TrimSpace(parts[0]))
		if isHTTPMethod(method) {
			return method, strings.TrimSpace(parts[1])
		}
	}
	return "ANY", pattern
}

func parseGoHTTPMethod(name string) string {
	switch strings.ToUpper(name) {
	case "GET":
		return "GET"
	case "POST":
		return "POST"
	case "PUT":
		return "PUT"
	case "PATCH":
		return "PATCH"
	case "DELETE":
		return "DELETE"
	}
	switch name {
	case "Get":
		return "GET"
	case "Post":
		return "POST"
	case "Put":
		return "PUT"
	case "Patch":
		return "PATCH"
	case "Delete":
		return "DELETE"
	}
	return ""
}

func parseGoHandlerSymbol(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		if recv, ok := v.X.(*ast.Ident); ok {
			if len(recv.Name) > 0 && unicode.IsUpper(rune(recv.Name[0])) {
				return recv.Name + "." + v.Sel.Name
			}
		}
		return v.Sel.Name
	}
	return ""
}

func exprBaseName(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}

func resolveMountedPrefixes(object string, mounts map[string][]mountedPrefix, seen map[string]bool) []string {
	if object == "" || seen[object] {
		return []string{""}
	}
	entries := mounts[object]
	if len(entries) == 0 {
		return []string{""}
	}
	seen[object] = true
	defer delete(seen, object)

	var prefixes []string
	for _, entry := range entries {
		for _, parentPrefix := range resolveMountedPrefixes(entry.parent, mounts, seen) {
			prefixes = append(prefixes, normalizePath(joinRoutePath(parentPrefix, entry.prefix)))
		}
	}
	if len(prefixes) == 0 {
		return []string{""}
	}
	return uniqueStrings(prefixes)
}

func resolveGoPrefixes(object string, prefixes map[string]goPrefix, seen map[string]bool) []string {
	if object == "" || seen[object] {
		return []string{""}
	}
	entry, ok := prefixes[object]
	if !ok {
		return []string{""}
	}
	seen[object] = true
	defer delete(seen, object)

	var resolved []string
	for _, parent := range resolveGoPrefixes(entry.parent, prefixes, seen) {
		resolved = append(resolved, normalizePath(joinRoutePath(parent, entry.prefix)))
	}
	if len(resolved) == 0 {
		return []string{""}
	}
	return uniqueStrings(resolved)
}

func splitTopLevelArgs(src string) []string {
	var args []string
	start := 0
	depthParen, depthBracket, depthBrace := 0, 0, 0
	inSingle, inDouble, inBacktick := false, false, false
	for i := 0; i < len(src); i++ {
		ch := src[i]
		if ch == '\\' && i+1 < len(src) && (inSingle || inDouble || inBacktick) {
			i++
			continue
		}
		switch ch {
		case '\'':
			if !inDouble && !inBacktick {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle && !inBacktick {
				inDouble = !inDouble
			}
		case '`':
			if !inSingle && !inDouble {
				inBacktick = !inBacktick
			}
		case '(':
			if !inSingle && !inDouble && !inBacktick {
				depthParen++
			}
		case ')':
			if !inSingle && !inDouble && !inBacktick && depthParen > 0 {
				depthParen--
			}
		case '[':
			if !inSingle && !inDouble && !inBacktick {
				depthBracket++
			}
		case ']':
			if !inSingle && !inDouble && !inBacktick && depthBracket > 0 {
				depthBracket--
			}
		case '{':
			if !inSingle && !inDouble && !inBacktick {
				depthBrace++
			}
		case '}':
			if !inSingle && !inDouble && !inBacktick && depthBrace > 0 {
				depthBrace--
			}
		case ',':
			if !inSingle && !inDouble && !inBacktick && depthParen == 0 && depthBracket == 0 && depthBrace == 0 {
				args = append(args, strings.TrimSpace(src[start:i]))
				start = i + 1
			}
		}
	}
	tail := strings.TrimSpace(src[start:])
	if tail != "" {
		args = append(args, tail)
	}
	return args
}

func findMatchingDelimiter(src string, openIdx int, open, close byte) int {
	depth := 0
	inSingle, inDouble, inBacktick := false, false, false
	for i := openIdx; i < len(src); i++ {
		ch := src[i]
		if ch == '\\' && i+1 < len(src) && (inSingle || inDouble || inBacktick) {
			i++
			continue
		}
		switch ch {
		case '\'':
			if !inDouble && !inBacktick {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle && !inBacktick {
				inDouble = !inDouble
			}
		case '`':
			if !inSingle && !inDouble {
				inBacktick = !inBacktick
			}
		default:
			if inSingle || inDouble || inBacktick {
				continue
			}
			if ch == open {
				depth++
			} else if ch == close {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
	}
	return -1
}

func joinRoutePath(prefix, path string) string {
	switch {
	case prefix == "":
		return path
	case path == "":
		return prefix
	default:
		return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(path, "/")
	}
}

func normalizePath(path string) string {
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimRight(path, "/")
	}
	return path
}

func extractRoutePathTokens(path string) []string {
	var tokens []string
	for _, segment := range strings.Split(path, "/") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		segment = strings.TrimPrefix(segment, "{")
		segment = strings.TrimSuffix(segment, "}")
		tokens = append(tokens, segment)
	}
	return tokens
}

func isAPIRoute(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasPrefix(lower, "/v1/") || strings.HasPrefix(lower, "/v2/") ||
		strings.HasPrefix(lower, "/v3/") || strings.HasPrefix(lower, "/api/") ||
		strings.Contains(lower, "/v1/") || strings.Contains(lower, "/v2/") ||
		strings.Contains(lower, "/v3/") || strings.Contains(lower, "/api/")
}

func isRouteFile(relPath string) bool {
	lower := strings.ToLower(relPath)
	base := filepath.Base(lower)
	if strings.HasPrefix(base, "route") && strings.HasSuffix(base, ".php") {
		return true
	}
	for _, part := range strings.Split(lower, string(filepath.Separator)) {
		if part == "routes" {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func dedupeRouteNodes(nodes []types.ASTNode) []types.ASTNode {
	seen := make(map[string]bool, len(nodes))
	var out []types.ASTNode
	for _, node := range nodes {
		key := node.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, node)
	}
	return out
}

func dedupeEdges(edges []types.ASTEdge) []types.ASTEdge {
	seen := make(map[string]bool, len(edges))
	var out []types.ASTEdge
	for _, edge := range edges {
		key := edge.SourceID + "|" + edge.TargetID + "|" + edge.TargetSymbol + "|" + edge.EdgeType.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, edge)
	}
	return out
}

func isIdentStart(ch byte) bool {
	return ch == '_' || ch == '$' || unicode.IsLetter(rune(ch))
}

func isIdentPart(ch byte) bool {
	return isIdentStart(ch) || unicode.IsDigit(rune(ch))
}

func isIdentifier(expr string) bool {
	if expr == "" || !isIdentStart(expr[0]) {
		return false
	}
	for i := 1; i < len(expr); i++ {
		if !isIdentPart(expr[i]) {
			return false
		}
	}
	return true
}

func isExportLike(name string) bool {
	return name != "" && unicode.IsUpper(rune(name[0]))
}

func isHTTPMethod(method string) bool {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "ANY":
		return true
	default:
		return false
	}
}
