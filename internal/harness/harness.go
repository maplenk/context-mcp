package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// atomicWriteFile writes data to path via a temp file + rename to prevent
// partial writes from corrupting the config if the process is killed mid-write.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Client represents a supported agent client.
type Client string

const (
	ClientClaudeCode Client = "claude-code"
	ClientCodex      Client = "codex"
)

// InstallOpts contains options for installing context-mcp into a client.
type InstallOpts struct {
	Client   Client
	Profile  string // core, extended, full
	RepoRoot string // absolute path to repo
	Force    bool   // overwrite existing config
}

// UninstallOpts contains options for uninstalling.
type UninstallOpts struct {
	Client Client
	Scope  string // optional; if empty, uses claude CLI default
}

// PrintConfigOpts contains options for printing the config snippet.
type PrintConfigOpts struct {
	Client   Client
	Profile  string
	RepoRoot string
}

// DoctorCheck represents a single diagnostic check result.
type DoctorCheck struct {
	Name    string
	Passed  bool
	Message string
	Fix     string // corrective step if failed
}

// DoctorOpts contains options for the doctor command.
type DoctorOpts struct {
	Client Client // if empty, check all clients
}

// ---------------------------------------------------------------------------
// Install
// ---------------------------------------------------------------------------

// Install configures context-mcp as an MCP server for the given client.
// Returns a human-readable status message.
func Install(opts InstallOpts) (string, error) {
	switch opts.Client {
	case ClientClaudeCode:
		return installClaudeCode(opts)
	case ClientCodex:
		return installCodex(opts)
	default:
		return "", fmt.Errorf("unsupported client: %s", opts.Client)
	}
}

func selfBinary() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine own binary path: %w", err)
	}
	return filepath.EvalSymlinks(p)
}

func buildArgs(opts InstallOpts) []string {
	args := []string{"--repo", opts.RepoRoot}
	if opts.Profile != "" {
		args = append(args, "--profile", opts.Profile)
	}
	return args
}

