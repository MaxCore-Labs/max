package agy

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"max/internal/app"
)

// Health inspects whether the agy binary is installed, checks isolated token files, and detects foreign MCP servers.
// Note per S0c: -32000 auth probing is only reliable when running under a dedicated OS user without a shared keyring or in a container.
func (d *Driver) Health(ctx context.Context) app.EngineHealth {
	var h app.EngineHealth
	resolved, err := resolveBinary(d.bin)
	if err != nil {
		return h
	}
	h.Installed = true
	h.Version = detectVersion(ctx, resolved, d.isolatedEnv())
	h.ForeignMCP = scanForeignMCP(d.home)
	h.LoggedIn = hasIsolatedAuthToken(d.home)
	return h
}

func resolveBinary(bin string) (string, error) {
	if filepath.IsAbs(bin) || strings.ContainsAny(bin, `/\`) {
		if _, err := os.Stat(bin); err != nil {
			return "", err
		}
		return bin, nil
	}
	return exec.LookPath(bin)
}

func detectVersion(ctx context.Context, bin string, env []string) string {
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	line := strings.TrimSpace(string(out))
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	if line == "" {
		return "unknown"
	}
	return line
}

func hasIsolatedAuthToken(home string) bool {
	candidates := []string{
		filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"),
		filepath.Join(home, ".config", "agy", "auth.json"),
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return true
		}
	}
	return false
}

func scanForeignMCP(home string) []string {
	candidates := []string{
		filepath.Join(home, ".gemini", "antigravity", "mcp_config.json"),
		filepath.Join(home, ".gemini", "settings.json"),
		filepath.Join(home, ".config", "agy", "mcp.json"),
		filepath.Join(home, "mcp.json"),
	}
	seen := make(map[string]bool)
	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var doc struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		for name := range doc.MCPServers {
			if name != "max" && name != "" {
				seen[name] = true
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
