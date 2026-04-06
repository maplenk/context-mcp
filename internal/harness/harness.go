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
	Client    Client
	Profile   string            // core, extended, full
	RepoRoot  string            // absolute path to repo
	Force     bool              // overwrite existing config
	Scope     string            // "user" (default), "local", "project" — Claude Code only
	Transport string            // "stdio" (default), "http", "sse"
	URL       string            // HTTP/SSE endpoint URL (required when transport != stdio)
	EnvVars   map[string]string // environment variables to pass to MCP server
}

// UninstallOpts contains options for uninstalling.
type UninstallOpts struct {
	Client Client
	Scope  string // optional; if empty, uses claude CLI default
}

// PrintConfigOpts contains options for printing the config snippet.
type PrintConfigOpts struct {
	Client    Client
	Profile   string
	RepoRoot  string
	Scope     string            // "user" (default), "local", "project" — Claude Code only
	Transport string            // "stdio" (default), "http", "sse"
	URL       string            // HTTP/SSE endpoint URL
	EnvVars   map[string]string // environment variables to pass to MCP server
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
	Client   Client // if empty, check all clients
	RepoRoot string // absolute path to repo root (optional; auto-detected if empty)
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

func effectiveProfile(profile string) string {
	if profile == "" {
		return "extended"
	}
	return profile
}

func buildArgs(opts InstallOpts) []string {
	args := []string{"--repo", opts.RepoRoot}
	if profile := effectiveProfile(opts.Profile); profile != "" {
		args = append(args, "--profile", profile)
	}
	return args
}

// effectiveScope returns the scope to use, defaulting to "user" if empty.
func effectiveScope(s string) string {
	if s == "" {
		return "user"
	}
	return s
}

// effectiveTransport returns the transport to use, defaulting to "stdio" if empty.
func effectiveTransport(t string) string {
	if t == "" {
		return "stdio"
	}
	return t
}

// isRemoteTransport returns true for http and sse transports.
func isRemoteTransport(t string) bool {
	return t == "http" || t == "sse"
}

// buildClaudeAddArgs returns the argument list for `claude mcp add`.
// Remote transports pass the MCP endpoint as the positional commandOrUrl arg.
func buildClaudeAddArgs(bin string, opts InstallOpts) []string {
	transport := effectiveTransport(opts.Transport)
	scope := effectiveScope(opts.Scope)

	cmdArgs := []string{
		"mcp", "add",
		"--transport", transport,
		"--scope", scope,
	}
	for _, k := range sortedEnvKeys(opts.EnvVars) {
		cmdArgs = append(cmdArgs, "--env", k+"="+opts.EnvVars[k])
	}
	if isRemoteTransport(transport) {
		cmdArgs = append(cmdArgs, "context-mcp", opts.URL)
		return cmdArgs
	}

	cmdArgs = append(cmdArgs, "context-mcp", "--", bin)
	cmdArgs = append(cmdArgs, buildArgs(opts)...)
	return cmdArgs
}

// sortedEnvKeys returns the keys of a map in sorted order for deterministic output.
func sortedEnvKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// sort
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func installClaudeCode(opts InstallOpts) (string, error) {
	transport := effectiveTransport(opts.Transport)
	scope := effectiveScope(opts.Scope)
	remote := isRemoteTransport(transport)

	var bin string
	if !remote {
		var err error
		bin, err = selfBinary()
		if err != nil {
			return "", err
		}
	}

	claudePath, lookErr := exec.LookPath("claude")

	// Try the CLI path first.
	if lookErr == nil {
		if opts.Force {
			rmCmd := exec.Command(claudePath, "mcp", "remove", "--scope", scope, "context-mcp")
			rmCmd.CombinedOutput() // ignore error (entry might not exist)
		}

		cmd := exec.Command(claudePath, buildClaudeAddArgs(bin, opts)...)
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

	entry := buildClaudeCodeJSONEntry(bin, transport, opts)
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

// buildClaudeCodeJSONEntry builds the JSON entry for the Claude Code config file.
func buildClaudeCodeJSONEntry(bin, transport string, opts InstallOpts) map[string]any {
	entry := make(map[string]any)
	if isRemoteTransport(transport) {
		entry["url"] = opts.URL
		entry["type"] = transport
	} else {
		entry["command"] = bin
		entry["args"] = buildArgs(opts)
	}
	if len(opts.EnvVars) > 0 {
		env := make(map[string]any, len(opts.EnvVars))
		for k, v := range opts.EnvVars {
			env[k] = v
		}
		entry["env"] = env
	}
	return entry
}

func installCodex(opts InstallOpts) (string, error) {
	transport := effectiveTransport(opts.Transport)
	var bin string
	if !isRemoteTransport(transport) {
		var err error
		bin, err = selfBinary()
		if err != nil {
			return "", err
		}
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
	transport := effectiveTransport(opts.Transport)
	var b strings.Builder
	b.WriteString("[mcp_servers.context-mcp]\n")

	if isRemoteTransport(transport) {
		b.WriteString(fmt.Sprintf("url = %q\n", opts.URL))
	} else {
		args := buildArgs(opts)
		b.WriteString(fmt.Sprintf("command = %q\n", bin))
		b.WriteString(fmt.Sprintf("args = %s\n", tomlStringArray(args)))
	}

	if len(opts.EnvVars) > 0 {
		b.WriteString("\n[mcp_servers.context-mcp.env]\n")
		for _, k := range sortedEnvKeys(opts.EnvVars) {
			b.WriteString(fmt.Sprintf("%s = %q\n", k, opts.EnvVars[k]))
		}
	}

	return b.String()
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

// removeTomlSection removes a [header] block and any sub-sections from TOML content.
// For example, removing "mcp_servers.context-mcp" also removes
// "[mcp_servers.context-mcp.env]" and similar child sections.
// Trailing blank lines left by the removal are also cleaned.
func removeTomlSection(content, section string) string {
	header := "[" + section + "]"
	subPrefix := "[" + section + "."
	lines := strings.Split(content, "\n")
	var out []string
	skipping := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == header || strings.HasPrefix(trimmed, subPrefix) {
			skipping = true
			continue
		}
		if skipping {
			// Stop skipping at the next section header that is not a sub-section.
			if strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, subPrefix) {
				skipping = false
				out = append(out, line)
				continue
			}
			// Still inside the removed section (or a child), skip line.
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
	var bin string
	if !isRemoteTransport(effectiveTransport(opts.Transport)) {
		var err error
		bin, err = selfBinary()
		if err != nil {
			return "", err
		}
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
	transport := effectiveTransport(opts.Transport)
	scope := effectiveScope(opts.Scope)

	installOpts := InstallOpts{
		RepoRoot:  opts.RepoRoot,
		Profile:   opts.Profile,
		Scope:     opts.Scope,
		Transport: opts.Transport,
		URL:       opts.URL,
		EnvVars:   opts.EnvVars,
	}

	// Build the CLI command.
	cmdParts := buildClaudeAddArgs(bin, installOpts)

	// Build the JSON snippet.
	entry := buildClaudeCodeJSONEntry(bin, transport, installOpts)
	snippet := map[string]any{
		"mcpServers": map[string]any{
			"context-mcp": entry,
		},
	}
	jsonBytes, _ := json.MarshalIndent(snippet, "", "  ")

	var b strings.Builder
	b.WriteString("# Claude Code — CLI command:\n")
	b.WriteString(strings.Join(cmdParts, " "))
	b.WriteString("\n\n# Claude Code — JSON snippet")
	if scope == "project" {
		b.WriteString(" for .mcp.json")
	} else {
		b.WriteString(" for ~/.claude.json")
	}
	b.WriteString(":\n")
	b.Write(jsonBytes)
	b.WriteString("\n")
	return b.String()
}

func printConfigCodex(bin string, opts PrintConfigOpts) string {
	block := buildCodexTOML(bin, InstallOpts{
		RepoRoot:  opts.RepoRoot,
		Profile:   opts.Profile,
		Transport: opts.Transport,
		URL:       opts.URL,
		EnvVars:   opts.EnvVars,
	})
	var b strings.Builder
	b.WriteString("# Codex — TOML snippet for ~/.codex/config.toml:\n")
	b.WriteString(block)
	return b.String()
}

// ---------------------------------------------------------------------------
// Doctor
// ---------------------------------------------------------------------------

// detectRepoRoot attempts to find the repository root via git.
func detectRepoRoot() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func normalizedProjectKey(repoRoot string) string {
	return filepath.ToSlash(filepath.Clean(repoRoot))
}

// Doctor runs diagnostic checks and returns results.
func Doctor(opts DoctorOpts) ([]DoctorCheck, error) {
	clients := []Client{ClientClaudeCode, ClientCodex}
	if opts.Client != "" {
		clients = []Client{opts.Client}
	}

	// Resolve repo root: explicit > auto-detect.
	repoRoot := opts.RepoRoot
	if repoRoot == "" {
		repoRoot = detectRepoRoot()
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
		cc, err := doctorClient(c, repoRoot)
		if err != nil {
			return nil, err
		}
		checks = append(checks, cc...)
	}

	return checks, nil
}

func doctorClient(c Client, repoRoot string) ([]DoctorCheck, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}

	var checks []DoctorCheck
	prefix := string(c)

	switch c {
	case ClientClaudeCode:
		userCfg := filepath.Join(home, ".claude.json")
		checks = append(checks, doctorClaudeCode(prefix, userCfg, repoRoot)...)
	case ClientCodex:
		userCfg := filepath.Join(home, ".codex", "config.toml")
		checks = append(checks, doctorCodex(prefix, userCfg, repoRoot)...)
	}

	return checks, nil
}

// checkClaudeCodeJSON checks a single JSON config file for a context-mcp entry.
// Returns (found bool, repoPath string extracted from args, diagnostic checks).
func checkClaudeCodeJSON(prefix, scope, cfgPath string) (bool, string, []DoctorCheck) {
	var checks []DoctorCheck

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/config-" + scope,
			Passed:  false,
			Message: fmt.Sprintf("Config not found (%s): %s", scope, cfgPath),
		})
		return false, "", checks
	}
	checks = append(checks, DoctorCheck{
		Name:    prefix + "/config-" + scope,
		Passed:  true,
		Message: fmt.Sprintf("Config exists (%s): %s", scope, cfgPath),
	})

	root := make(map[string]any)
	if err := json.Unmarshal(data, &root); err != nil {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/entry-" + scope,
			Passed:  false,
			Message: fmt.Sprintf("Cannot parse %s: %v", cfgPath, err),
			Fix:     "Fix JSON syntax in " + cfgPath,
		})
		return false, "", checks
	}

	servers, _ := root["mcpServers"].(map[string]any)
	entry, _ := servers["context-mcp"].(map[string]any)
	if entry == nil {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/entry-" + scope,
			Passed:  false,
			Message: fmt.Sprintf("context-mcp not found in mcpServers (%s)", scope),
		})
		return false, "", checks
	}

	repoPath := extractRepoArg(entry)
	checks = append(checks, DoctorCheck{
		Name:    prefix + "/entry-" + scope,
		Passed:  true,
		Message: fmt.Sprintf("context-mcp entry found (%s): %s", scope, cfgPath),
	})
	return true, repoPath, checks
}

