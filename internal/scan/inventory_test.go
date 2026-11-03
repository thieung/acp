package scan

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/thieung/acp/internal/core"
	"github.com/thieung/acp/internal/testkit"
)

func TestInventoryListsHooksAndMCPServersWithoutSecrets(t *testing.T) {
	f := testkit.New(t)
	f.Write(filepath.Join(f.Home, ".claude/settings.json"), `{"hooks": {
  "SessionStart": [{"matcher": "startup", "hooks": [
    {"type": "command", "command": "python3 /old/agent-control-plane/scripts/acp link --hook"},
    {"type": "command", "command": "notify --token=abc123secret"}]}],
  "Stop": [{"hooks": [{"type": "command", "command": "say done"}]}]}}`)
	f.Write(filepath.Join(f.Home, ".claude.json"), `{"mcpServers": {
  "gone": {"type": "stdio", "command": "/opt/missing/server", "args": ["--key", "secret-x"], "env": {"GITHUB_TOKEN": "ghp_x"}},
  "remote": {"type": "http", "url": "https://mcp.example.com", "headers": {"Authorization": "Bearer secret-x"}}},
  "projects": {"/work": {"mcpServers": {"local": {"command": "sh"}}}}}`)
	f.Write(filepath.Join(f.Home, ".codex/config.toml"), "[mcp_servers.tools]\ncommand = \"/opt/missing/codex-tool\"\nenv = { \"API_KEY\" = \"secret-x\" }\n")

	inv := Inventory()
	out, _ := core.MarshalJSON(inv)
	text := string(out)
	for _, want := range []string{`"event": "SessionStart"`, `"kind": "legacy"`, `"command": "say done"`,
		`"name": "gone"`, `"command": "/opt/missing/server"`, `"name": "remote"`, `"remote": true`,
		`"name": "local"`, `"name": "tools"`, `"exists": false`, `"label": "Claude Code"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("%s missing from:\n%s", want, text)
		}
	}
	for _, secret := range []string{"secret-x", "ghp_x", "mcp.example.com", "abc123secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("inventory contains %q:\n%s", secret, text)
		}
	}
}

func TestMCPServersReadArgsAndScopeForComparisonOnly(t *testing.T) {
	f := testkit.New(t)
	f.Write(filepath.Join(f.Home, ".claude.json"), `{"mcpServers": {
  "acp": {"type": "stdio", "command": "/opt/acp", "args": ["mcp"], "env": {}}},
  "projects": {"/work": {"mcpServers": {"acp": {"command": "/old/acp", "args": ["mcp", "--x"]}}}}}`)
	f.Write(filepath.Join(f.Home, ".codex/config.toml"), `[mcp_servers.acp]
command = "/opt/acp"
args = ["mcp", 'two', "a \"q\" ]"]

[mcp_servers.acp.env]
TOKEN = "x"

[mcp_servers.multi]
command = "srv"
args = [
  "--port",
  "9", # comment
]
`)
	got := map[string]string{}
	for _, s := range MCPServers() {
		got[s.Agent+" "+s.Scope+" "+s.Name] = s.Command + " " + strings.Join(s.Args, "|")
	}
	want := map[string]string{
		"claude user acp":    "/opt/acp mcp",
		"claude project acp": "/old/acp mcp|--x",
		"codex user acp":     `/opt/acp mcp|two|a "q" ]`,
		"codex user multi":   "srv --port|9",
	}
	if len(got) != len(want) {
		t.Fatalf("servers %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	out, _ := core.MarshalJSON(Inventory())
	if strings.Contains(string(out), "--x") || strings.Contains(string(out), "--port") {
		t.Fatalf("inventory carries args:\n%s", out)
	}
}
