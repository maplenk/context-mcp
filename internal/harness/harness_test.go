package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// removeTomlSection
// ---------------------------------------------------------------------------

func TestRemoveTomlSection_Middle(t *testing.T) {
	content := `[server.alpha]
host = "a"

[mcp_servers.context-mcp]
command = "/usr/bin/qb"
args = ["--repo", "/tmp"]

[server.beta]
host = "b"
`
	got := removeTomlSection(content, "mcp_servers.context-mcp")
	if strings.Contains(got, "context-mcp") {
		t.Fatalf("section not removed:\n%s", got)
	}
	if !strings.Contains(got, "[server.alpha]") || !strings.Contains(got, "[server.beta]") {
		t.Fatalf("surrounding sections damaged:\n%s", got)
	}
}

func TestRemoveTomlSection_Start(t *testing.T) {
	content := `[mcp_servers.context-mcp]
command = "/usr/bin/qb"

[other]
key = "val"
`
	got := removeTomlSection(content, "mcp_servers.context-mcp")
	if strings.Contains(got, "context-mcp") {
		t.Fatalf("section not removed:\n%s", got)
	}
	if !strings.Contains(got, "[other]") {
		t.Fatalf("following section damaged:\n%s", got)
	}
}

func TestRemoveTomlSection_End(t *testing.T) {
	content := `[other]
key = "val"

[mcp_servers.context-mcp]
command = "/usr/bin/qb"
args = ["--repo", "/tmp"]
`
	got := removeTomlSection(content, "mcp_servers.context-mcp")
	if strings.Contains(got, "context-mcp") {
		t.Fatalf("section not removed:\n%s", got)
	}
	if !strings.Contains(got, "[other]") {
		t.Fatalf("preceding section damaged:\n%s", got)
	}
}

func TestRemoveTomlSection_NotFound(t *testing.T) {
	content := `[other]
key = "val"
`
	got := removeTomlSection(content, "mcp_servers.context-mcp")
	if !strings.Contains(got, "[other]") {
		t.Fatalf("content should be unchanged:\n%s", got)
	}
}

func TestRemoveTomlSection_NestedSubtables(t *testing.T) {
	content := `[mcp_servers.context-mcp]
command = "/usr/local/bin/context-mcp"
args = ["--repo", "/path"]

[mcp_servers.context-mcp.tools.context]
enabled = true

[mcp_servers.context-mcp.tools.impact]
enabled = false

[mcp_servers.other]
command = "other"
`
	got := removeTomlSection(content, "mcp_servers.context-mcp")
	if strings.Contains(got, "context-mcp") {
		t.Fatalf("section and subtables should be removed:\n%s", got)
	}
	if !strings.Contains(got, "[mcp_servers.other]") {
		t.Fatalf("unrelated section should survive:\n%s", got)
	}
}

func TestRemoveTomlSection_NestedSubtablesAtEnd(t *testing.T) {
	content := `[mcp_servers.other]
command = "other"

[mcp_servers.context-mcp]
command = "/usr/local/bin/context-mcp"

[mcp_servers.context-mcp.tools.context]
enabled = true
`
	got := removeTomlSection(content, "mcp_servers.context-mcp")
	if strings.Contains(got, "context-mcp") {
		t.Fatalf("section and subtables at EOF should be removed:\n%s", got)
	}
	if !strings.Contains(got, "[mcp_servers.other]") {
		t.Fatalf("preceding section should survive:\n%s", got)
	}
}

func TestRemoveTomlSection_SimilarPrefixNotRemoved(t *testing.T) {
	// "context-mcp-extra" is NOT a child of "context-mcp" — must survive.
	content := `[mcp_servers.context-mcp]
command = "ctx"

[mcp_servers.context-mcp-extra]
command = "extra"
`
	got := removeTomlSection(content, "mcp_servers.context-mcp")
	if !strings.Contains(got, "[mcp_servers.context-mcp-extra]") {
		t.Fatalf("similarly-prefixed section should NOT be removed:\n%s", got)
	}
	if strings.Contains(got, "command = \"ctx\"") {
		t.Fatalf("original section content should be removed:\n%s", got)
	}
}

