//go:build fts5

package parser

import (
	"strings"
	"testing"

	"github.com/naman/qb-context/internal/types"
)

func TestExtractRoutes_BasicRoute(t *testing.T) {
	src := []byte(`<?php
Route::get('/status', ['as' => 'status', 'uses' => 'statusController@QBHealthCheck']);
Route::post('v1/merchant/{chainID}/getDashboardStats', ['as' => 'getDashboardStats', 'uses' => 'chartController@getDashboardStats']);
`)
	nodes, edges := ExtractRoutes(src, "app/Http/routes.php")

	if len(nodes) != 2 {
		t.Fatalf("expected 2 route nodes, got %d", len(nodes))
	}

	// Check first route
	if nodes[0].SymbolName != "GET /status" {
		t.Errorf("expected 'GET /status', got %q", nodes[0].SymbolName)
	}
	if nodes[0].NodeType != types.NodeTypeRoute {
		t.Errorf("expected NodeTypeRoute, got %v", nodes[0].NodeType)
	}

	// Check second route
	if nodes[1].SymbolName != "POST /v1/merchant/{chainID}/getDashboardStats" {
		t.Errorf("expected 'POST /v1/merchant/{chainID}/getDashboardStats', got %q", nodes[1].SymbolName)
	}

	// Check edges
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}
	if edges[0].EdgeType != types.EdgeTypeHandles {
		t.Errorf("expected EdgeTypeHandles, got %v", edges[0].EdgeType)
	}
	if edges[0].TargetSymbol != "QBHealthCheck" {
		t.Errorf("expected target symbol 'QBHealthCheck', got %q", edges[0].TargetSymbol)
	}
	if edges[1].TargetSymbol != "getDashboardStats" {
		t.Errorf("expected target symbol 'getDashboardStats', got %q", edges[1].TargetSymbol)
	}
}

func TestExtractRoutes_GroupPrefix(t *testing.T) {
	src := []byte(`<?php
Route::group(['prefix' => 'v1/webhook'], function () {
    Route::post('/receiveSalesInvoice', ['as' => 'receiveSalesInvoice', 'uses' => 'WebhookTestController@receiveSalesInvoice']);
    Route::post('/receiveRefundInvoice', ['as' => 'receiveRefundInvoice', 'uses' => 'WebhookTestController@receiveRefundInvoice']);
});
`)
	nodes, _ := ExtractRoutes(src, "app/Http/routes.webhooks.php")

	if len(nodes) < 2 {
		t.Fatalf("expected at least 2 route nodes, got %d", len(nodes))
	}

	// Routes should have group prefix applied
	for _, n := range nodes {
		if !strings.Contains(n.SymbolName, "/v1/webhook/") {
			t.Errorf("expected group prefix 'v1/webhook' in route %q", n.SymbolName)
		}
	}

	// Verify full paths
	if nodes[0].SymbolName != "POST /v1/webhook/receiveSalesInvoice" {
		t.Errorf("expected 'POST /v1/webhook/receiveSalesInvoice', got %q", nodes[0].SymbolName)
	}
	if nodes[1].SymbolName != "POST /v1/webhook/receiveRefundInvoice" {
		t.Errorf("expected 'POST /v1/webhook/receiveRefundInvoice', got %q", nodes[1].SymbolName)
	}
}

func TestExtractRoutes_NestedGroups(t *testing.T) {
	src := []byte(`<?php
Route::group(['prefix' => 'v1'], function () {
    Route::group(['prefix' => 'merchant'], function () {
        Route::get('/list', ['as' => 'merchantList', 'uses' => 'MerchantController@list']);
    });
});
`)
	nodes, _ := ExtractRoutes(src, "routes/api.php")

	if len(nodes) != 1 {
		t.Fatalf("expected 1 route node, got %d", len(nodes))
	}

	if nodes[0].SymbolName != "GET /v1/merchant/list" {
		t.Errorf("expected 'GET /v1/merchant/list', got %q", nodes[0].SymbolName)
	}
}

func TestExtractRoutes_NoRoutes(t *testing.T) {
	src := []byte(`<?php
class OrderController {
    public function index() { return "hello"; }
}
`)
	nodes, edges := ExtractRoutes(src, "app/Http/Controllers/OrderController.php")
	if len(nodes) != 0 {
		t.Errorf("expected 0 route nodes from non-route file, got %d", len(nodes))
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges from non-route file, got %d", len(edges))
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
	want := "POST /v1/order -> OrderController@create"
	if nodes[0].ContentSum != want {
		t.Errorf("expected ContentSum %q, got %q", want, nodes[0].ContentSum)
	}
}

func TestExtractRoutes_NoHandler(t *testing.T) {
	// Route without 'uses' => should still create a node but no edge
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
