package scan

import (
	"os"
	"path/filepath"

	"github.com/thieung/acp/internal/core"
)

// Inventory lists what the dashboard's Hooks, MCP & Settings and AgentKit & Runtimes sections show:
// every hook of every settings file acp knows, every MCP server by name and command, and each
// runtime's skills home. Commands are redacted; MCP args, env, headers and urls are left out.
func Inventory() *core.Object {
	files := []any{}
	for _, t := range core.HookTargets(nil) {
		data, ok := readSettings(t.Path)
		_, statErr := os.Lstat(t.Path)
		hooks := []any{}
		for _, h := range hookEntries(data) {
			kind := "other"
			switch {
			case core.IsLegacyHook(h.command):
				kind = "legacy"
			case core.HookBinary(h.command) != "":
				kind = "acp"
			}
			hooks = append(hooks, core.Obj("event", h.event, "matcher", h.matcher, "command", core.Redact(h.command),
				"kind", kind))
		}
		files = append(files, core.Obj("name", t.Name, "path", t.Path, "exists", statErr == nil, "readable", ok,
			"hooks", hooks))
	}
	servers := []any{}
	for _, s := range mcpServers() {
		servers = append(servers, core.Obj("source", s.Source, "name", s.Name, "command", core.Redact(s.Command),
			"remote", s.Remote))
	}
	homes := []any{}
	folders := core.GlobalHomes()
	for _, rt := range core.GlobalRuntimes {
		dir := folders[rt]
		entries, err := os.ReadDir(filepath.Join(dir, "skills"))
		_, statErr := os.Stat(dir)
		skills := 0
		if err == nil {
			skills = len(entries)
		}
		homes = append(homes, core.Obj("runtime", rt, "label", runtimeLabels[rt], "folder", dir,
			"exists", statErr == nil, "skills", skills))
	}
	return core.Obj("settings", files, "mcp", servers, "homes", homes)
}

var runtimeLabels = map[string]string{
	"claude": "Claude Code", "codex": "Codex", "pi": "Pi", "omp": "OMP",
	"pi-ak": "Pi (hồ sơ pi-ak)", "pi-omp": "Pi (hồ sơ pi-omp)",
}
