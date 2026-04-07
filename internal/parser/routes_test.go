//go:build fts5

package parser

import (
	"strings"
	"testing"

	"github.com/maplenk/context-mcp/internal/types"
)

func findHandleEdge(edges []types.ASTEdge, target string) *types.ASTEdge {
	for i, edge := range edges {
		if edge.EdgeType == types.EdgeTypeHandles && edge.TargetSymbol == target {
			return &edges[i]
		}
	}
	return nil
}

func TestExtractRoutes_BasicRoute(t *testing.T) {
	src := []byte(`<?php
Route::get('/status', ['as' => 'status', 'uses' => 'statusController@QBHealthCheck']);
Route::post('v1/merchant/{chainID}/getDashboardStats', ['as' => 'getDashboardStats', 'uses' => 'chartController@getDashboardStats']);
`)
	nodes, edges := ExtractRoutes(src, "app/Http/routes.php")

	if len(nodes) != 2 {
		t.Fatalf("expected 2 route nodes, got %d", len(nodes))
	}
	if nodes[0].SymbolName != "GET /status" {
		t.Errorf("expected 'GET /status', got %q", nodes[0].SymbolName)
	}
	if nodes[1].SymbolName != "POST /v1/merchant/{chainID}/getDashboardStats" {
		t.Errorf("expected normalized path, got %q", nodes[1].SymbolName)
	}
	if findHandleEdge(edges, "statusController.QBHealthCheck") == nil {
		t.Fatal("expected fully qualified handler target for status route")
	}
	if findHandleEdge(edges, "chartController.getDashboardStats") == nil {
		t.Fatal("expected fully qualified handler target for dashboard route")
	}
}

func TestExtractRoutes_GroupPrefixAndArrayHandlers(t *testing.T) {
	src := []byte(`<?php
Route::group(['prefix' => 'v1/webhook'], function () {
    Route::post('/receiveSalesInvoice', [WebhookTestController::class, 'receiveSalesInvoice']);
    Route::post('/receiveRefundInvoice', [WebhookTestController::class, 'receiveRefundInvoice']);
});
`)
	nodes, edges := ExtractRoutes(src, "app/Http/routes.webhooks.php")

	if len(nodes) != 2 {
		t.Fatalf("expected 2 route nodes, got %d", len(nodes))
	}
	if nodes[0].SymbolName != "POST /v1/webhook/receiveSalesInvoice" {
		t.Errorf("unexpected first route %q", nodes[0].SymbolName)
	}
	if nodes[1].SymbolName != "POST /v1/webhook/receiveRefundInvoice" {
		t.Errorf("unexpected second route %q", nodes[1].SymbolName)
	}
	if findHandleEdge(edges, "WebhookTestController.receiveSalesInvoice") == nil {
		t.Fatal("expected fully qualified array handler target")
	}
	if findHandleEdge(edges, "WebhookTestController.receiveRefundInvoice") == nil {
		t.Fatal("expected fully qualified array handler target")
	}
}

func TestExtractRoutes_MatchAnyAndResource(t *testing.T) {
	src := []byte(`<?php
Route::match(['get', 'post'], '/auth/login', [AuthController::class, 'login']);
Route::any('/sync', SyncController::class);
Route::apiResource('/v1/orders', OrderController::class);
`)
	nodes, edges := ExtractRoutes(src, "routes/api.php")

	if len(nodes) != 12 {
		t.Fatalf("expected 12 route nodes, got %d", len(nodes))
	}
	for _, want := range []string{
		"GET /auth/login",
		"POST /auth/login",
		"GET /sync",
		"DELETE /sync",
		"GET /v1/orders",
		"POST /v1/orders",
		"GET /v1/orders/{id}",
		"PUT /v1/orders/{id}",
		"DELETE /v1/orders/{id}",
	} {
		if findNodeBySymbol(nodes, want) == nil {
			t.Errorf("expected route %q", want)
		}
	}
	if findHandleEdge(edges, "AuthController.login") == nil {
		t.Fatal("expected match handler edge")
	}
	if findHandleEdge(edges, "SyncController.__invoke") == nil {
		t.Fatal("expected invokable controller edge")
	}
	if findHandleEdge(edges, "OrderController.index") == nil || findHandleEdge(edges, "OrderController.destroy") == nil {
		t.Fatal("expected apiResource handler edges")
	}
}

