package core

import (
	"path/filepath"
	"strings"
)

// PluginRuntimes are the runtimes a manifest's `plugins` entry may name. Plugins exist in Claude
// Code only; Codex has no plugin system.
var PluginRuntimes = []string{"claude"}

// Plugin is one entry of a manifest's `plugins` list: a Claude Code plugin named
// <plugin>@<marketplace>, the version the manifest pins (or "") and the rule files it brings.
type Plugin struct {
	Name    string
	Runtime string
	Version string
	Rules   []string
}

// Plugins lists the manifest's declared plugins in manifest order; runtime defaults to claude.
func (p *Project) Plugins() []Plugin {
	var out []Plugin
	for _, it := range p.Manifest.Array("plugins") {
		o, ok := it.(*Object)
		if !ok {
			continue
		}
		out = append(out, Plugin{Name: o.String("name"), Runtime: o.StringOr("runtime", "claude"),
			Version: o.String("version"), Rules: o.Strings("rules")})
	}
	return out
}

// Supported says whether acp can manage this plugin: a Claude Code plugin named <plugin>@<marketplace>.
func (pl Plugin) Supported() bool {
	for _, rt := range PluginRuntimes {
		if rt == pl.Runtime {
			return strings.Contains(pl.Name, "@")
		}
	}
	return false
}

// Short is the plugin name without its marketplace.
func (pl Plugin) Short() string { return strings.SplitN(pl.Name, "@", 2)[0] }

// PluginState is what Claude Code records for one declared plugin, read from
// ~/.claude/plugins/installed_plugins.json and the enabledPlugins of two settings files: the main
// checkout's .claude/settings.json (project scope) and ~/.claude/settings.json (user scope).
// It mirrors plugin_state of the Python acp.
type PluginState struct {
	Plugin
	Installed   string // version installed at project scope for the main checkout, "" when none
	Enabled     bool   // enabledPlugins[name] is true in <main>/.claude/settings.json
	UserEnabled bool   // enabledPlugins[name] is true in ~/.claude/settings.json
	UserVersion string // version installed at user scope, "" when none
}

// OK is the Python acp's ok: installed and enabled at project scope, at the pinned version if any.
func (s PluginState) OK() bool {
	return s.Installed != "" && s.Enabled && (s.Version == "" || s.Installed == s.Version)
}

// PluginRegistry is installed_plugins.json's path under the Claude Code home.
func PluginRegistry() string {
	return filepath.Join(GlobalHomes()["claude"], "plugins", "installed_plugins.json")
}

// ReadPluginState reads one plugin's state for a main checkout (already resolved). Missing or
// unreadable files count as nothing installed and nothing enabled.
func ReadPluginState(pl Plugin, main string) PluginState {
	s := PluginState{Plugin: pl}
	registry, _ := ReadObjectOr(PluginRegistry())
	for _, it := range registry.Object("plugins").Array(pl.Name) {
		install, ok := it.(*Object)
		if !ok {
			continue
		}
		switch install.String("scope") {
		case "project":
			if path := install.String("projectPath"); path != "" && resolvePath(path) == main {
				s.Installed = install.String("version")
			}
		case "user":
			if s.UserVersion == "" {
				s.UserVersion = install.String("version")
			}
		}
	}
	s.Enabled = pluginEnabled(filepath.Join(main, ".claude", "settings.json"), pl.Name)
	s.UserEnabled = pluginEnabled(filepath.Join(GlobalHomes()["claude"], "settings.json"), pl.Name)
	return s
}

// pluginEnabled is `enabledPlugins[name] is True` of one settings file.
func pluginEnabled(settings, name string) bool {
	o, err := ReadObjectOr(settings)
	if err != nil {
		return false
	}
	return o.Object("enabledPlugins").Bool(name)
}
