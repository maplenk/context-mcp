package parser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsFileDocCandidate(t *testing.T) {
	tests := []struct {
		relPath string
		want    bool
	}{
		// Positive: config PHP
		{"config/app.php", true},
		{"config/session.php", true},
		{"config/database.php", true},
		{"config/opentelemetry.php", true},

		// Positive: Blade templates
		{"resources/views/invoices/order.blade.php", true},
		{"resources/views/emails/orderplacedemail.blade.php", true},

		// Positive: SQL schemas
		{"cloud_schema/chain.sql", true},
		{"cloud_schema/store.sql", true},

		// Negative: regular PHP (handled by tree-sitter)
		{"app/Order.php", false},
		{"app/Http/Controllers/OrderController.php", false},

		// Negative: other file types
		{"README.md", false},
		{"package.json", false},
		{".env", false},
		{"app/Http/routes.php", false}, // route files are PHP, handled by tree-sitter + route extractor
	}

	for _, tt := range tests {
		t.Run(tt.relPath, func(t *testing.T) {
			got := IsFileDocCandidate(tt.relPath)
			if got != tt.want {
				t.Errorf("IsFileDocCandidate(%q) = %v, want %v", tt.relPath, got, tt.want)
			}
		})
	}
}

func TestCreateFileDocNode(t *testing.T) {
	// Create a temp config file
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	os.MkdirAll(configDir, 0755)

	content := `<?php
return [
    'driver' => env('SESSION_DRIVER', 'file'),
    'lifetime' => env('SESSION_LIFETIME', 120),
    'expire_on_close' => false,
];`

	configFile := filepath.Join(configDir, "session.php")
	os.WriteFile(configFile, []byte(content), 0644)

	node, err := CreateFileDocNode(configFile, "config/session.php")
	if err != nil {
		t.Fatalf("CreateFileDocNode: %v", err)
	}

	if node.NodeType != 6 { // NodeTypeFile
		t.Errorf("expected NodeTypeFile (6), got %v", node.NodeType)
	}
	if node.SymbolName != "config/session.php" {
		t.Errorf("expected symbol name 'config/session.php', got %q", node.SymbolName)
	}
	if node.FilePath != "config/session.php" {
		t.Errorf("expected file path 'config/session.php', got %q", node.FilePath)
	}
	// Content should include both filename and content
	if len(node.ContentSum) < 50 {
		t.Errorf("expected substantial content sum, got %d chars: %q", len(node.ContentSum), node.ContentSum)
	}
}

func TestCreateFileDocNode_LargeFile(t *testing.T) {
	dir := t.TempDir()

	// Create a file larger than maxFileDocBytes
	large := make([]byte, 10000)
	for i := range large {
		large[i] = 'A' + byte(i%26)
	}

	largePath := filepath.Join(dir, "large.sql")
	os.WriteFile(largePath, large, 0644)

	node, err := CreateFileDocNode(largePath, "cloud_schema/large.sql")
	if err != nil {
		t.Fatalf("CreateFileDocNode: %v", err)
	}

	// Content should be truncated
	if len(node.ContentSum) > maxFileDocBytes+200 { // allow overhead for filename + "..."
		t.Errorf("content sum too large: %d bytes (max should be ~%d)", len(node.ContentSum), maxFileDocBytes)
	}
}
