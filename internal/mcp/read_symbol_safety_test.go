//go:build fts5

package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maplenk/context-mcp/internal/storage"
	"github.com/maplenk/context-mcp/internal/types"
)

type largeControllerFixture struct {
	Deps          ToolDeps
	Cleanup       func()
	RelPath       string
	AbsPath       string
	ClassSymbol   string
	ProcessSymbol string
	SmallSymbol   string
}

type readSymbolTestResponse struct {
	SymbolName        string                `json:"symbol_name"`
	FilePath          string                `json:"file_path"`
	NodeType          string                `json:"node_type"`
	ModeRequested     string                `json:"mode_requested"`
	ModeUsed          string                `json:"mode_used"`
	Source            string                `json:"source"`
	SymbolStartLine   int                   `json:"symbol_start_line"`
	SymbolEndLine     int                   `json:"symbol_end_line"`
	SelectedStartLine int                   `json:"selected_start_line"`
	SelectedEndLine   int                   `json:"selected_end_line"`
	Signature         string                `json:"signature"`
	NextModes         []string              `json:"next_modes"`
	Downgraded        bool                  `json:"downgraded"`
	DowngradeReason   string                `json:"downgrade_reason"`
	Truncated         bool                  `json:"truncated"`
	AppliedMaxChars   int                   `json:"applied_max_chars"`
	AppliedMaxLines   int                   `json:"applied_max_lines"`
	FlowSummary       readSymbolFlowSummary `json:"flow_summary"`
}

type listFileSymbolsTestResponse struct {
	Path      string `json:"path"`
	Count     int    `json:"count"`
	Total     int    `json:"total"`
	Truncated bool   `json:"truncated"`
	Symbols   []struct {
		ID             string            `json:"id"`
		SymbolName     string            `json:"symbol_name"`
		NodeType       string            `json:"node_type"`
		StartLine      int               `json:"start_line"`
		EndLine        int               `json:"end_line"`
		Signature      string            `json:"signature"`
		ReadSymbolArgs map[string]string `json:"read_symbol_args"`
	} `json:"symbols"`
}