// checkClaudeCodeLocalProject checks Claude's global projects[...] config for a
// local-scope MCP entry for the requested repository.
func checkClaudeCodeLocalProject(prefix, cfgPath, repoRoot string) (bool, string, []DoctorCheck) {
	if repoRoot == "" {
		return false, "", nil
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return false, "", nil
	}

	root := make(map[string]any)
	if err := json.Unmarshal(data, &root); err != nil {
		return false, "", nil
	}

	projects, _ := root["projects"].(map[string]any)
	if projects == nil {
		return false, "", nil
	}

	candidates := []string{repoRoot, filepath.Clean(repoRoot), normalizedProjectKey(repoRoot)}
	for _, key := range candidates {
		project, _ := projects[key].(map[string]any)
		if project == nil {
			continue
		}
		servers, _ := project["mcpServers"].(map[string]any)
		entry, _ := servers["context-mcp"].(map[string]any)
		if entry == nil {
			continue
		}
		repoPath := extractRepoArg(entry)
		checks := []DoctorCheck{{
			Name:    prefix + "/entry-local",
			Passed:  true,
			Message: fmt.Sprintf("context-mcp entry found (local): %s [%s]", cfgPath, key),
		}}
		return true, repoPath, checks
	}

	return false, "", nil
}

func doctorClaudeCode(prefix, userCfgPath, repoRoot string) []DoctorCheck {
	var checks []DoctorCheck

	// Check user-scope config.
	userFound, userRepoPath, userChecks := checkClaudeCodeJSON(prefix, "user", userCfgPath)
	checks = append(checks, userChecks...)

	// Check local-scope config stored under projects[...] in the global file.
	localFound, localRepoPath, localChecks := checkClaudeCodeLocalProject(prefix, userCfgPath, repoRoot)
	checks = append(checks, localChecks...)

	// Check project-scope configs if repoRoot is known.
	var projectFound bool
	var projectRepoPath string
	if repoRoot != "" {
		projectConfigs := []string{
			filepath.Join(repoRoot, ".mcp.json"),
			filepath.Join(repoRoot, ".claude", "settings.json"),
			filepath.Join(repoRoot, ".claude", "settings.local.json"),
		}
		for _, pc := range projectConfigs {
			found, rp, pcChecks := checkClaudeCodeJSON(prefix, "project", pc)
			checks = append(checks, pcChecks...)
			if found {
				projectFound = true
				if rp != "" {
					projectRepoPath = rp
				}
				break // one project config is enough
			}
		}
	}

	// Overall entry verdict.
	if !userFound && !localFound && !projectFound {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/entry",
			Passed:  false,
			Message: "context-mcp not found in any config scope (user, local, or project)",
			Fix:     "Run: context-mcp install --client claude-code --repo <path>",
		})
		return checks
	}

	var scopes []string
	if userFound {
		scopes = append(scopes, "user")
	}
	if localFound {
		scopes = append(scopes, "local")
	}
	if projectFound {
		scopes = append(scopes, "project")
	}
	checks = append(checks, DoctorCheck{
		Name:    prefix + "/entry",
		Passed:  true,
		Message: fmt.Sprintf("context-mcp configured (scopes: %s)", strings.Join(scopes, ", ")),
	})

	// Determine best repo path: prefer user config, fall back to project config, fall back to repoRoot.
	repoPath := userRepoPath
	if repoPath == "" {
		repoPath = localRepoPath
	}
	if repoPath == "" {
		repoPath = projectRepoPath
	}
	if repoPath == "" {
		repoPath = repoRoot
	}
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

