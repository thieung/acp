package commands

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/thieung/acp/internal/core"
	"github.com/thieung/acp/internal/scan"
)

// agentCLI is an agent's own CLI, which acp asks to register `acp mcp` so the agent's config file
// is written by the program that owns it.
type agentCLI struct {
	name, env string
	dirs      []string // folders under $HOME tried after PATH
	add       func(bin string) []string
	remove    []string
}

var agentCLIs = []agentCLI{
	// A local install of Claude Code lives in ~/.claude/local, often reached only by a shell alias.
	{name: "claude", env: "ACP_CLAUDE", dirs: []string{".local/bin", ".claude/local"},
		add:    func(bin string) []string { return []string{"mcp", "add", "--scope", "user", "acp", "--", bin, "mcp"} },
		remove: []string{"mcp", "remove", "--scope", "user", "acp"}},
	{name: "codex", env: "ACP_CODEX", dirs: []string{".local/bin"},
		add:    func(bin string) []string { return []string{"mcp", "add", "acp", "--", bin, "mcp"} },
		remove: []string{"mcp", "remove", "acp"}},
}

// find returns the CLI's path, or "" when it is not installed: ACP_CLAUDE / ACP_CODEX when set
// (nothing else is tried then), else PATH, else its folders under $HOME. The desktop app hands acp
// the PATH of a login shell, so no other folder is guessed.
func (c agentCLI) find() string {
	dirs := make([]string, len(c.dirs))
	for i, d := range c.dirs {
		dirs[i] = filepath.Join(core.HomeDir(), d)
	}
	p, _ := core.FindCLI(c.env, c.name, dirs...)
	return p
}

// run runs the CLI from $HOME, so no project's .mcp.json or settings are in play.
func (c agentCLI) run(cli string, args ...string) string {
	res := core.Exec(context.Background(), core.ExecOpts{Dir: core.HomeDir(), Timeout: 30 * time.Second}, cli, args...)
	switch {
	case res.TimedOut:
		return c.name + " did not answer in 30 seconds"
	case res.Code == 127:
		return c.name + " is installed but cannot run (missing node?)"
	case !res.OK():
		msg, _, _ := strings.Cut(strings.TrimSpace(res.Stderr+errText(res.Err)), "\n")
		return c.name + ": `" + c.name + " " + strings.Join(args[:2], " ") + "` failed: " + core.Redact(msg)
	}
	return ""
}

// mcpEntry is the state of acp's user-scope entry in an agent's config, read from the file and never
// by running the agent (`mcp get` would start every server to check its health): missing, ok when it
// runs bin with the single argument mcp, else stale. old is the command the entry runs now.
func mcpEntry(agent, bin string) (state, old string) {
	for _, s := range scan.MCPServers() {
		if s.Agent != agent || s.Scope != "user" || s.Name != "acp" {
			continue
		}
		switch {
		case s.Remote:
			return "stale", "a remote server"
		case filepath.IsAbs(s.Command) && resolve(s.Command) == resolve(bin) && slices.Equal(s.Args, []string{"mcp"}):
			return "ok", s.Command
		}
		return "stale", s.Command
	}
	return "missing", ""
}

// cmdConnect registers the read-only `acp mcp` server at user scope with every installed agent CLI,
// replacing an acp entry that runs another binary or other arguments. Other servers are never
// touched, and their args and env never leave acp.
func cmdConnect(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	bin, err := hookBinary()
	if err != nil {
		return 0, err
	}
	apply := a.Bool("apply")
	if apply {
		lock, err := lockRoot(e, root)
		if err != nil {
			return 0, err
		}
		defer lock.Release()
	}
	log := core.NewLog("acp mcp in AI agents")
	agents := []any{}
	failed := false
	for _, c := range agentCLIs {
		cli := c.find()
		if cli == "" {
			log.Issue("%s: not installed", c.name)
			agents = append(agents, core.Obj("name", c.name, "cli", nil, "state", "not installed"))
			continue
		}
		state, old := mcpEntry(c.name, bin)
		switch state {
		case "ok":
			log.Ok("%s: acp mcp is registered", c.name)
		case "missing":
			log.Change("+ %s: register acp mcp", c.name)
		case "stale":
			log.Change("~ %s: replace stale acp (%s)", c.name, old)
		}
		if apply && state != "ok" {
			msg := ""
			if state == "stale" {
				msg = c.run(cli, c.remove...)
			}
			if msg == "" {
				msg = c.run(cli, c.add(bin)...)
			}
			// Report what the config says now, not what the CLI answered.
			if state, _ = mcpEntry(c.name, bin); msg == "" && state != "ok" {
				msg = c.name + ": acp mcp is still " + state + " after " + c.name + " mcp add"
			}
			if msg != "" {
				log.Issue("%s", msg)
				failed = true
			}
		}
		agents = append(agents, core.Obj("name", c.name, "cli", cli, "state", state))
	}
	e.Set("agents", agents)
	e.PrintLog(log, true)
	if !apply && len(log.Changes) > 0 {
		e.Printf("%s\n", e.Paint("2", "preview: nothing was changed; pass --apply"))
	}
	if failed {
		return 1, nil
	}
	return 0, nil
}