func setupLargeControllerFixture(t *testing.T) largeControllerFixture {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".context-mcp", "test.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	relPath := filepath.Join("app", "Http", "Controllers", "OrderController.php")
	absPath := filepath.Join(tmpDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	content := buildLargeControllerSource()
	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	nodes := buildLargeControllerNodes(t, relPath, content)
	if err := store.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	return largeControllerFixture{
		Deps: ToolDeps{
			Store:    store,
			RepoRoot: tmpDir,
			Profile:  "extended",
		},
		Cleanup: func() {
			store.Close()
		},
		RelPath:       relPath,
		AbsPath:       absPath,
		ClassSymbol:   "OrderController",
		ProcessSymbol: "OrderController.processOrder",
		SmallSymbol:   "OrderController.helper001",
	}
}

func buildLargeControllerSource() string {
	var b strings.Builder
	b.WriteString("<?php\n\n")
	b.WriteString("class OrderController {\n")
	b.WriteString("    public function __construct() {\n")
	b.WriteString("        $this->client = Http::baseUrl('https://example.test');\n")
	b.WriteString("    }\n\n")
	b.WriteString("    public function processOrder($request) {\n")
	b.WriteString("        $validated = $this->validatePayload($request);\n")
	b.WriteString("        if (!$validated) {\n")
	b.WriteString("            return false;\n")
	b.WriteString("        }\n")
	b.WriteString("        DB::table('orders')->insert(['status' => 'new']);\n")
	b.WriteString("        dispatch(new SyncOrderJob());\n")
	b.WriteString("        event(new OrderCreatedEvent());\n")
	b.WriteString("        Mail::to($request->email)->send(new ReceiptMail());\n")
	b.WriteString("        Http::post('https://example.test/orders', []);\n")
	for i := 0; i < 260; i++ {
		fmt.Fprintf(&b, "        $step%03d = $this->helper%03d($request);\n", i, (i%180)+1)
		if i%17 == 0 {
			b.WriteString("        if ($request->has('coupon')) {\n")
			b.WriteString("            $this->applyCoupon($request);\n")
			b.WriteString("        }\n")
		}
		if i%29 == 0 {
			b.WriteString("        try {\n")
			b.WriteString("            $this->syncInventory($request);\n")
			b.WriteString("        } catch (\\Throwable $e) {\n")
			b.WriteString("            report($e);\n")
			b.WriteString("        }\n")
		}
	}
	b.WriteString("        return $this->finalizeOrder($request);\n")
	b.WriteString("    }\n\n")

	for i := 1; i <= 420; i++ {
		fmt.Fprintf(&b, "    public function helper%03d($request) {\n", i)
		fmt.Fprintf(&b, "        if ($request->has('flag%03d')) {\n", i)
		fmt.Fprintf(&b, "            return '%03d';\n", i)
		b.WriteString("        }\n")
		b.WriteString("        return $request;\n")
		b.WriteString("    }\n\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func buildLargeControllerNodes(t *testing.T, relPath, content string) []types.ASTNode {
	t.Helper()

	classStart := strings.Index(content, "class OrderController")
	if classStart < 0 {
		t.Fatal("class OrderController not found")
	}
	classEnd := findBlockEnd(content, classStart)

	nodes := []types.ASTNode{
		{
			ID:         types.GenerateNodeID(relPath, "OrderController"),
			FilePath:   relPath,
			SymbolName: "OrderController",
			NodeType:   types.NodeTypeClass,
			StartByte:  uint32(classStart),
			EndByte:    uint32(classEnd),
			ContentSum: "large laravel order controller",
		},
	}

	methodNames := []string{"__construct", "processOrder"}
	for i := 1; i <= 420; i++ {
		methodNames = append(methodNames, fmt.Sprintf("helper%03d", i))
	}
	for _, name := range methodNames {
		marker := "    public function " + name
		start := strings.Index(content, marker)
		if start < 0 {
			t.Fatalf("method %s not found", name)
		}
		end := findBlockEnd(content, start)
		nodes = append(nodes, types.ASTNode{
			ID:         types.GenerateNodeID(relPath, "OrderController."+name),
			FilePath:   relPath,
			SymbolName: "OrderController." + name,
			NodeType:   types.NodeTypeMethod,
			StartByte:  uint32(start),
			EndByte:    uint32(end),
			ContentSum: "controller method " + name,
		})
	}
	return nodes
}

func findBlockEnd(content string, start int) int {
	openBrace := strings.Index(content[start:], "{")
	if openBrace < 0 {
		return start
	}
	openBrace += start
	depth := 1
	for i := openBrace + 1; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(content)
}

func decodeToolResult(t *testing.T, result interface{}, target interface{}) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal result: %v", err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
}

func TestReadSymbol_BoundedByDefaultLargeController(t *testing.T) {
	fixture := setupLargeControllerFixture(t)
	defer fixture.Cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, fixture.Deps, nil)

	handler, ok := server.GetHandler("read_symbol")
	if !ok {
		t.Fatal("read_symbol handler not registered")
	}

	params, _ := json.Marshal(ReadSymbolParams{SymbolID: fixture.ClassSymbol})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("read_symbol error: %v", err)
	}

	var resp readSymbolTestResponse
	decodeToolResult(t, result, &resp)

	if resp.ModeUsed != "bounded" {
		t.Fatalf("mode_used = %q, want bounded", resp.ModeUsed)
	}
	if resp.Source == "" {
		t.Fatal("expected bounded source preview")
	}
	if countLines(resp.Source) > defaultReadSymbolMaxLines {
		t.Fatalf("bounded preview lines = %d, want <= %d", countLines(resp.Source), defaultReadSymbolMaxLines)
	}
	if countRunes(resp.Source) > defaultReadSymbolMaxChars {
		t.Fatalf("bounded preview chars = %d, want <= %d", countRunes(resp.Source), defaultReadSymbolMaxChars)
	}
	if resp.Signature == "" {
		t.Fatal("expected signature in bounded response")
	}
	if resp.SymbolStartLine == 0 || resp.SymbolEndLine == 0 {
		t.Fatal("expected symbol line span in response")
	}
	if len(resp.NextModes) == 0 {
		t.Fatal("expected next_modes in response")
	}
}

func TestReadSymbol_FullDowngradesLargeController(t *testing.T) {
	fixture := setupLargeControllerFixture(t)
	defer fixture.Cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, fixture.Deps, nil)

	handler, _ := server.GetHandler("read_symbol")
	params, _ := json.Marshal(ReadSymbolParams{SymbolID: fixture.ClassSymbol, Mode: "full"})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("read_symbol error: %v", err)
	}

	var resp readSymbolTestResponse
	decodeToolResult(t, result, &resp)

	if resp.ModeUsed != "bounded" {
		t.Fatalf("mode_used = %q, want bounded after downgrade", resp.ModeUsed)
	}
	if !resp.Downgraded {
		t.Fatal("expected downgraded=true for large full read")
	}
	if resp.DowngradeReason != "symbol_exceeds_safe_read_threshold" {
		t.Fatalf("downgrade_reason = %q", resp.DowngradeReason)
	}
	if resp.Source == "" {
		t.Fatal("expected bounded preview after downgrade")
	}
}

