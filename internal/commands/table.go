package commands

import (
	"fmt"
	"io"
)

const overview = `acp: see and sync agent skills across your projects.

  acp                      fleet status (AgentKit version, personal links, worktrees)
  acp sync [ID...]         sync the project you are in, from its main checkout or any worktree
  acp sync --all           sync every project in the fleet
  acp upgrade              update the ak binary, pin the channel's latest kit, sync the fleet
  acp add [PATH]           add a repo to the fleet and sync it
  acp remove ID [--yes]    take a repo out of the fleet and drop the links acp made
  acp link [PATH]          offline: link a checkout or worktree (session hooks call this)
  acp unlink [PATH]        remove the links acp made in one checkout
  acp activity             repos with Claude/Codex sessions in the last 30 days
  acp global [--check]     link personal skills from global-skills.json into user homes
  acp hooks [install|uninstall]  link worktrees automatically at session start
  acp doctor               like status, but exits 1 when anything needs a sync
  acp skills list|set ID   show or change a project's skills (set previews until --apply)
  acp kit add ID           give a project AgentKit (previews until --apply)
  acp setup [install-ak|login|link-cli|install-clt]  ak CLI, sign-in, terminal acp, Apple's tools
  acp connect              register acp mcp with Claude Code and Codex (previews until --apply)
  acp init                 create or adopt a control plane (no personal skills)
  acp scan                 read-only health check with plain-language findings
  acp explain CODE         what a scan finding means and how to fix it
  acp web                  local dashboard on 127.0.0.1; every change previews first
  acp mcp                  MCP server on stdio: scan, status, explain and fix previews
  acp describe [COMMAND]   machine-readable contract of every command

Every command takes --json and prints {schema_version, kind, data}.

AgentKit owns native content in each main checkout (` + "`ak kit init`" + `, pinned by
agentkit-releases.lock.json). acp adds personal skill links and mirrors the untracked agent
files of the main checkout into every worktree as symlinks, hidden from git status.
`

var (
	verbose = flagSpec{name: "verbose", short: "v", kind: boolFlag, help: "show ok lines too"}
	dryRun  = flagSpec{name: "dry-run", kind: boolFlag, help: "show what would change; change nothing"}
	noFix   = flagSpec{name: "no-fix", kind: boolFlag, help: "accepted for compatibility; does nothing"}
	apply   = flagSpec{name: "apply", kind: boolFlag, help: "write the manifest and sync; default is a preview"}
	kitFlag = flagSpec{name: "kit", kind: strFlag, def: "engineer", help: "AgentKit kit"}
	channel = flagSpec{name: "channel", kind: strFlag, def: "beta", help: "AgentKit release channel"}
	targets = flagSpec{name: "targets", kind: strFlag, def: "claude-code,codex", help: "AgentKit targets"}
)

var commandTable []*command