func TestRemoveTomlSection_OnlySection(t *testing.T) {
	content := `[mcp_servers.context-mcp]
command = "/usr/bin/qb"
args = ["--repo", "/tmp"]
`
	got := removeTomlSection(content, "mcp_servers.context-mcp")
	if strings.TrimSpace(got) != "" {
		t.Fatalf("expected empty result, got:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// tomlStringArray
// ---------------------------------------------------------------------------

func TestTomlStringArray(t *testing.T) {
	tests := []struct {
		input []string
		want  string
	}{
		{[]string{"--repo", "/tmp/myrepo"}, `["--repo", "/tmp/myrepo"]`},
		{[]string{"single"}, `["single"]`},
		{nil, `[]`},
	}
	for _, tt := range tests {
		got := tomlStringArray(tt.input)
		if got != tt.want {
			t.Errorf("tomlStringArray(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// parseSimpleTOMLArray
// ---------------------------------------------------------------------------

func TestParseSimpleTOMLArray(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{`["--repo", "/tmp/myrepo"]`, []string{"--repo", "/tmp/myrepo"}},
		{`["single"]`, []string{"single"}},
		{`[]`, nil},
		{`not-an-array`, nil},
		{`["--repo", "/path", "--profile", "core"]`, []string{"--repo", "/path", "--profile", "core"}},
	}
	for _, tt := range tests {
		got := parseSimpleTOMLArray(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("parseSimpleTOMLArray(%q) = %v (len %d), want %v (len %d)",
				tt.input, got, len(got), tt.want, len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parseSimpleTOMLArray(%q)[%d] = %q, want %q",
					tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// buildArgs
// ---------------------------------------------------------------------------

func TestBuildArgs(t *testing.T) {
	got := buildArgs(InstallOpts{RepoRoot: "/my/repo", Profile: "core"})
	if len(got) != 4 || got[0] != "--repo" || got[1] != "/my/repo" || got[2] != "--profile" || got[3] != "core" {
		t.Fatalf("unexpected args: %v", got)
	}

	got2 := buildArgs(InstallOpts{RepoRoot: "/my/repo"})
	if len(got2) != 2 || got2[0] != "--repo" || got2[1] != "/my/repo" {
		t.Fatalf("unexpected args without profile: %v", got2)
	}
}

// ---------------------------------------------------------------------------
// buildCodexTOML
// ---------------------------------------------------------------------------

func TestBuildCodexTOML(t *testing.T) {
	block := buildCodexTOML("/usr/local/bin/qb", InstallOpts{RepoRoot: "/my/repo", Profile: "extended"})
	if !strings.Contains(block, "[mcp_servers.context-mcp]") {
		t.Fatal("missing section header")
	}
	if !strings.Contains(block, `command = "/usr/local/bin/qb"`) {
		t.Fatal("missing command line")
	}
	if !strings.Contains(block, `"--repo"`) {
		t.Fatal("missing --repo in args")
	}
	if !strings.Contains(block, `"--profile"`) {
		t.Fatal("missing --profile in args")
	}
}

// ---------------------------------------------------------------------------
// extractRepoArg (JSON entry)
// ---------------------------------------------------------------------------

func TestExtractRepoArg(t *testing.T) {
	entry := map[string]any{
		"command": "/usr/bin/qb",
		"args":    []any{"--repo", "/my/repo", "--profile", "core"},
	}
	got := extractRepoArg(entry)
	if got != "/my/repo" {
		t.Fatalf("extractRepoArg = %q, want %q", got, "/my/repo")
	}

	// No --repo flag.
	entry2 := map[string]any{
		"command": "/usr/bin/qb",
		"args":    []any{"--profile", "core"},
	}
	if extractRepoArg(entry2) != "" {
		t.Fatal("expected empty string when --repo absent")
	}

	// No args key.
	entry3 := map[string]any{"command": "/usr/bin/qb"}
	if extractRepoArg(entry3) != "" {
		t.Fatal("expected empty string when args missing")
	}

	// --repo is the last element (no value follows).
	entry4 := map[string]any{
		"command": "/usr/bin/qb",
		"args":    []any{"--repo"},
	}
	if extractRepoArg(entry4) != "" {
		t.Fatal("expected empty string when --repo has no value")
	}
}

// ---------------------------------------------------------------------------
// extractRepoArgFromTOML
// ---------------------------------------------------------------------------

func TestExtractRepoArgFromTOML(t *testing.T) {
	content := `[other]
key = "val"

[mcp_servers.context-mcp]
command = "/usr/bin/qb"
args = ["--repo", "/my/repo", "--profile", "core"]

[another]
key2 = "val2"
`
	got := extractRepoArgFromTOML(content)
	if got != "/my/repo" {
		t.Fatalf("extractRepoArgFromTOML = %q, want %q", got, "/my/repo")
	}

	// No context-mcp section.
	if extractRepoArgFromTOML("[other]\nkey = \"val\"\n") != "" {
		t.Fatal("expected empty when section missing")
	}

	// Section without args line.
	noArgs := "[mcp_servers.context-mcp]\ncommand = \"/usr/bin/qb\"\n"
	if extractRepoArgFromTOML(noArgs) != "" {
		t.Fatal("expected empty when args line missing")
	}
}

// ---------------------------------------------------------------------------
// Install / Uninstall — unsupported client
// ---------------------------------------------------------------------------

func TestInstall_UnsupportedClient(t *testing.T) {
	_, err := Install(InstallOpts{Client: "unknown"})
	if err == nil {
		t.Fatal("expected error for unsupported client")
	}
	if !strings.Contains(err.Error(), "unsupported client") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUninstall_UnsupportedClient(t *testing.T) {
	_, err := Uninstall(UninstallOpts{Client: "unknown"})
	if err == nil {
		t.Fatal("expected error for unsupported client")
	}
	if !strings.Contains(err.Error(), "unsupported client") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// PrintConfig — unsupported client
// ---------------------------------------------------------------------------

func TestPrintConfig_UnsupportedClient(t *testing.T) {
	_, err := PrintConfig(PrintConfigOpts{Client: "unknown"})
	if err == nil {
		t.Fatal("expected error for unsupported client")
	}
	if !strings.Contains(err.Error(), "unsupported client") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// PrintConfig — valid clients (selfBinary works in test context)
// ---------------------------------------------------------------------------

func TestPrintConfig_ClaudeCode(t *testing.T) {
	out, err := PrintConfig(PrintConfigOpts{
		Client:   ClientClaudeCode,
		RepoRoot: "/tmp/myrepo",
		Profile:  "core",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "claude") {
		t.Fatal("output should mention claude")
	}
	if !strings.Contains(out, "mcpServers") {
		t.Fatal("output should contain JSON snippet")
	}
	if !strings.Contains(out, "--repo") {
		t.Fatal("output should contain --repo")
	}
	if !strings.Contains(out, "/tmp/myrepo") {
		t.Fatal("output should contain the repo path")
	}
}

func TestPrintConfig_Codex(t *testing.T) {
	out, err := PrintConfig(PrintConfigOpts{
		Client:   ClientCodex,
		RepoRoot: "/tmp/myrepo",
		Profile:  "extended",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "[mcp_servers.context-mcp]") {
		t.Fatal("output should contain TOML section header")
	}
	if !strings.Contains(out, "--repo") {
		t.Fatal("output should contain --repo")
	}
}

// ---------------------------------------------------------------------------
// Doctor — binary check (should pass in test context)
// ---------------------------------------------------------------------------

func TestDoctor_BinaryCheck(t *testing.T) {
	checks, err := Doctor(DoctorOpts{})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	if len(checks) == 0 {
		t.Fatal("expected at least one check")
	}
	// The first check should be the binary check and should pass.
	if checks[0].Name != "binary" {
		t.Fatalf("first check should be 'binary', got %q", checks[0].Name)
	}
	if !checks[0].Passed {
		t.Fatalf("binary check should pass in test context: %s", checks[0].Message)
	}
}

// ---------------------------------------------------------------------------
// doctorRepoPaths — with temp directory
// ---------------------------------------------------------------------------

func TestDoctorRepoPaths_ValidRepo(t *testing.T) {
	tmp := t.TempDir()
	// Create the .context-mcp/index.db file.
	dbDir := filepath.Join(tmp, ".context-mcp")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "index.db"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	checks := doctorRepoPaths("test", tmp)
	for _, c := range checks {
		if !c.Passed {
			t.Fatalf("check %q should pass: %s", c.Name, c.Message)
		}
	}
	// Should have repo + index checks.
	if len(checks) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(checks))
	}
}

func TestDoctorRepoPaths_MissingRepo(t *testing.T) {
	checks := doctorRepoPaths("test", "/nonexistent/path/abc123")
	if len(checks) == 0 {
		t.Fatal("expected at least one check")
	}
	if checks[0].Passed {
		t.Fatal("repo check should fail for nonexistent path")
	}
}

func TestDoctorRepoPaths_EmptyPath(t *testing.T) {
	checks := doctorRepoPaths("test", "")
	if len(checks) == 0 {
		t.Fatal("expected at least one check")
	}
	// Now passes with a skip warning instead of failing.
	if !checks[0].Passed {
		t.Fatal("repo check should pass (with skip warning) for empty path")
	}
	if !strings.Contains(checks[0].Message, "not specified") {
		t.Fatalf("unexpected message: %s", checks[0].Message)
	}
}

func TestDoctorRepoPaths_MissingIndex(t *testing.T) {
	tmp := t.TempDir()
	// No .context-mcp/index.db.
	checks := doctorRepoPaths("test", tmp)
	if len(checks) != 2 {
		t.Fatalf("expected 2 checks (repo + index), got %d", len(checks))
	}
	if !checks[0].Passed {
		t.Fatalf("repo check should pass for valid dir: %s", checks[0].Message)
	}
	if checks[1].Passed {
		t.Fatal("index check should fail when index.db missing")
	}
}

// ---------------------------------------------------------------------------
// doctorClaudeCode — with temp config files
// ---------------------------------------------------------------------------

func TestDoctorClaudeCode_MissingConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "nonexistent.json")
	checks := doctorClaudeCode("test", cfgPath, "")
	if len(checks) == 0 {
		t.Fatal("expected checks")
	}
	// Should have config-user fail and overall entry fail.
	entryFailed := false
	for _, c := range checks {
		if c.Name == "test/entry" && !c.Passed {
			entryFailed = true
		}
	}
	if !entryFailed {
		t.Fatal("overall entry check should fail when no config found")
	}
}

func TestDoctorClaudeCode_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, ".claude.json")
	if err := os.WriteFile(cfgPath, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	checks := doctorClaudeCode("test", cfgPath, "")
	// Should have config-user (pass) + entry-user (fail due to parse error) + overall entry (fail).
	found := false
	for _, c := range checks {
		if c.Name == "test/entry" && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatal("expected overall entry check to fail on invalid JSON")
	}
}

func TestDoctorClaudeCode_NoQBEntry(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, ".claude.json")
	data, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"other-server": map[string]any{"command": "/bin/other"},
		},
	})
	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	checks := doctorClaudeCode("test", cfgPath, "")
	found := false
	for _, c := range checks {
		if c.Name == "test/entry" && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatal("expected overall entry check to fail when context-mcp not present")
	}
}

func TestDoctorClaudeCode_ValidEntry(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	dbDir := filepath.Join(repoDir, ".context-mcp")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "index.db"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(tmp, ".claude.json")
	data, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"context-mcp": map[string]any{
				"command": "/usr/bin/qb",
				"args":    []any{"--repo", repoDir},
			},
		},
	})
	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	checks := doctorClaudeCode("test", cfgPath, "")
	for _, c := range checks {
		if !c.Passed {
			t.Fatalf("check %q should pass: %s", c.Name, c.Message)
		}
	}
}

// ---------------------------------------------------------------------------
// doctorCodex — with temp config files
// ---------------------------------------------------------------------------

func TestDoctorCodex_MissingConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "nonexistent.toml")
	checks := doctorCodex("test", cfgPath, "")
	if len(checks) == 0 {
		t.Fatal("expected checks")
	}
	entryFailed := false
	for _, c := range checks {
		if c.Name == "test/entry" && !c.Passed {
			entryFailed = true
		}
	}
	if !entryFailed {
		t.Fatal("overall entry check should fail when no config found")
	}
}

func TestDoctorCodex_NoQBSection(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[other]\nkey = \"val\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	checks := doctorCodex("test", cfgPath, "")
	found := false
	for _, c := range checks {
		if c.Name == "test/entry" && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatal("expected overall entry check to fail when context-mcp section missing")
	}
}

func TestDoctorCodex_ValidEntry(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	dbDir := filepath.Join(repoDir, ".context-mcp")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "index.db"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(tmp, "config.toml")
	content := `[mcp_servers.context-mcp]
command = "/usr/bin/qb"
args = ["--repo", "` + repoDir + `"]
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	checks := doctorCodex("test", cfgPath, "")
	for _, c := range checks {
		if !c.Passed {
			t.Fatalf("check %q should pass: %s", c.Name, c.Message)
		}
	}
}

// ---------------------------------------------------------------------------
// Project-scoped config: Claude Code (.mcp.json)
// ---------------------------------------------------------------------------

func TestDoctorClaudeCode_ProjectScope_MCPJson(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	dbDir := filepath.Join(repoDir, ".context-mcp")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "index.db"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create .mcp.json in the project root.
	mcpJSON, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"context-mcp": map[string]any{
				"command": "/usr/bin/qb",
				"args":    []any{"--repo", repoDir},
			},
		},
	})
	if err := os.WriteFile(filepath.Join(repoDir, ".mcp.json"), mcpJSON, 0644); err != nil {
		t.Fatal(err)
	}

	// Use a nonexistent user config so only the project config is found.
	userCfg := filepath.Join(tmp, "nonexistent.json")
	checks := doctorClaudeCode("test", userCfg, repoDir)

	// Overall entry should pass via project scope.
	entryPassed := false
	for _, c := range checks {
		if c.Name == "test/entry" && c.Passed {
			entryPassed = true
			if !strings.Contains(c.Message, "project") {
				t.Fatalf("entry message should mention project scope: %s", c.Message)
			}
		}
	}
	if !entryPassed {
		t.Fatal("entry check should pass via project-scoped .mcp.json")
	}
}

func TestDoctorClaudeCode_ProjectScope_ClaudeSettings(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")

	// Create .claude/settings.json in the project root.
	settingsDir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	settingsJSON, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"context-mcp": map[string]any{
				"command": "/usr/bin/qb",
				"args":    []any{},
			},
		},
	})
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), settingsJSON, 0644); err != nil {
		t.Fatal(err)
	}

	userCfg := filepath.Join(tmp, "nonexistent.json")
	checks := doctorClaudeCode("test", userCfg, repoDir)

	entryPassed := false
	for _, c := range checks {
		if c.Name == "test/entry" && c.Passed {
			entryPassed = true
		}
	}
	if !entryPassed {
		t.Fatal("entry check should pass via project-scoped .claude/settings.json")
	}
}

func TestDoctorClaudeCode_BothScopes(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	dbDir := filepath.Join(repoDir, ".context-mcp")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "index.db"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	// User config.
	userCfg := filepath.Join(tmp, ".claude.json")
	userData, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"context-mcp": map[string]any{
				"command": "/usr/bin/qb",
				"args":    []any{"--repo", repoDir},
			},
		},
	})
	if err := os.WriteFile(userCfg, userData, 0644); err != nil {
		t.Fatal(err)
	}

	// Project config.
	mcpJSON, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"context-mcp": map[string]any{
				"command": "/usr/bin/qb",
				"args":    []any{"--repo", repoDir},
			},
		},
	})
	if err := os.WriteFile(filepath.Join(repoDir, ".mcp.json"), mcpJSON, 0644); err != nil {
		t.Fatal(err)
	}

	checks := doctorClaudeCode("test", userCfg, repoDir)

	entryPassed := false
	for _, c := range checks {
		if c.Name == "test/entry" && c.Passed {
			entryPassed = true
			if !strings.Contains(c.Message, "user") || !strings.Contains(c.Message, "project") {
				t.Fatalf("entry message should mention both scopes: %s", c.Message)
			}
		}
	}
	if !entryPassed {
		t.Fatal("entry check should pass with both scopes")
	}
}

func TestDoctorClaudeCode_LocalScope_GlobalProjects(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	dbDir := filepath.Join(repoDir, ".context-mcp")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "index.db"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	userCfg := filepath.Join(tmp, ".claude.json")
	data, _ := json.Marshal(map[string]any{
		"projects": map[string]any{
			filepath.ToSlash(repoDir): map[string]any{
				"mcpServers": map[string]any{
					"context-mcp": map[string]any{
						"command": "/usr/bin/qb",
						"args":    []any{"--repo", repoDir},
					},
				},
			},
		},
	})
	if err := os.WriteFile(userCfg, data, 0644); err != nil {
		t.Fatal(err)
	}

	checks := doctorClaudeCode("test", userCfg, repoDir)

	var localFound bool
	var entryPassed bool
	for _, c := range checks {
		if c.Name == "test/entry-local" && c.Passed {
			localFound = true
		}
		if c.Name == "test/entry" && c.Passed {
			entryPassed = true
			if !strings.Contains(c.Message, "local") {
				t.Fatalf("entry message should mention local scope: %s", c.Message)
			}
		}
	}
	if !localFound {
		t.Fatal("local-scope Claude config should be detected from projects[...]")
	}
	if !entryPassed {
		t.Fatal("overall entry should pass via local-scope Claude config")
	}
}

func TestDoctorClaudeCode_ProjectScopeNoRepoArg(t *testing.T) {
	// Project config exists but has no --repo arg; repoRoot is provided.
	// The overall entry, repo, and index checks should pass using repoRoot as fallback.
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	dbDir := filepath.Join(repoDir, ".context-mcp")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "index.db"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	mcpJSON, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"context-mcp": map[string]any{
				"command": "/usr/bin/qb",
				"args":    []any{},
			},
		},
	})
	if err := os.WriteFile(filepath.Join(repoDir, ".mcp.json"), mcpJSON, 0644); err != nil {
		t.Fatal(err)
	}

	userCfg := filepath.Join(tmp, "nonexistent.json")
	checks := doctorClaudeCode("test", userCfg, repoDir)

	// The overall entry, repo, and index checks should pass.
	// Individual scope checks (config-user) may fail, which is expected.
	for _, c := range checks {
		switch c.Name {
		case "test/entry", "test/repo", "test/index":
			if !c.Passed {
				t.Fatalf("check %q should pass: %s", c.Name, c.Message)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Project-scoped config: Codex (.codex/config.toml)
// ---------------------------------------------------------------------------

func TestDoctorCodex_ProjectScope(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	dbDir := filepath.Join(repoDir, ".context-mcp")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "index.db"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create project-scoped .codex/config.toml.
	codexDir := filepath.Join(repoDir, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `[mcp_servers.context-mcp]
command = "/usr/bin/qb"
args = ["--repo", "` + repoDir + `"]
`
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// No user config.
	userCfg := filepath.Join(tmp, "nonexistent.toml")
	checks := doctorCodex("test", userCfg, repoDir)

	entryPassed := false
	for _, c := range checks {
		if c.Name == "test/entry" && c.Passed {
			entryPassed = true
			if !strings.Contains(c.Message, "project") {
				t.Fatalf("entry message should mention project scope: %s", c.Message)
			}
		}
	}
	if !entryPassed {
		t.Fatal("entry check should pass via project-scoped .codex/config.toml")
	}
}

func TestDoctorCodex_BothScopes(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	dbDir := filepath.Join(repoDir, ".context-mcp")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "index.db"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	// User config.
	userCfg := filepath.Join(tmp, "config.toml")
	userContent := `[mcp_servers.context-mcp]
command = "/usr/bin/qb"
args = ["--repo", "` + repoDir + `"]
`
	if err := os.WriteFile(userCfg, []byte(userContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Project config.
	codexDir := filepath.Join(repoDir, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(userContent), 0644); err != nil {
		t.Fatal(err)
	}

	checks := doctorCodex("test", userCfg, repoDir)

	entryPassed := false
	for _, c := range checks {
		if c.Name == "test/entry" && c.Passed {
			entryPassed = true
			if !strings.Contains(c.Message, "user") || !strings.Contains(c.Message, "project") {
				t.Fatalf("entry message should mention both scopes: %s", c.Message)
			}
		}
	}
	if !entryPassed {
		t.Fatal("entry check should pass with both scopes")
	}
}

// ---------------------------------------------------------------------------
// checkClaudeCodeJSON / checkCodexTOML unit tests
// ---------------------------------------------------------------------------

func TestCheckClaudeCodeJSON_Found(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "test.json")
	data, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"context-mcp": map[string]any{
				"command": "/usr/bin/qb",
				"args":    []any{"--repo", "/some/path"},
			},
		},
	})
	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	found, repoPath, checks := checkClaudeCodeJSON("test", "user", cfgPath)
	if !found {
		t.Fatal("should find entry")
	}
	if repoPath != "/some/path" {
		t.Fatalf("repoPath = %q, want /some/path", repoPath)
	}
	for _, c := range checks {
		if !c.Passed {
			t.Fatalf("check %q should pass: %s", c.Name, c.Message)
		}
	}
}

func TestCheckClaudeCodeJSON_NotFound(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "test.json")
	data, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{},
	})
	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	found, _, _ := checkClaudeCodeJSON("test", "user", cfgPath)
	if found {
		t.Fatal("should not find entry in empty mcpServers")
	}
}

func TestCheckCodexTOML_Found(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	content := `[mcp_servers.context-mcp]
command = "/usr/bin/qb"
args = ["--repo", "/some/path"]
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	found, repoPath, checks := checkCodexTOML("test", "project", cfgPath)
	if !found {
		t.Fatal("should find section")
	}
	if repoPath != "/some/path" {
		t.Fatalf("repoPath = %q, want /some/path", repoPath)
	}
	for _, c := range checks {
		if !c.Passed {
			t.Fatalf("check %q should pass: %s", c.Name, c.Message)
		}
	}
}

func TestCheckCodexTOML_NotFound(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[other]\nkey = \"val\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	found, _, _ := checkCodexTOML("test", "project", cfgPath)
	if found {
		t.Fatal("should not find section")
	}
}

// ---------------------------------------------------------------------------
// removeTomlSection round-trip: install then uninstall for Codex
// ---------------------------------------------------------------------------

func TestRemoveTomlSection_RoundTrip(t *testing.T) {
	// Simulate installing, then uninstalling the Codex section.
	existing := `[mcp_servers.other]
command = "/bin/other"
args = ["--flag"]
`
	block := buildCodexTOML("/usr/bin/qb", InstallOpts{RepoRoot: "/my/repo"})
	combined := strings.TrimRight(existing, "\n") + "\n" + block

	if !strings.Contains(combined, "[mcp_servers.context-mcp]") {
		t.Fatal("install should add context-mcp section")
	}
	if !strings.Contains(combined, "[mcp_servers.other]") {
		t.Fatal("existing section should survive install")
	}

	// Now remove.
	after := removeTomlSection(combined, "mcp_servers.context-mcp")
	if strings.Contains(after, "context-mcp") {
		t.Fatalf("context-mcp should be removed:\n%s", after)
	}
	if !strings.Contains(after, "[mcp_servers.other]") {
		t.Fatalf("other section should survive uninstall:\n%s", after)
	}
}

// ---------------------------------------------------------------------------
// buildCodexTOML — HTTP transport
// ---------------------------------------------------------------------------

func TestBuildCodexTOML_HTTP(t *testing.T) {
	block := buildCodexTOML("", InstallOpts{
		Transport: "http",
		URL:       "http://localhost:8080/mcp",
	})
	if !strings.Contains(block, "[mcp_servers.context-mcp]") {
		t.Fatal("missing section header")
	}
	if !strings.Contains(block, `url = "http://localhost:8080/mcp"`) {
		t.Fatalf("missing url line, got:\n%s", block)
	}
	if strings.Contains(block, "command") {
		t.Fatal("HTTP transport should not have command")
	}
	if strings.Contains(block, "args") {
		t.Fatal("HTTP transport should not have args")
	}
}

func TestBuildCodexTOML_EnvVars(t *testing.T) {
	block := buildCodexTOML("/usr/local/bin/qb", InstallOpts{
		RepoRoot: "/my/repo",
		EnvVars: map[string]string{
			"CONTEXT_MCP_AUTH": "token123",
			"ANOTHER_VAR":      "value",
		},
	})
	if !strings.Contains(block, `command = "/usr/local/bin/qb"`) {
		t.Fatal("missing command line")
	}
	if !strings.Contains(block, "[mcp_servers.context-mcp.env]") {
		t.Fatalf("missing env section, got:\n%s", block)
	}
	if !strings.Contains(block, `ANOTHER_VAR = "value"`) {
		t.Fatal("missing ANOTHER_VAR env entry")
	}
	if !strings.Contains(block, `CONTEXT_MCP_AUTH = "token123"`) {
		t.Fatal("missing CONTEXT_MCP_AUTH env entry")
	}
}

func TestBuildCodexTOML_HTTPWithEnv(t *testing.T) {
	block := buildCodexTOML("", InstallOpts{
		Transport: "http",
		URL:       "http://localhost:8080/mcp",
		EnvVars:   map[string]string{"TOKEN": "abc"},
	})
	if !strings.Contains(block, `url = "http://localhost:8080/mcp"`) {
		t.Fatal("missing url")
	}
	if !strings.Contains(block, "[mcp_servers.context-mcp.env]") {
		t.Fatal("missing env section")
	}
	if !strings.Contains(block, `TOKEN = "abc"`) {
		t.Fatal("missing TOKEN env")
	}
}

// ---------------------------------------------------------------------------
// buildClaudeCodeJSONEntry — various transports
// ---------------------------------------------------------------------------

func TestBuildClaudeCodeJSONEntry_Stdio(t *testing.T) {
	entry := buildClaudeCodeJSONEntry("/usr/bin/qb", "stdio", InstallOpts{
		RepoRoot: "/my/repo",
		Profile:  "core",
	})
	if entry["command"] != "/usr/bin/qb" {
		t.Fatalf("expected command, got %v", entry)
	}
	if _, ok := entry["url"]; ok {
		t.Fatal("stdio should not have url")
	}
}

func TestBuildClaudeCodeJSONEntry_HTTP(t *testing.T) {
	entry := buildClaudeCodeJSONEntry("", "http", InstallOpts{
		URL: "http://localhost:8080/mcp",
	})
	if entry["url"] != "http://localhost:8080/mcp" {
		t.Fatalf("expected url, got %v", entry)
	}
	if entry["type"] != "http" {
		t.Fatalf("expected type=http, got %v", entry["type"])
	}
	if _, ok := entry["command"]; ok {
		t.Fatal("HTTP should not have command")
	}
}

func TestBuildClaudeCodeJSONEntry_SSE(t *testing.T) {
	entry := buildClaudeCodeJSONEntry("", "sse", InstallOpts{
		URL: "http://localhost:9090/sse",
	})
	if entry["url"] != "http://localhost:9090/sse" {
		t.Fatalf("expected url, got %v", entry)
	}
	if entry["type"] != "sse" {
		t.Fatalf("expected type=sse, got %v", entry["type"])
	}
}

func TestBuildClaudeCodeJSONEntry_WithEnvVars(t *testing.T) {
	entry := buildClaudeCodeJSONEntry("/usr/bin/qb", "stdio", InstallOpts{
		RepoRoot: "/my/repo",
		EnvVars:  map[string]string{"KEY1": "val1", "KEY2": "val2"},
	})
	env, ok := entry["env"].(map[string]any)
	if !ok {
		t.Fatalf("expected env map, got %T", entry["env"])
	}
	if env["KEY1"] != "val1" || env["KEY2"] != "val2" {
		t.Fatalf("unexpected env: %v", env)
	}
}

// ---------------------------------------------------------------------------
// PrintConfig — HTTP transport
// ---------------------------------------------------------------------------

func TestPrintConfig_ClaudeCode_HTTP(t *testing.T) {
	out, err := PrintConfig(PrintConfigOpts{
		Client:    ClientClaudeCode,
		RepoRoot:  "/tmp/myrepo",
		Profile:   "core",
		Transport: "http",
		URL:       "http://localhost:8080/mcp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "--transport http") {
		t.Fatal("output should contain --transport http")
	}
	if !strings.Contains(out, "context-mcp http://localhost:8080/mcp") {
		t.Fatal("output should pass the URL positionally")
	}
	if !strings.Contains(out, `"url"`) {
		t.Fatal("JSON should contain url key")
	}
	if !strings.Contains(out, `"type"`) {
		t.Fatal("JSON should contain type key")
	}
}

func TestBuildClaudeAddArgs_HTTP(t *testing.T) {
	args := buildClaudeAddArgs("", InstallOpts{
		Scope:     "project",
		Transport: "http",
		URL:       "http://localhost:8080/mcp",
	})
	got := strings.Join(args, " ")
	if strings.Contains(got, "--url") {
		t.Fatalf("Claude add args should not use --url: %s", got)
	}
	if !strings.Contains(got, "context-mcp http://localhost:8080/mcp") {
		t.Fatalf("Claude add args should use positional URL: %s", got)
	}
	if !strings.Contains(got, "--scope project") {
		t.Fatalf("Claude add args should preserve scope: %s", got)
	}
}

func TestPrintConfig_ClaudeCode_WithScope(t *testing.T) {
	out, err := PrintConfig(PrintConfigOpts{
		Client:   ClientClaudeCode,
		RepoRoot: "/tmp/myrepo",
		Profile:  "core",
		Scope:    "project",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "--scope project") {
		t.Fatal("output should contain --scope project")
	}
}

func TestPrintConfig_ClaudeCode_WithEnvVars(t *testing.T) {
	out, err := PrintConfig(PrintConfigOpts{
		Client:   ClientClaudeCode,
		RepoRoot: "/tmp/myrepo",
		Profile:  "core",
		EnvVars:  map[string]string{"MY_TOKEN": "abc123"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "--env MY_TOKEN=abc123") {
		t.Fatalf("CLI output should contain --env, got:\n%s", out)
	}
	if !strings.Contains(out, `"MY_TOKEN"`) {
		t.Fatal("JSON should contain env var key")
	}
}

func TestPrintConfig_Codex_HTTP(t *testing.T) {
	out, err := PrintConfig(PrintConfigOpts{
		Client:    ClientCodex,
		RepoRoot:  "/tmp/myrepo",
		Profile:   "core",
		Transport: "http",
		URL:       "http://localhost:8080/mcp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, `url = "http://localhost:8080/mcp"`) {
		t.Fatalf("TOML should contain url, got:\n%s", out)
	}
	if strings.Contains(out, "command =") {
		t.Fatal("HTTP TOML should not have command")
	}
}

func TestPrintConfig_Codex_WithEnvVars(t *testing.T) {
	out, err := PrintConfig(PrintConfigOpts{
		Client:   ClientCodex,
		RepoRoot: "/tmp/myrepo",
		Profile:  "core",
		EnvVars:  map[string]string{"MY_TOKEN": "abc123"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "[mcp_servers.context-mcp.env]") {
		t.Fatalf("TOML should contain env section, got:\n%s", out)
	}
	if !strings.Contains(out, `MY_TOKEN = "abc123"`) {
		t.Fatal("TOML should contain env var")
	}
}

// ---------------------------------------------------------------------------
// removeTomlSection — with env sub-section
// ---------------------------------------------------------------------------

func TestRemoveTomlSection_WithSubSection(t *testing.T) {
	content := `[server.alpha]
host = "a"

[mcp_servers.context-mcp]
command = "/usr/bin/qb"
args = ["--repo", "/tmp"]

[mcp_servers.context-mcp.env]
TOKEN = "abc"

[server.beta]
host = "b"
`
	got := removeTomlSection(content, "mcp_servers.context-mcp")
	if strings.Contains(got, "context-mcp") {
		t.Fatalf("section and sub-sections not removed:\n%s", got)
	}
	if !strings.Contains(got, "[server.alpha]") || !strings.Contains(got, "[server.beta]") {
		t.Fatalf("surrounding sections damaged:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// effectiveScope / effectiveTransport / isRemoteTransport helpers
// ---------------------------------------------------------------------------

func TestEffectiveScope(t *testing.T) {
	if effectiveScope("") != "user" {
		t.Fatal("empty should default to user")
	}
	if effectiveScope("project") != "project" {
		t.Fatal("project should stay project")
	}
}

func TestEffectiveTransport(t *testing.T) {
	if effectiveTransport("") != "stdio" {
		t.Fatal("empty should default to stdio")
	}
	if effectiveTransport("http") != "http" {
		t.Fatal("http should stay http")
	}
}

func TestIsRemoteTransport(t *testing.T) {
	if isRemoteTransport("stdio") {
		t.Fatal("stdio is not remote")
	}
	if !isRemoteTransport("http") {
		t.Fatal("http is remote")
	}
	if !isRemoteTransport("sse") {
		t.Fatal("sse is remote")
	}
}

// ---------------------------------------------------------------------------
// Round-trip with env sub-section
// ---------------------------------------------------------------------------

func TestRemoveTomlSection_RoundTrip_WithEnv(t *testing.T) {
	existing := `[mcp_servers.other]
command = "/bin/other"
args = ["--flag"]
`
	block := buildCodexTOML("/usr/bin/qb", InstallOpts{
		RepoRoot: "/my/repo",
		EnvVars:  map[string]string{"TOKEN": "abc"},
	})
	combined := strings.TrimRight(existing, "\n") + "\n\n" + block

	if !strings.Contains(combined, "[mcp_servers.context-mcp]") {
		t.Fatal("install should add context-mcp section")
	}
	if !strings.Contains(combined, "[mcp_servers.context-mcp.env]") {
		t.Fatal("install should add context-mcp.env section")
	}

	after := removeTomlSection(combined, "mcp_servers.context-mcp")
	if strings.Contains(after, "context-mcp") {
		t.Fatalf("context-mcp and sub-sections should be removed:\n%s", after)
	}
	if !strings.Contains(after, "[mcp_servers.other]") {
		t.Fatalf("other section should survive:\n%s", after)
	}
}