func installClaudeCode(opts InstallOpts) (string, error) {
	bin, err := selfBinary()
	if err != nil {
		return "", err
	}

	claudePath, lookErr := exec.LookPath("claude")

	// Try the CLI path first.
	if lookErr == nil {
		cmdArgs := []string{
			"mcp", "add",
			"--transport", "stdio",
			"--scope", "user",
		}
		if opts.Force {
			cmdArgs = append(cmdArgs, "--force")
		}
		cmdArgs = append(cmdArgs, "context-mcp", "--", bin)
		cmdArgs = append(cmdArgs, buildArgs(opts)...)

		cmd := exec.Command(claudePath, cmdArgs...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("claude mcp add failed: %w\n%s", err, string(out))
		}
		return fmt.Sprintf("Installed via claude CLI.\n%s", strings.TrimSpace(string(out))), nil
	}

	// Fall back to editing ~/.claude.json directly.
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	cfgPath := filepath.Join(home, ".claude.json")

	root := make(map[string]any)
	data, err := os.ReadFile(cfgPath)
	if err == nil {
		if err := json.Unmarshal(data, &root); err != nil {
			return "", fmt.Errorf("failed to parse %s: %w", cfgPath, err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("cannot read %s: %w", cfgPath, err)
	}

	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		servers = make(map[string]any)
	}
	if _, exists := servers["context-mcp"]; exists && !opts.Force {
		return "", fmt.Errorf("context-mcp already configured in %s (use --force to overwrite)", cfgPath)
	}

	entry := map[string]any{
		"command": bin,
		"args":    buildArgs(opts),
	}
	servers["context-mcp"] = entry
	root["mcpServers"] = servers

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := atomicWriteFile(cfgPath, append(out, '\n'), 0600); err != nil {
		return "", fmt.Errorf("failed to write %s: %w", cfgPath, err)
	}
	return fmt.Sprintf("Installed by writing to %s", cfgPath), nil
}

func installCodex(opts InstallOpts) (string, error) {
	bin, err := selfBinary()
	if err != nil {
		return "", err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	cfgPath := filepath.Join(home, ".codex", "config.toml")

	// Read existing file (or start empty).
	var content string
	data, err := os.ReadFile(cfgPath)
	if err == nil {
		content = string(data)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("cannot read %s: %w", cfgPath, err)
	}

	if strings.Contains(content, "[mcp_servers.context-mcp]") {
		if !opts.Force {
			return "", fmt.Errorf("context-mcp already configured in %s (use --force to overwrite)", cfgPath)
		}
		// Remove the existing block before re-adding.
		content = removeTomlSection(content, "mcp_servers.context-mcp")
	}

	block := buildCodexTOML(bin, opts)

	// Ensure there's a blank line separator when appending to existing content.
	if content != "" {
		content = strings.TrimRight(content, "\n") + "\n\n"
	}
	content += block

	// Ensure parent dir exists.
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return "", fmt.Errorf("cannot create config directory: %w", err)
	}
	if err := atomicWriteFile(cfgPath, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("failed to write %s: %w", cfgPath, err)
	}
	return fmt.Sprintf("Installed by writing to %s", cfgPath), nil
}

func buildCodexTOML(bin string, opts InstallOpts) string {
	args := buildArgs(opts)
	return fmt.Sprintf("[mcp_servers.context-mcp]\ncommand = %q\nargs = %s\n", bin, tomlStringArray(args))
}

// tomlStringArray formats a Go string slice as a TOML inline array.
func tomlStringArray(ss []string) string {
	quoted := make([]string, len(ss))
	for i, s := range ss {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// ---------------------------------------------------------------------------
// Uninstall
// ---------------------------------------------------------------------------

// Uninstall removes context-mcp configuration from the given client.
func Uninstall(opts UninstallOpts) (string, error) {
	switch opts.Client {
	case ClientClaudeCode:
		return uninstallClaudeCode(opts)
	case ClientCodex:
		return uninstallCodex()
	default:
		return "", fmt.Errorf("unsupported client: %s", opts.Client)
	}
}

func uninstallClaudeCode(opts UninstallOpts) (string, error) {
	claudePath, lookErr := exec.LookPath("claude")
	if lookErr == nil {
		cmdArgs := []string{"mcp", "remove"}
		if opts.Scope != "" {
			cmdArgs = append(cmdArgs, "--scope", opts.Scope)
		}
		cmdArgs = append(cmdArgs, "context-mcp")
		cmd := exec.Command(claudePath, cmdArgs...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("claude mcp remove failed: %w\n%s", err, string(out))
		}
		return fmt.Sprintf("Uninstalled via claude CLI.\n%s", strings.TrimSpace(string(out))), nil
	}

	// Fall back to editing ~/.claude.json.
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	cfgPath := filepath.Join(home, ".claude.json")

	data, err := os.ReadFile(cfgPath)
	if os.IsNotExist(err) {
		return "Nothing to uninstall: ~/.claude.json not found.", nil
	}
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", cfgPath, err)
	}

	root := make(map[string]any)
	if err := json.Unmarshal(data, &root); err != nil {
		return "", fmt.Errorf("failed to parse %s: %w", cfgPath, err)
	}

	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		return "Nothing to uninstall: no mcpServers in ~/.claude.json.", nil
	}
	if _, exists := servers["context-mcp"]; !exists {
		return "Nothing to uninstall: context-mcp not found in ~/.claude.json.", nil
	}
	delete(servers, "context-mcp")
	if len(servers) == 0 {
		delete(root, "mcpServers")
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := atomicWriteFile(cfgPath, append(out, '\n'), 0600); err != nil {
		return "", fmt.Errorf("failed to write %s: %w", cfgPath, err)
	}
	return fmt.Sprintf("Uninstalled from %s", cfgPath), nil
}

func uninstallCodex() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	cfgPath := filepath.Join(home, ".codex", "config.toml")

	data, err := os.ReadFile(cfgPath)
	if os.IsNotExist(err) {
		return "Nothing to uninstall: ~/.codex/config.toml not found.", nil
	}
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", cfgPath, err)
	}

	content := string(data)
	if !strings.Contains(content, "[mcp_servers.context-mcp]") {
		return "Nothing to uninstall: context-mcp not found in ~/.codex/config.toml.", nil
	}

	content = removeTomlSection(content, "mcp_servers.context-mcp")

	if err := atomicWriteFile(cfgPath, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("failed to write %s: %w", cfgPath, err)
	}
	return fmt.Sprintf("Uninstalled from %s", cfgPath), nil
}