func TestReadSymbol_SectionExplicitRange(t *testing.T) {
	fixture := setupLargeControllerFixture(t)
	defer fixture.Cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, fixture.Deps, nil)

	handler, _ := server.GetHandler("read_symbol")
	params, _ := json.Marshal(ReadSymbolParams{
		SymbolID:  fixture.ClassSymbol,
		Mode:      "section",
		StartLine: 20,
		EndLine:   30,
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("read_symbol error: %v", err)
	}

	var resp readSymbolTestResponse
	decodeToolResult(t, result, &resp)

	if resp.SelectedStartLine != 20 || resp.SelectedEndLine != 30 {
		t.Fatalf("selected lines = %d-%d, want 20-30", resp.SelectedStartLine, resp.SelectedEndLine)
	}
	if countLines(resp.Source) != 11 {
		t.Fatalf("section source lines = %d, want 11", countLines(resp.Source))
	}
}

func TestReadSymbol_FlowSummarySchema(t *testing.T) {
	fixture := setupLargeControllerFixture(t)
	defer fixture.Cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, fixture.Deps, nil)

	handler, _ := server.GetHandler("read_symbol")
	params, _ := json.Marshal(ReadSymbolParams{
		SymbolID: fixture.ProcessSymbol,
		Mode:     "flow_summary",
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("read_symbol error: %v", err)
	}

	var resp readSymbolTestResponse
	decodeToolResult(t, result, &resp)

	if resp.ModeUsed != "flow_summary" {
		t.Fatalf("mode_used = %q, want flow_summary", resp.ModeUsed)
	}
	if resp.Source != "" {
		t.Fatal("flow_summary should not return raw source")
	}
	if len(resp.FlowSummary.Steps) == 0 {
		t.Fatal("expected flow summary steps")
	}
	if resp.FlowSummary.Summary == "" {
		t.Fatal("expected flow summary summary text")
	}
	if resp.FlowSummary.SideEffects.DBWrites == nil || resp.FlowSummary.SideEffects.Jobs == nil ||
		resp.FlowSummary.SideEffects.Events == nil || resp.FlowSummary.SideEffects.Notifications == nil ||
		resp.FlowSummary.SideEffects.Integrations == nil {
		t.Fatal("expected all flow_summary side effect arrays to be present")
	}
}

func TestReadSymbol_FlowSummaryCapsLargeMethods(t *testing.T) {
	fixture := setupLargeControllerFixture(t)
	defer fixture.Cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, fixture.Deps, nil)

	handler, _ := server.GetHandler("read_symbol")
	params, _ := json.Marshal(ReadSymbolParams{
		SymbolID: fixture.ProcessSymbol,
		Mode:     "flow_summary",
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("read_symbol error: %v", err)
	}

	var resp readSymbolTestResponse
	decodeToolResult(t, result, &resp)

	if len(resp.FlowSummary.Steps) > flowSummaryMaxSteps {
		t.Fatalf("steps = %d, want <= %d", len(resp.FlowSummary.Steps), flowSummaryMaxSteps)
	}
	if len(resp.FlowSummary.HelperCalls) > flowSummaryMaxHelperCalls {
		t.Fatalf("helper_calls = %d, want <= %d", len(resp.FlowSummary.HelperCalls), flowSummaryMaxHelperCalls)
	}
	if len(resp.FlowSummary.Validations) > flowSummaryMaxValidations {
		t.Fatalf("validations = %d, want <= %d", len(resp.FlowSummary.Validations), flowSummaryMaxValidations)
	}
	if len(resp.FlowSummary.SideEffects.DBWrites) > flowSummaryMaxSideEffects {
		t.Fatalf("db_writes = %d, want <= %d", len(resp.FlowSummary.SideEffects.DBWrites), flowSummaryMaxSideEffects)
	}
	if len(resp.FlowSummary.SideEffects.Jobs) > flowSummaryMaxSideEffects {
		t.Fatalf("jobs = %d, want <= %d", len(resp.FlowSummary.SideEffects.Jobs), flowSummaryMaxSideEffects)
	}
	if len(resp.FlowSummary.SideEffects.Events) > flowSummaryMaxSideEffects {
		t.Fatalf("events = %d, want <= %d", len(resp.FlowSummary.SideEffects.Events), flowSummaryMaxSideEffects)
	}
	if len(resp.FlowSummary.SideEffects.Notifications) > flowSummaryMaxSideEffects {
		t.Fatalf("notifications = %d, want <= %d", len(resp.FlowSummary.SideEffects.Notifications), flowSummaryMaxSideEffects)
	}
	if len(resp.FlowSummary.SideEffects.Integrations) > flowSummaryMaxSideEffects {
		t.Fatalf("integrations = %d, want <= %d", len(resp.FlowSummary.SideEffects.Integrations), flowSummaryMaxSideEffects)
	}
	if !resp.FlowSummary.Truncated {
		t.Fatal("expected flow_summary to report truncation for large methods")
	}
}

