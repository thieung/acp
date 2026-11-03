package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thieung/acp/internal/testkit"
)

// connectResult is one `acp connect --json` run: its log lines by kind and each agent's state.
type connectResult struct {
	result
	changes, issues, ok []string
	states              map[string]string
	clis                map[string]any
}

func connect(t *testing.T, f *testkit.Fixture, args ...string) connectResult {
	t.Helper()
	r := acp(t, f, "", append([]string{"connect", "--json"}, args...)...)
	c := connectResult{result: r, states: map[string]string{}, clis: map[string]any{}}
	data, _ := envelope(t, r.stdout)["data"].(map[string]any)
	logs, _ := data["logs"].([]any)
	for _, l := range logs {
		lo := l.(map[string]any)
		for kind, dst := range map[string]*[]string{"changes": &c.changes, "issues": &c.issues, "ok": &c.ok} {
			for _, s := range lo[kind].([]any) {
				*dst = append(*dst, s.(string))
			}
		}
	}
	agents, _ := data["agents"].([]any)
	for _, a := range agents {
		ao := a.(map[string]any)
		c.states[ao["name"].(string)] = ao["state"].(string)
		c.clis[ao["name"].(string)] = ao["cli"]
		if len(ao) != 3 {
			t.Errorf("agent carries more than name, cli and state: %v", ao)
		}
	}
	return c
}

func calls(f *testkit.Fixture, cli string) string {
	return f.Read(filepath.Join(f.Home, ".fake-"+cli+"-calls"))
}

func noCalls(t *testing.T, f *testkit.Fixture, when string) {
	t.Helper()
	for _, cli := range []string{"claude", "codex"} {
		if f.Exists(filepath.Join(f.Home, ".fake-"+cli+"-calls")) {
			t.Fatalf("%s ran %s: %q", when, cli, calls(f, cli))
		}
	}
}

func TestConnectRegistersAcpMCPWithEachAgent(t *testing.T) {
	f := testkit.New(t)
	bin := installableBinary(t, f, filepath.Join(f.Base, "app"))
	c := connect(t, f)
	if c.code != 0 || strings.Join(c.changes, "\n") != "+ claude: register acp mcp as "+bin+" mcp\n+ codex: register acp mcp as "+bin+" mcp" {
		t.Fatalf("preview: %d %q\n%s%s", c.code, c.changes, c.stdout, c.stderr)
	}
	if c.states["claude"] != "missing" || c.states["codex"] != "missing" || c.clis["claude"] != f.Claude {
		t.Fatalf("preview agents %v %v", c.states, c.clis)
	}
	noCalls(t, f, "the preview")

	c = connect(t, f, "--apply")
	if c.code != 0 || c.states["claude"] != "ok" || c.states["codex"] != "ok" {
		t.Fatalf("apply: %d %v\n%s%s", c.code, c.states, c.stdout, c.stderr)
	}
	if got := calls(f, "claude"); got != "mcp add --scope user acp -- "+bin+" mcp\n" {
		t.Fatalf("claude calls %q", got)
	}
	if got := calls(f, "codex"); got != "mcp add acp -- "+bin+" mcp\n" {
		t.Fatalf("codex calls %q", got)
	}
	servers := readMap(t, filepath.Join(f.Home, ".claude.json"))["mcpServers"].(map[string]any)
	if servers["acp"].(map[string]any)["command"] != bin {
		t.Fatalf("~/.claude.json %v", servers)
	}

	c = connect(t, f, "--apply")
	if c.code != 0 || len(c.changes) != 0 || len(c.ok) != 2 || c.states["claude"] != "ok" || c.states["codex"] != "ok" {
		t.Fatalf("re-run: %d %q %q %v", c.code, c.changes, c.ok, c.states)
	}
	if strings.Count(calls(f, "claude")+calls(f, "codex"), "\n") != 2 {
		t.Fatalf("the re-run called the agents: %q %q", calls(f, "claude"), calls(f, "codex"))
	}
	if r := acp(t, f, "", "connect"); r.code != 0 || !strings.Contains(r.stdout, "claude: acp mcp is registered") {
		t.Fatalf("text: %d %q", r.code, r.stdout)
	}
}

func TestConnectReplacesAStaleEntry(t *testing.T) {
	f := testkit.New(t)
	bin := installableBinary(t, f, filepath.Join(f.Base, "app"))
	f.Write(filepath.Join(f.Home, ".claude.json"),
		`{"mcpServers": {"acp": {"type": "stdio", "command": "/old/acp", "args": ["mcp"], "env": {}}}}`)
	// The right binary with other arguments is stale too.
	f.Write(filepath.Join(f.Home, ".codex/config.toml"), "[mcp_servers.acp]\ncommand = \""+bin+"\"\nargs = [\"mcp\", \"--x\"]\n")
	c := connect(t, f)
	if c.code != 0 || strings.Join(c.changes, "\n") != "~ claude: replace stale acp (/old/acp) with "+bin+" mcp\n~ codex: replace stale acp ("+bin+") with "+bin+" mcp" ||
		c.states["claude"] != "stale" || c.states["codex"] != "stale" {
		t.Fatalf("preview: %d %q %v", c.code, c.changes, c.states)
	}
	noCalls(t, f, "the preview")
	c = connect(t, f, "--apply")
	if c.code != 0 || c.states["claude"] != "ok" || c.states["codex"] != "ok" {
		t.Fatalf("apply: %d %v %q\n%s", c.code, c.states, c.issues, c.stderr)
	}
	if got := calls(f, "claude"); got != "mcp remove --scope user acp\nmcp add --scope user acp -- "+bin+" mcp\n" {
		t.Fatalf("claude calls %q", got)
	}
	if got := calls(f, "codex"); got != "mcp remove acp\nmcp add acp -- "+bin+" mcp\n" {
		t.Fatalf("codex calls %q", got)
	}
}