// removeTomlSection removes a [header] block from TOML content.
// It removes lines from the matching header up to (but not including) the next
// section header or EOF. Trailing blank lines left by the removal are also cleaned.
func removeTomlSection(content, section string) string {
	header := "[" + section + "]"
	lines := strings.Split(content, "\n")
	var out []string
	skipping := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == header {
			skipping = true
			continue
		}
		if skipping {
			// Stop skipping at the next section header (including [[array_of_tables]]).
			if strings.HasPrefix(trimmed, "[") {
				skipping = false
				out = append(out, line)
			}
			// else: still inside the removed section, skip line.
			continue
		}
		out = append(out, line)
	}

	// Remove trailing blank lines.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}

	result := strings.Join(out, "\n")
	if result != "" {
		result += "\n"
	}
	return result
}

// ---------------------------------------------------------------------------
// PrintConfig
// ---------------------------------------------------------------------------

// PrintConfig returns the config snippet for manual installation.
func PrintConfig(opts PrintConfigOpts) (string, error) {
	bin, err := selfBinary()
	if err != nil {
		return "", err
	}

	switch opts.Client {
	case ClientClaudeCode:
		return printConfigClaudeCode(bin, opts), nil
	case ClientCodex:
		return printConfigCodex(bin, opts), nil
	default:
		return "", fmt.Errorf("unsupported client: %s", opts.Client)
	}
}

func printConfigClaudeCode(bin string, opts PrintConfigOpts) string {
	args := buildArgs(InstallOpts{RepoRoot: opts.RepoRoot, Profile: opts.Profile})

	// Build the CLI command.
	cmdParts := []string{
		"claude", "mcp", "add",
		"--transport", "stdio",
		"--scope", "user",
		"context-mcp", "--", bin,
	}
	cmdParts = append(cmdParts, args...)

	// Build the JSON snippet.
	entry := map[string]any{
		"command": bin,
		"args":    args,
	}
	snippet := map[string]any{
		"mcpServers": map[string]any{
			"context-mcp": entry,
		},
	}
	jsonBytes, _ := json.MarshalIndent(snippet, "", "  ")

	var b strings.Builder
	b.WriteString("# Claude Code — CLI command:\n")
	b.WriteString(strings.Join(cmdParts, " "))
	b.WriteString("\n\n# Claude Code — JSON snippet for ~/.claude.json:\n")
	b.Write(jsonBytes)
	b.WriteString("\n")
	return b.String()
}

func printConfigCodex(bin string, opts PrintConfigOpts) string {
	block := buildCodexTOML(bin, InstallOpts{RepoRoot: opts.RepoRoot, Profile: opts.Profile})
	var b strings.Builder
	b.WriteString("# Codex — TOML snippet for ~/.codex/config.toml:")
	b.WriteString(block)
	return b.String()
}

// ---------------------------------------------------------------------------
// Doctor
// ---------------------------------------------------------------------------

