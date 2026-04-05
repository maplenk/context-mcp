package parser

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/maplenk/context-mcp/internal/types"
)

// routeMethodRe matches Route::method('path', ...) calls.
// Captures: (1) HTTP method, (2) route path
var routeMethodRe = regexp.MustCompile(`(?i)Route::(get|post|put|delete|patch)\s*\(\s*['"]([^'"]*?)['"]`)

// routeUsesRe extracts the 'uses' => 'Controller@method' from route options.
// Applied to the text after the route path match.
var routeUsesRe = regexp.MustCompile(`['"]uses['"]\s*=>\s*['"](\w+)@(\w+)['"]`)

// routeGroupPrefixRe matches Route::group(['prefix' => '...'], ...)
var routeGroupPrefixRe = regexp.MustCompile(`Route::group\s*\(\s*\[.*?['"]prefix['"]\s*=>\s*['"]([^'"]+)['"]`)

// groupPrefix tracks a Route::group prefix and the byte range of its body.
type groupPrefix struct {
	prefix   string
	startPos int // byte offset of the group's function body opening {
	endPos   int // byte offset of the closing });
}

// extractGroupPrefixes finds all Route::group calls with a prefix and determines
// the byte range of each group's function body using brace-depth tracking.
func extractGroupPrefixes(src string) []groupPrefix {
	var groups []groupPrefix
	matches := routeGroupPrefixRe.FindAllStringSubmatchIndex(src, -1)
	for _, match := range matches {
		prefix := src[match[2]:match[3]]

		// Find the opening { of the closure after this match
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

		// Track brace depth to find the matching closing }
		depth := 1
		braceEnd := -1
		for i := braceStart + 1; i < len(src); i++ {
			switch src[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					braceEnd = i
				}
			}
			if braceEnd >= 0 {
				break
			}
		}
		if braceEnd == -1 {
			braceEnd = len(src) // unclosed group — extend to EOF
		}

		groups = append(groups, groupPrefix{
			prefix:   prefix,
			startPos: braceStart,
			endPos:   braceEnd,
		})
	}
	return groups
}

// applyGroupPrefix prepends the innermost enclosing group prefix to routePath.
// For nested groups, all enclosing prefixes are applied from outermost to innermost.
func applyGroupPrefix(groups []groupPrefix, routePos int, routePath string) string {
	// Collect all enclosing group prefixes (outermost first, since they appear
	// earlier in the source and are discovered in order).
	var prefixes []string
	for _, g := range groups {
		if routePos > g.startPos && routePos < g.endPos {
			prefixes = append(prefixes, g.prefix)
		}
	}
	if len(prefixes) == 0 {
		return routePath
	}

	// Build combined prefix: outermost/innermost
	combined := strings.Join(prefixes, "/")

	// Normalize: ensure combined has no leading /, routePath has no leading /
	combined = strings.TrimPrefix(combined, "/")
	routePath = strings.TrimPrefix(routePath, "/")

	return "/" + combined + "/" + routePath
}

// ExtractRoutes extracts Laravel route definitions from PHP source code.
// It returns route nodes and edges linking routes to their handler methods.
func ExtractRoutes(content []byte, relPath string) ([]types.ASTNode, []types.ASTEdge) {
	src := string(content)

	// Step 1: Build prefix context from Route::group declarations.
	groupPrefixes := extractGroupPrefixes(src)

	var nodes []types.ASTNode
	var edges []types.ASTEdge

	// Step 2: Find all Route::method() calls
	matches := routeMethodRe.FindAllStringSubmatchIndex(src, -1)
	for _, match := range matches {
		httpMethod := strings.ToUpper(src[match[2]:match[3]])
		routePath := src[match[4]:match[5]]

		// Normalize path: ensure leading /
		if !strings.HasPrefix(routePath, "/") {
			routePath = "/" + routePath
		}

		// Apply group prefix if this route is inside a group
		routePath = applyGroupPrefix(groupPrefixes, match[0], routePath)

		// Clean up double slashes from prefix concatenation
		for strings.Contains(routePath, "//") {
			routePath = strings.ReplaceAll(routePath, "//", "/")
		}

		// Extract handler from the rest of the route definition (until next ; or ))
		restStart := match[5]
		restEnd := restStart + 500 // look ahead up to 500 chars
		if restEnd > len(src) {
			restEnd = len(src)
		}
		rest := src[restStart:restEnd]

		var controllerName, methodName string
		usesMatch := routeUsesRe.FindStringSubmatch(rest)
		if usesMatch != nil {
			controllerName = usesMatch[1]
			methodName = usesMatch[2]
		}

		// Create route node
		symbolName := httpMethod + " " + routePath
		contentSum := symbolName

		// Extract meaningful path segments for FTS indexing
		pathTokens := extractRoutePathTokens(routePath)
		if len(pathTokens) > 0 {
			contentSum += " " + strings.Join(pathTokens, " ")
		}

		// Mark versioned API paths as "api endpoint" for search discovery
		if isAPIRoute(routePath) {
			contentSum += " api endpoint"
		}

		if controllerName != "" {
			contentSum += " " + controllerName + " " + methodName
		}

		routeNode := types.ASTNode{
			ID:         types.GenerateNodeID(relPath, symbolName),
			FilePath:   relPath,
			SymbolName: symbolName,
			NodeType:   types.NodeTypeRoute,
			StartByte:  uint32(match[0]),
			EndByte:    uint32(match[1]),
			ContentSum: contentSum,
		}
		nodes = append(nodes, routeNode)

		// Create edge to handler (will be resolved later via symbol lookup)
		if controllerName != "" {
			edge := types.ASTEdge{
				SourceID:     routeNode.ID,
				TargetID:     "", // resolved later during cross-file edge resolution
				EdgeType:     types.EdgeTypeHandles,
				TargetSymbol: methodName,
			}
			edges = append(edges, edge)
		}
	}

	return nodes, edges
}

// extractRoutePathTokens splits a route path into meaningful search tokens.
// "/v1/merchant/{storeID}/order" → ["v1", "merchant", "storeID", "order"]
func extractRoutePathTokens(path string) []string {
	var tokens []string
	for _, segment := range strings.Split(path, "/") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		// Strip braces from parameters: {storeID} → storeID
		segment = strings.TrimPrefix(segment, "{")
		segment = strings.TrimSuffix(segment, "}")
		tokens = append(tokens, segment)
	}
	return tokens
}

// isAPIRoute returns true if the route path looks like a versioned API endpoint.
func isAPIRoute(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasPrefix(lower, "/v1/") || strings.HasPrefix(lower, "/v2/") ||
		strings.HasPrefix(lower, "/v3/") || strings.HasPrefix(lower, "/api/") ||
		strings.Contains(lower, "/v1/") || strings.Contains(lower, "/v2/") ||
		strings.Contains(lower, "/v3/") || strings.Contains(lower, "/api/")
}

// isRouteFile returns true if the file path looks like a Laravel route file.
func isRouteFile(relPath string) bool {
	lower := strings.ToLower(relPath)
	base := filepath.Base(lower)
	// Match route files: routes.php, routes.v1.php, routesWeb.v2.php, routes.webhooks.php, etc.
	if strings.HasPrefix(base, "route") && strings.HasSuffix(base, ".php") {
		return true
	}
	// Also match files in a routes/ directory
	parts := strings.Split(lower, string(filepath.Separator))
	for _, part := range parts {
		if part == "routes" {
			return true
		}
	}
	return false
}