func init() {
	commandTable = []*command{
		{name: "status", help: "fleet status", flags: []flagSpec{verbose}, run: cmdStatus},
		{name: "doctor", help: "status, exit 1 on drift", flags: []flagSpec{verbose}, run: cmdStatus},
		{name: "sync", help: "sync projects (default: the one you are in)",
			pos: []posSpec{{name: "ids", many: true, help: "project ids"}},
			flags: []flagSpec{
				{name: "all", kind: boolFlag, help: "every project in the fleet"}, dryRun,
				{name: "offline", kind: boolFlag, help: "skip AgentKit; only links"},
				{name: "adopt", kind: boolFlag, help: "back up old agent-file copies in worktrees and link them to main"},
				noFix}, run: cmdSync},
		{name: "link", help: "offline link of one checkout or worktree",
			pos: []posSpec{{name: "path", optional: true, help: "checkout or worktree (default: here)"}},
			flags: []flagSpec{
				{name: "hook", kind: boolFlag, help: "session hook mode: read cwd from stdin, never fail"},
				{name: "check", kind: boolFlag, help: "write nothing; exit 1 if links are missing"},
				{name: "adopt", kind: boolFlag, help: "back up old copies and link them to main"}}, run: cmdLink},
		{name: "unlink", help: "remove the links acp made in one checkout",
			pos:   []posSpec{{name: "path", optional: true, help: "checkout or worktree (default: here)"}},
			flags: []flagSpec{dryRun}, run: cmdUnlink},
		{name: "upgrade", help: "update ak, pin the channel's latest kit, sync",
			flags: []flagSpec{
				{name: "channel", kind: strFlag, def: "beta", choices: []string{"stable", "beta"}, help: "release channel"},
				{name: "version", kind: strFlag, help: "pin this kit version instead of the latest"},
				{name: "kit", kind: strFlag, help: "only this kit (default: every kit on the channel)"},
				{name: "skip-binary", kind: boolFlag, help: "do not update the ak binary"},
				{name: "no-sync", kind: boolFlag, help: "only change the pins"},
				{name: "dry-run", kind: boolFlag, help: "show the new pins; change nothing"}, noFix}, run: cmdUpgrade},
		{name: "add", help: "add a repo to the fleet",
			pos: []posSpec{{name: "path", optional: true, help: "repo (default: here)"}},
			flags: []flagSpec{{name: "id", kind: strFlag, help: "project id (default: folder name)"}, kitFlag, channel, targets,
				{name: "no-agentkit", kind: boolFlag, help: "personal skills only"},
				{name: "no-sync", kind: boolFlag, help: "only write the manifest and overlay"}, dryRun, noFix}, run: cmdAdd},
		{name: "remove", help: "take a repo out of the fleet",
			pos:   []posSpec{{name: "id", help: "project id"}},
			flags: []flagSpec{{name: "yes", kind: boolFlag, help: "do it; default is a dry run"}}, run: cmdRemove},
		{name: "activity", help: "recent Claude/Codex sessions per repo",
			flags: []flagSpec{{name: "days", kind: intFlag, def: "30", help: "look back this many days"},
				{name: "min", kind: intFlag, def: "2", help: "hide repos with fewer sessions"}}, run: cmdActivity},
		{name: "global", help: "link global personal skills",
			flags: []flagSpec{{name: "check", kind: boolFlag, help: "write nothing; exit 1 if links are missing"}, verbose}, run: cmdGlobal},
		{name: "hooks", help: "session-start hooks that link worktrees", action: true,
			pos: []posSpec{{name: "action", optional: true, def: "status", choices: []string{"status", "install", "uninstall"}}},
			flags: []flagSpec{dryRun,
				{name: "settings", kind: listFlag, meta: "PATH", help: "also this Claude settings file (repeatable)"}}, run: cmdHooks},
		{name: "skills", help: "show or change a project's skills", action: true,
			pos: []posSpec{{name: "action", choices: []string{"list", "set"}}, {name: "id", help: "project id"}},
			flags: []flagSpec{{name: "add", kind: strFlag, help: "comma-separated skill names"},
				{name: "remove", kind: strFlag, help: "comma-separated skill names"},
				{name: "runtimes", kind: strFlag, help: "personal skills only: claude,codex (default: the project's targets)"},
				apply, noFix}, run: cmdSkills},
		{name: "kit", help: "give a project AgentKit", action: true,
			pos:   []posSpec{{name: "action", choices: []string{"add"}}, {name: "id", help: "project id"}},
			flags: []flagSpec{kitFlag, channel, targets, apply, noFix}, run: cmdKit},
		{name: "setup", help: "check, install or sign in to the ak CLI", action: true,
			pos: []posSpec{{name: "action", optional: true, def: "check", choices: []string{"check", "install-ak", "login", "link-cli", "install-clt"}}},
			flags: []flagSpec{
				{name: "channel", kind: strFlag, choices: []string{"stable", "beta"}, help: "install-ak: default is the fleet's channel"},
				{name: "version", kind: strFlag, help: "install-ak: pin an ak CLI version (not the kit version)"},
				{name: "apply", kind: boolFlag, help: "install-ak, link-cli, install-clt: make the change; default is a preview"},
				{name: "api-key-stdin", kind: boolFlag, help: "login: read an AgentKit API key from stdin; no terminal needed"}}, run: cmdSetup},
		{name: "connect", help: "register acp mcp with Claude Code and Codex",
			flags: []flagSpec{{name: "apply", kind: boolFlag, help: "register it; default is a preview"}}, run: cmdConnect},
		{name: "init", help: "create or adopt a control plane",
			flags: []flagSpec{{name: "root", kind: strFlag, meta: "PATH", help: "where (default: ~/agent-control-plane)"}, dryRun}, run: cmdInit},
		{name: "scan", help: "read-only health check with plain-language findings",
			flags: []flagSpec{{name: "module", kind: strFlag, choices: []string{"projects", "agentkit", "hooks"},
				help: "only one dashboard module"}}, run: cmdScan},
		{name: "explain", help: "what a scan finding means and how to fix it",
			pos: []posSpec{{name: "code", help: "finding code, e.g. worktree_unlinked"}}, run: cmdExplain},
		{name: "describe", help: "machine-readable contract of every command",
			pos: []posSpec{{name: "name", many: true, help: "command (default: all)"}}, run: cmdDescribe},
		{name: "web", help: "local dashboard on 127.0.0.1", server: true,
			flags: []flagSpec{{name: "port", kind: intFlag, def: "8780", help: "port (0: any free port)"},
				{name: "no-open", kind: boolFlag, help: "do not open a browser"},
				{name: "print-url", kind: boolFlag, help: "print the sign-in URL even without a terminal"},
				{name: "exit-with-parent", kind: boolFlag, help: "stop when stdin closes (for an app that runs acp web)"}}, run: cmdWeb},
		{name: "mcp", help: "MCP server on stdio with read and preview tools", server: true, run: cmdMCP},
	}
}

func lookup(name string) *command {
	for _, c := range commandTable {
		if c.name == name {
			return c
		}
	}
	return nil
}

func commandNames() []string {
	names := make([]string, len(commandTable))
	for i, c := range commandTable {
		names[i] = c.name
	}
	return names
}

// Main is the acp entry point; it returns the exit code.
func Main(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a, err := parse(argv)
	switch {
	case err == errVersion:
		fmt.Fprintf(stdout, "acp %s\n", Version)
		return 0
	case err == errHelp:
		printHelp(stdout, a.cmd)
		return 0
	case err != nil:
		pe, _ := err.(*parseError)
		if contains(argv, "--json") {
			kind := "acp"
			if pe != nil && pe.cmd != nil {
				kind = "acp." + pe.cmd.name
			}
			writeEnvelope(stdout, kind, &Env{}, "", 2, err)
			return 2
		}
		var cmd *command
		if pe != nil {
			cmd = pe.cmd
		}
		fmt.Fprintf(stderr, "%s\nacp: error: %s\n", usageLine(cmd), err)
		return 2
	}
	run := a.cmd.run
	if run == nil {
		run = func(*Env, *Args) (int, error) { return 0, fail("acp %s is not implemented yet", a.cmd.name) }
	}
	for _, f := range a.cmd.flags {
		if f.name == "no-fix" {
			warnNoFix(stderr, a.Bool("no-fix"))
		}
	}
	return execute(a, run, stdin, stdout, stderr)
}
