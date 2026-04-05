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
	if checks[0].Passed {
		t.Fatal("repo check should fail for empty path")
	}
	if !strings.Contains(checks[0].Message, "Cannot determine repo path") {
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
	checks := doctorClaudeCode("test", cfgPath)
	if len(checks) == 0 {
		t.Fatal("expected checks")
	}
	if checks[0].Passed {
		t.Fatal("config check should fail")
	}
}

func TestDoctorClaudeCode_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, ".claude.json")
	if err := os.WriteFile(cfgPath, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	checks := doctorClaudeCode("test", cfgPath)
	// Should have config (pass) + entry (fail due to parse error).
	found := false
	for _, c := range checks {
		if strings.HasSuffix(c.Name, "/entry") && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatal("expected entry check to fail on invalid JSON")
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
	checks := doctorClaudeCode("test", cfgPath)
	found := false
	for _, c := range checks {
		if strings.HasSuffix(c.Name, "/entry") && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatal("expected entry check to fail when context-mcp not present")
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

	checks := doctorClaudeCode("test", cfgPath)
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
	checks := doctorCodex("test", cfgPath)
	if len(checks) == 0 {
		t.Fatal("expected checks")
	}
	if checks[0].Passed {
		t.Fatal("config check should fail")
	}
}

func TestDoctorCodex_NoQBSection(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[other]\nkey = \"val\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	checks := doctorCodex("test", cfgPath)
	found := false
	for _, c := range checks {
		if strings.HasSuffix(c.Name, "/entry") && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Fatal("expected entry check to fail when context-mcp section missing")
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

	checks := doctorCodex("test", cfgPath)
	for _, c := range checks {
		if !c.Passed {
			t.Fatalf("check %q should pass: %s", c.Name, c.Message)
		}
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