// checkCodexTOML checks a single TOML config file for a context-mcp section.
// Returns (found bool, repoPath string extracted from args, diagnostic checks).
func checkCodexTOML(prefix, scope, cfgPath string) (bool, string, []DoctorCheck) {
	var checks []DoctorCheck

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/config-" + scope,
			Passed:  false,
			Message: fmt.Sprintf("Config not found (%s): %s", scope, cfgPath),
		})
		return false, "", checks
	}
	checks = append(checks, DoctorCheck{
		Name:    prefix + "/config-" + scope,
		Passed:  true,
		Message: fmt.Sprintf("Config exists (%s): %s", scope, cfgPath),
	})

	content := string(data)
	if !strings.Contains(content, "[mcp_servers.context-mcp]") {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/entry-" + scope,
			Passed:  false,
			Message: fmt.Sprintf("context-mcp section not found (%s)", scope),
		})
		return false, "", checks
	}

	repoPath := extractRepoArgFromTOML(content)
	checks = append(checks, DoctorCheck{
		Name:    prefix + "/entry-" + scope,
		Passed:  true,
		Message: fmt.Sprintf("context-mcp section found (%s): %s", scope, cfgPath),
	})
	return true, repoPath, checks
}

func doctorCodex(prefix, userCfgPath, repoRoot string) []DoctorCheck {
	var checks []DoctorCheck

	// Check user-scope config.
	userFound, userRepoPath, userChecks := checkCodexTOML(prefix, "user", userCfgPath)
	checks = append(checks, userChecks...)

	// Check project-scope config if repoRoot is known.
	var projectFound bool
	var projectRepoPath string
	if repoRoot != "" {
		projectCfg := filepath.Join(repoRoot, ".codex", "config.toml")
		found, rp, pcChecks := checkCodexTOML(prefix, "project", projectCfg)
		checks = append(checks, pcChecks...)
		if found {
			projectFound = true
			projectRepoPath = rp
		}
	}

	// Overall entry verdict.
	if !userFound && !projectFound {
		checks = append(checks, DoctorCheck{
			Name:    prefix + "/entry",
			Passed:  false,
			Message: "context-mcp not found in any config scope (user or project)",
			Fix:     "Run: context-mcp install --client codex --repo <path>",
		})
		return checks
	}

	var scopes []string
	if userFound {
		scopes = append(scopes, "user")
	}
	if projectFound {
		scopes = append(scopes, "project")
	}
	checks = append(checks, DoctorCheck{
		Name:    prefix + "/entry",
		Passed:  true,
		Message: fmt.Sprintf("context-mcp configured (scopes: %s)", strings.Join(scopes, ", ")),
	})

	// Determine best repo path.
	repoPath := userRepoPath
	if repoPath == "" {
		repoPath = projectRepoPath
	}
	if repoPath == "" {
		repoPath = repoRoot
	}
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
			Passed:  true,
			Message: "Repo path not specified in config (skipping repo/index checks)",
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