func TestReadSymbol_FullHonoredForSmallMethod(t *testing.T) {
	fixture := setupLargeControllerFixture(t)
	defer fixture.Cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, fixture.Deps, nil)

	handler, _ := server.GetHandler("read_symbol")
	params, _ := json.Marshal(ReadSymbolParams{
		SymbolID: fixture.SmallSymbol,
		Mode:     "full",
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("read_symbol error: %v", err)
	}

	var resp readSymbolTestResponse
	decodeToolResult(t, result, &resp)

	if resp.ModeUsed != "full" {
		t.Fatalf("mode_used = %q, want full", resp.ModeUsed)
	}
	if resp.Downgraded {
		t.Fatal("did not expect downgrade for a small method")
	}
	if !strings.Contains(resp.Source, "return $request;") {
		t.Fatalf("expected full source for helper method, got: %q", resp.Source)
	}
}

func TestReadSymbol_ClampLimits(t *testing.T) {
	fixture := setupLargeControllerFixture(t)
	defer fixture.Cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, fixture.Deps, nil)

	handler, _ := server.GetHandler("read_symbol")
	params, _ := json.Marshal(ReadSymbolParams{
		SymbolID: fixture.ClassSymbol,
		Mode:     "section",
		Section:  "top",
		MaxLines: 500,
		MaxChars: 50000,
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("read_symbol error: %v", err)
	}

	var resp readSymbolTestResponse
	decodeToolResult(t, result, &resp)

	if resp.AppliedMaxLines != hardReadSymbolMaxLines {
		t.Fatalf("applied_max_lines = %d, want %d", resp.AppliedMaxLines, hardReadSymbolMaxLines)
	}
	if resp.AppliedMaxChars != hardReadSymbolMaxChars {
		t.Fatalf("applied_max_chars = %d, want %d", resp.AppliedMaxChars, hardReadSymbolMaxChars)
	}
	if countLines(resp.Source) > hardReadSymbolMaxLines {
		t.Fatalf("source lines = %d, want <= %d", countLines(resp.Source), hardReadSymbolMaxLines)
	}
	if countRunes(resp.Source) > hardReadSymbolMaxChars {
		t.Fatalf("source chars = %d, want <= %d", countRunes(resp.Source), hardReadSymbolMaxChars)
	}
}

func TestListFileSymbols_UsesSafeReadArgsAndSourceOrder(t *testing.T) {
	fixture := setupLargeControllerFixture(t)
	defer fixture.Cleanup()

	server := NewServerWithIO(nil, nil)
	RegisterTools(server, fixture.Deps, nil)

	handler, ok := server.GetHandler("list_file_symbols")
	if !ok {
		t.Fatal("list_file_symbols handler not registered")
	}

	params, _ := json.Marshal(ListFileSymbolsParams{
		Path:  fixture.AbsPath,
		Limit: 25,
		Kinds: []string{"method"},
	})
	result, err := handler(params)
	if err != nil {
		t.Fatalf("list_file_symbols error: %v", err)
	}

	var resp listFileSymbolsTestResponse
	decodeToolResult(t, result, &resp)

	if resp.Path != fixture.RelPath {
		t.Fatalf("path = %q, want %q", resp.Path, fixture.RelPath)
	}
	if resp.Count != 25 {
		t.Fatalf("count = %d, want 25", resp.Count)
	}
	if resp.Total <= resp.Count {
		t.Fatalf("expected total > count for truncated inventory, got total=%d count=%d", resp.Total, resp.Count)
	}
	if !resp.Truncated {
		t.Fatal("expected truncated=true when limit cuts off the method inventory")
	}
	if len(resp.Symbols) == 0 {
		t.Fatal("expected symbols in response")
	}
	if resp.Symbols[0].SymbolName != "OrderController.__construct" {
		t.Fatalf("first method = %q, want OrderController.__construct", resp.Symbols[0].SymbolName)
	}
	if resp.Symbols[0].ReadSymbolArgs["mode"] != "bounded" {
		t.Fatalf("read_symbol_args.mode = %q, want bounded", resp.Symbols[0].ReadSymbolArgs["mode"])
	}
	if resp.Symbols[0].ReadSymbolArgs["symbol_id"] == "" {
		t.Fatal("expected read_symbol_args.symbol_id")
	}
	if len(resp.Symbols) > 1 && resp.Symbols[0].StartLine >= resp.Symbols[1].StartLine {
		t.Fatalf("expected source-ordered symbols, got %d then %d", resp.Symbols[0].StartLine, resp.Symbols[1].StartLine)
	}
}
