package commands

import (
	"fmt"
	"strings"

	"github.com/thieung/acp/internal/core"
)

type contract struct {
	name        string
	summary     string
	mutation    bool
	risk        string
	preview     string
	confirm     string
	network     bool
	interactive bool
	server      bool
}

// contracts is describe/2: Python's describe/1 without the fix commands and the agent field,
// plus init, scan, explain and mcp.
var contracts = []contract{
	{name: "status", summary: "fleet status: AgentKit per target, personal links, worktrees"},
	{name: "doctor", summary: "fleet status; exit 1 when anything needs a sync"},
	{name: "sync", summary: "install or refresh AgentKit and links", mutation: true, risk: "medium",
		preview: "--dry-run", network: true},
	{name: "link", summary: "offline link of one checkout or worktree", mutation: true, risk: "low", preview: "--check"},
	{name: "unlink", summary: "remove the links acp made in one checkout", mutation: true, risk: "low", preview: "--dry-run"},
	{name: "upgrade", summary: "update ak, pin the channel's latest kit, sync the fleet", mutation: true, risk: "high",
		preview: "--dry-run", network: true},
	{name: "add", summary: "add a repo to the fleet and sync it", mutation: true, risk: "medium", preview: "--dry-run"},
	{name: "remove", summary: "take a repo out of the fleet", mutation: true, risk: "medium",
		preview: "default; dry run until --yes", confirm: "--yes"},
	{name: "activity", summary: "recent Claude/Codex sessions per repo"},
	{name: "global", summary: "link global personal skills into user homes", mutation: true, risk: "low", preview: "--check"},
	{name: "hooks", summary: "session-start hooks that link worktrees", mutation: true, risk: "low",
		preview: "--dry-run (or the status action)"},
	{name: "skills list", summary: "a project's AgentKit and personal skills with their catalogs"},
	{name: "skills set", summary: "add or remove skills of a project", mutation: true, risk: "medium",
		preview: "default; preview until --apply", confirm: "--apply"},
	{name: "kit add", summary: "give a project AgentKit", mutation: true, risk: "medium",
		preview: "default; preview until --apply", confirm: "--apply", network: true},
	{name: "setup", summary: "ak binary, sign-in, doctor and pins", network: true},
	{name: "setup install-ak", summary: "download and run AgentKit's official verified installer", mutation: true,
		risk: "high", preview: "default; plan until --apply", confirm: "--apply", network: true},
	{name: "setup login", summary: "sign in with an AgentKit API key on stdin, or `ak onboard` in a terminal",
		mutation: true, risk: "medium", preview: "none: an API key sign-in has nothing to preview; ak checks the key",
		network: true},
	{name: "setup link-cli", summary: "point ~/.local/bin/acp at this acp binary, replacing only another acp (backed up)",
		mutation: true, risk: "low", preview: "default; preview until --apply", confirm: "--apply"},
	{name: "setup install-clt", summary: "open Apple's installer for the Command Line Tools (macOS; git needs them)",
		mutation: true, risk: "low", preview: "default; plan until --apply", confirm: "--apply", interactive: true},
	{name: "connect", summary: "register the read-only acp mcp server with Claude Code and Codex at user scope",
		mutation: true, risk: "low", preview: "default; preview until --apply", confirm: "--apply"},
	{name: "init", summary: "create or adopt a control plane without personal skills", mutation: true, risk: "low",
		preview: "--dry-run"},
	{name: "scan", summary: "read-only health check with stable finding codes and explanations"},
	{name: "explain", summary: "plain-language explanation of one finding code"},
	{name: "describe", summary: "this contract"},
	{name: "web", summary: "local dashboard on 127.0.0.1 with a one-time sign-in; every change previews first",
		risk: "low", server: true},
	{name: "mcp", summary: "MCP server on stdio; tools only read and preview", server: true},
}

func (c contract) object() *core.Object {
	risk := c.risk
	if risk == "" {
		risk = "read"
	}
	var preview, confirm any
	if c.preview != "" {
		preview = c.preview
	}
	if c.confirm != "" {
		confirm = c.confirm
	}
	o := core.Obj("name", c.name, "mutation", c.mutation, "risk", risk, "preview", preview, "confirm", confirm,
		"network", c.network, "json", true, "summary", c.summary)
	if c.interactive {
		o.Set("interactive", true)
	}
	if c.server {
		o.Set("server", true)
	}
	return o
}

func cmdDescribe(e *Env, a *Args) (int, error) {
	name := strings.Join(a.List("name"), " ")
	list := contracts
	if name != "" {
		list = nil
		for _, c := range contracts {
			if c.name == name {
				list = []contract{c}
			}
		}
		if list == nil {
			return 0, fail("no command '%s'", name)
		}
	}
	commands := make([]any, len(list))
	for i, c := range list {
		commands[i] = c.object()
		flags := "read"
		if c.mutation {
			flags = "mutation " + c.risk
		}
		line := fmt.Sprintf("%-17s %-16s %s", c.name, flags, c.summary)
		if c.preview != "" {
			line += "  [preview: " + c.preview + "]"
		}
		e.Printf("%s\n", line)
	}
	e.Set("schema", "acp.describe/2")
	e.Set("envelope", core.Obj("schema_version", 1, "kind", "acp.<command>", "data", "object",
		"error", "string on failure"))
	e.Set("commands", commands)
	return 0, nil
}