func TestExtractRoutes_ContentSum(t *testing.T) {
	src := []byte(`<?php
Route::post('/v1/order', ['as' => 'createOrder', 'uses' => 'OrderController@create']);
`)
	nodes, _ := ExtractRoutes(src, "app/Http/routes.php")

	if len(nodes) != 1 {
		t.Fatalf("expected 1 route node, got %d", len(nodes))
	}
	got := nodes[0].ContentSum
	for _, wantPart := range []string{
		"POST /v1/order",
		"v1 order",
		"api endpoint",
		"OrderController.create",
	} {
		if !strings.Contains(got, wantPart) {
			t.Errorf("expected ContentSum to contain %q, got %q", wantPart, got)
		}
	}
}

func TestExtractRoutes_NoHandler(t *testing.T) {
	src := []byte(`<?php
Route::get('/health', function() { return 'ok'; });
`)
	nodes, edges := ExtractRoutes(src, "routes/web.php")

	if len(nodes) != 1 {
		t.Fatalf("expected 1 route node, got %d", len(nodes))
	}
	if nodes[0].SymbolName != "GET /health" {
		t.Errorf("expected 'GET /health', got %q", nodes[0].SymbolName)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for closure route, got %d", len(edges))
	}
}

func TestExtractJSRoutes_MountedRouterPrefix(t *testing.T) {
	src := []byte(`
const router = express.Router();
router.get('/users', controller.listUsers);
app.use('/api/v1', router);
`)
	nodes, edges := ExtractJSRoutes(src, "routes.js")

	if len(nodes) != 1 {
		t.Fatalf("expected 1 JS route, got %d", len(nodes))
	}
	if nodes[0].SymbolName != "GET /api/v1/users" {
		t.Fatalf("expected mounted path, got %q", nodes[0].SymbolName)
	}
	if findHandleEdge(edges, "listUsers") == nil {
		t.Fatal("expected JS handler edge")
	}
}

func TestExtractJSRoutes_NestDecorators(t *testing.T) {
	src := []byte(`
@Controller('/users')
export class UsersController {
  @Get('/:id')
  getUser() {
    return {};
  }
}
`)
	nodes, edges := ExtractJSRoutes(src, "users.controller.ts")

	if len(nodes) != 1 {
		t.Fatalf("expected 1 Nest route, got %d", len(nodes))
	}
	if nodes[0].SymbolName != "GET /users/:id" {
		t.Fatalf("expected controller + method path, got %q", nodes[0].SymbolName)
	}
	if findHandleEdge(edges, "UsersController.getUser") == nil {
		t.Fatal("expected fully qualified Nest handler")
	}
}

func TestParseFile_Go_RouteNodes(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "routes.go", `package main

import "net/http"

func registerRoutes() {
	http.HandleFunc("/status", statusHandler)
}

func statusHandler(w http.ResponseWriter, r *http.Request) {}
`)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile Go: %v", err)
	}
	if findNodeBySymbol(result.Nodes, "ANY /status") == nil {
		t.Fatal("expected Go route node from http.HandleFunc")
	}
	if findHandleEdge(result.Edges, "statusHandler") == nil {
		t.Fatal("expected Go handler edge")
	}
}

func TestParseFile_JS_RouteNodes(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "app.js", `
const router = express.Router();
router.post('/orders', createOrder);
app.use('/api', router);
function createOrder() {}
`)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile JS: %v", err)
	}
	if findNodeBySymbol(result.Nodes, "POST /api/orders") == nil {
		t.Fatal("expected JS route node from parser integration")
	}
}

func TestParseFile_TS_NestRouteNodes(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "users.controller.ts", `
@Controller('/users')
export class UsersController {
  @Post('/')
  create() {
    return {};
  }
}
`)

	p := New()
	result, err := p.ParseFile(path, dir)
	if err != nil {
		t.Fatalf("ParseFile TS: %v", err)
	}
	if findNodeBySymbol(result.Nodes, "POST /users/") == nil && findNodeBySymbol(result.Nodes, "POST /users") == nil {
		t.Fatal("expected TS route node from Nest decorators")
	}
	if findHandleEdge(result.Edges, "UsersController.create") == nil {
		t.Fatal("expected TS handler edge")
	}
}

func TestIsRouteFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"app/Http/routes.php", true},
		{"app/Http/routes.v1.php", true},
		{"app/Http/routesWeb.v2.php", true},
		{"app/Http/routes.webhooks.php", true},
		{"routes/web.php", true},
		{"routes/api.php", true},
		{"app/Http/Controllers/OrderController.php", false},
		{"app/Order.php", false},
		{"config/app.php", false},
	}
	for _, tt := range tests {
		got := isRouteFile(tt.path)
		if got != tt.want {
			t.Errorf("isRouteFile(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