func TestConnectIgnoresAProjectScopeEntry(t *testing.T) {
	f := testkit.New(t)
	bin := installableBinary(t, f, filepath.Join(f.Base, "app"))
	f.Write(filepath.Join(f.Home, ".claude.json"),
		`{"projects": {"/work": {"mcpServers": {"acp": {"command": "`+bin+`", "args": ["mcp"]}}}}}`)
	if c := connect(t, f); c.code != 0 || c.states["claude"] != "missing" {
		t.Fatalf("preview: %d %v", c.code, c.states)
	}
	noCalls(t, f, "the preview")
}

func TestConnectGoesOnWithoutAnAgent(t *testing.T) {
	f := testkit.New(t)
	bin := installableBinary(t, f, filepath.Join(f.Base, "app"))
	// Set but not executable: acp must not fall back to a claude on PATH.
	t.Setenv("ACP_CLAUDE", filepath.Join(f.Base, "nowhere/claude"))
	c := connect(t, f, "--apply")
	if c.code != 0 || strings.Join(c.issues, "\n") != "claude: not installed" || c.states["claude"] != "not installed" ||
		c.clis["claude"] != nil || c.states["codex"] != "ok" {
		t.Fatalf("apply: %d %q %v %v\n%s", c.code, c.issues, c.states, c.clis, c.stderr)
	}
	if got := calls(f, "codex"); got != "mcp add acp -- "+bin+" mcp\n" {
		t.Fatalf("codex calls %q", got)
	}

	// A CLI whose interpreter is missing exits 127.
	f.Executable(filepath.Join(f.Base, "broken/claude"), "#!/bin/sh\nexit 127\n")
	t.Setenv("ACP_CLAUDE", filepath.Join(f.Base, "broken/claude"))
	c = connect(t, f, "--apply")
	if c.code != 1 || strings.Join(c.issues, "\n") != "claude is installed but cannot run (missing node?)" || c.states["codex"] != "ok" {
		t.Fatalf("broken claude: %d %q %v", c.code, c.issues, c.states)
	}
}

func TestConnectNeverShowsOtherServersSecrets(t *testing.T) {
	f := testkit.New(t)
	installableBinary(t, f, filepath.Join(f.Base, "app"))
	f.Write(filepath.Join(f.Home, ".claude.json"), `{"mcpServers": {"tools": {"type": "stdio", "command": "/opt/tools",
  "args": ["--token-arg", "argsecret-1"], "env": {"TOKEN": "envsecret-1"}}},
  "projects": {"/work": {"mcpServers": {"acp": {"command": "/p/acp", "args": ["projsecret-1"]}}}}}`)
	f.Write(filepath.Join(f.Home, ".codex/config.toml"),
		"[mcp_servers.tools]\ncommand = \"/opt/tools\"\nargs = [\"argsecret-2\"]\nenv = { \"TOKEN\" = \"envsecret-2\" }\n")
	outs := []string{}
	for _, args := range [][]string{{}, {"--apply"}} {
		c := connect(t, f, args...)
		if c.code != 0 {
			t.Fatalf("%v: %d\n%s%s", args, c.code, c.stdout, c.stderr)
		}
		outs = append(outs, c.stdout, c.stderr, acp(t, f, "", append([]string{"connect"}, args...)...).stdout)
	}
	for _, out := range outs {
		for _, secret := range []string{"argsecret", "envsecret", "projsecret", "TOKEN", "--token-arg"} {
			if strings.Contains(out, secret) {
				t.Fatalf("output contains %q:\n%s", secret, out)
			}
		}
	}
	// The other servers stay as they were.
	tools := readMap(t, filepath.Join(f.Home, ".claude.json"))["mcpServers"].(map[string]any)["tools"].(map[string]any)
	if tools["env"].(map[string]any)["TOKEN"] != "envsecret-1" || !strings.Contains(f.Read(filepath.Join(f.Home, ".codex/config.toml")), "envsecret-2") {
		t.Fatalf("other servers changed: %v", tools)
	}
}

func TestConnectRefusesAPathThatVanishes(t *testing.T) {
	f := testkit.New(t)
	// No ACP_BIN: the test binary lives in a temporary folder.
	c := connect(t, f, "--apply")
	if c.code != 1 || !strings.Contains(c.stdout, "temporary") {
		t.Fatalf("%d\n%s%s", c.code, c.stdout, c.stderr)
	}
	noCalls(t, f, "a refused connect")
	if _, err := os.Stat(filepath.Join(f.Home, ".claude.json")); err == nil {
		t.Fatal("a refused connect wrote ~/.claude.json")
	}
}

func TestConnectFindsClaudesLocalInstall(t *testing.T) {
	f := testkit.New(t)
	t.Setenv("ACP_CLAUDE", "")
	t.Setenv("ACP_CODEX", "")
	t.Setenv("PATH", filepath.Join(f.Base, "empty"))
	local := filepath.Join(f.Home, ".claude/local/claude")
	f.Executable(local, testkit.FakeClaude)
	f.Executable(filepath.Join(f.Home, ".claude/local/codex"), testkit.FakeCodex)
	for _, c := range agentCLIs {
		want := ""
		if c.name == "claude" {
			want = local
		}
		if got := c.find(); got != want {
			t.Errorf("%s: found %q, want %q", c.name, got, want)
		}
	}
	// ~/.local/bin comes first.
	user := filepath.Join(f.Home, ".local/bin/claude")
	f.Executable(user, testkit.FakeClaude)
	if got := agentCLIs[0].find(); got != user {
		t.Errorf("claude: found %q, want %q", got, user)
	}
}