// Doctor runs diagnostic checks and returns results.
func Doctor(opts DoctorOpts) ([]DoctorCheck, error) {
	clients := []Client{ClientClaudeCode, ClientCodex}
	if opts.Client != "" {
		clients = []Client{opts.Client}
	}

	var checks []DoctorCheck

	// 1. Binary check (common to all clients).
	bin, binErr := selfBinary()
	if binErr != nil {
		checks = append(checks, DoctorCheck{
			Name:    "binary",
			Passed:  false,
			Message: fmt.Sprintf("Cannot resolve binary: %v", binErr),
			Fix:     "Ensure context-mcp is installed and accessible on PATH.",
		})
	} else {
		info, err := os.Stat(bin)
		if err != nil {
			checks = append(checks, DoctorCheck{
				Name:    "binary",
				Passed:  false,
				Message: fmt.Sprintf("Binary not found at %s", bin),
				Fix:     "Reinstall context-mcp.",
			})
		} else if info.Mode()&0111 == 0 {
			checks = append(checks, DoctorCheck{
				Name:    "binary",
				Passed:  false,
				Message: fmt.Sprintf("Binary at %s is not executable", bin),
				Fix:     fmt.Sprintf("chmod +x %s", bin),
			})
		} else {
			checks = append(checks, DoctorCheck{
				Name:    "binary",
				Passed:  true,
				Message: fmt.Sprintf("Binary OK: %s", bin),
			})
		}
	}

	// Per-client checks.
	for _, c := range clients {
		cc, err := doctorClient(c)
		if err != nil {
			return nil, err
		}
		checks = append(checks, cc...)
	}

	return checks, nil
}

func doctorClient(c Client) ([]DoctorCheck, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}

	var checks []DoctorCheck
	prefix := string(c)

	switch c {
	case ClientClaudeCode:
		cfgPath := filepath.Join(home, ".claude.json")
		checks = append(checks, doctorClaudeCode(prefix, cfgPath)...)
	case ClientCodex:
		cfgPath := filepath.Join(home, ".codex", "config.toml")
		checks = append(checks, doctorCodex(prefix, cfgPath)...)
	}

	return checks, nil
}

func doctorClaudeCode(prefix, cfgPath string) []DoctorCheck {
	var checks []DoctorCheck

	// Config file exists?
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/config",
			Passed:  false,
			Message: fmt.Sprintf("Config file not found: %s", cfgPath),
			Fix:     "Run: context-mcp install --client claude-code --repo <path>",
		})
		return checks
	}
	checks = append(checks, DoctorCheck{
		Name:    prefix + "/config",
		Passed:  true,
		Message: fmt.Sprintf("Config file exists: %s", cfgPath),
	})

	// Parse and check entry.
	root := make(map[string]any)
	if err := json.Unmarshal(data, &root); err != nil {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/entry",
			Passed:  false,
			Message: fmt.Sprintf("Cannot parse %s: %v", cfgPath, err),
			Fix:     "Fix JSON syntax in " + cfgPath,
		})
		return checks
	}

	servers, _ := root["mcpServers"].(map[string]any)
	entry, _ := servers["context-mcp"].(map[string]any)
	if entry == nil {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/entry",
			Passed:  false,
			Message: "context-mcp not found in mcpServers",
			Fix:     "Run: context-mcp install --client claude-code --repo <path>",
		})
		return checks
	}
	checks = append(checks, DoctorCheck{
		Name:    prefix + "/entry",
		Passed:  true,
		Message: "context-mcp entry found in mcpServers",
	})

	// Extract repo path from args.
	repoPath := extractRepoArg(entry)
	checks = append(checks, doctorRepoPaths(prefix, repoPath)...)

	// Check project-level config files for context-mcp entry.
	if repoPath != "" {
		projectConfigs := []string{
			filepath.Join(repoPath, ".mcp.json"),
			filepath.Join(repoPath, ".claude", "settings.json"),
			filepath.Join(repoPath, ".claude", "settings.local.json"),
		}
		for _, pc := range projectConfigs {
			pcData, err := os.ReadFile(pc)
			if err != nil {
				continue // file doesn't exist, skip
			}
			var pcRoot map[string]any
			if err := json.Unmarshal(pcData, &pcRoot); err != nil {
				continue
			}
			pcServers, _ := pcRoot["mcpServers"].(map[string]any)
			if _, ok := pcServers["context-mcp"]; ok {
				checks = append(checks, DoctorCheck{
					Name:    prefix + "/project-config",
					Passed:  true,
					Message: fmt.Sprintf("context-mcp also configured in project file: %s", pc),
				})
			}
		}
	}

	return checks
}

func doctorCodex(prefix, cfgPath string) []DoctorCheck {
	var checks []DoctorCheck

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/config",
			Passed:  false,
			Message: fmt.Sprintf("Config file not found: %s", cfgPath),
			Fix:     "Run: context-mcp install --client codex --repo <path>",
		})
		return checks
	}
	checks = append(checks, DoctorCheck{
		Name:    prefix + "/config",
		Passed:  true,
		Message: fmt.Sprintf("Config file exists: %s", cfgPath),
	})

	content := string(data)
	if !strings.Contains(content, "[mcp_servers.context-mcp]") {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/entry",
			Passed:  false,
			Message: "context-mcp section not found in config",
			Fix:     "Run: context-mcp install --client codex --repo <path>",
		})
		return checks
	}
	checks = append(checks, DoctorCheck{
		Name:    prefix + "/entry",
		Passed:  true,
		Message: "context-mcp section found in config",
	})

	// Extract repo path from TOML args line.
	repoPath := extractRepoArgFromTOML(content)
	checks = append(checks, doctorRepoPaths(prefix, repoPath)...)

	return checks
}

// extractRepoArg finds --repo <value> in the args array of a JSON entry.
func extractRepoArg(entry map[string]any) string {
	rawArgs, _ := entry["args"].([]any)
	for i, a := range rawArgs {
		s, _ := a.(string)
		if s == "--repo" && i+1 < len(rawArgs) {
			next, _ := rawArgs[i+1].(string)
			return next
		}
	}
	return ""
}

// extractRepoArgFromTOML finds --repo value in the args line of a TOML section.
func extractRepoArgFromTOML(content string) string {
	lines := strings.Split(content, "\n")
	inSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[mcp_servers.context-mcp]" {
			inSection = true
			continue
		}
		if inSection {
			if strings.HasPrefix(trimmed, "[") {
				break
			}
			if strings.HasPrefix(trimmed, "args") {
				// Parse the simple inline array.
				idx := strings.Index(trimmed, "=")
				if idx < 0 {
					continue
				}
				arrStr := strings.TrimSpace(trimmed[idx+1:])
				parts := parseSimpleTOMLArray(arrStr)
				for i, p := range parts {
					if p == "--repo" && i+1 < len(parts) {
						return parts[i+1]
					}
				}
			}
		}
	}
	return ""
}

// parseSimpleTOMLArray parses a TOML inline string array like ["a", "b"].
// TOML inline arrays of strings share JSON array syntax, so json.Unmarshal
// handles escaped quotes and commas in values correctly.
func parseSimpleTOMLArray(s string) []string {
	s = strings.TrimSpace(s)
	var result []string
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil
	}
	return result
}

func doctorRepoPaths(prefix, repoPath string) []DoctorCheck {
	var checks []DoctorCheck

	if repoPath == "" {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/repo",
			Passed:  false,
			Message: "Cannot determine repo path from config",
			Fix:     "Re-run install with --repo <path>",
		})
		return checks
	}

	info, err := os.Stat(repoPath)
	if err != nil || !info.IsDir() {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/repo",
			Passed:  false,
			Message: fmt.Sprintf("Repo directory not found: %s", repoPath),
			Fix:     "Verify --repo path exists and is a directory.",
		})
		return checks
	}
	checks = append(checks, DoctorCheck{
		Name:    prefix + "/repo",
		Passed:  true,
		Message: fmt.Sprintf("Repo directory OK: %s", repoPath),
	})

	// Index check.
	dbPath := filepath.Join(repoPath, ".context-mcp", "index.db")
	if _, err := os.Stat(dbPath); err != nil {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/index",
			Passed:  false,
			Message: fmt.Sprintf("Index database not found: %s", dbPath),
			Fix:     fmt.Sprintf("Run context-mcp once to create the index: context-mcp --repo %s", repoPath),
		})
	} else {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/index",
			Passed:  true,
			Message: fmt.Sprintf("Index database OK: %s", dbPath),
		})
	}

	return checks
}
