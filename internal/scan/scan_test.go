package scan

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thieung/acp/internal/core"
	"github.com/thieung/acp/internal/testkit"
)

// healthyAK answers whoami and doctor like a signed-in, healthy ak and otherwise acts as FakeAK.
func healthyAK(t *testing.T, f *testkit.Fixture) {
	ak := filepath.Join(f.Base, "bin/ak-healthy")
	f.Executable(ak, `#!/bin/sh
case "$1" in
  whoami) echo '{"data": {"authenticated": true, "authMethod": "device"}}'; exit 0 ;;
  doctor) echo '{"data": {"healthy": true}}'; exit 0 ;;
esac
exec `+f.AK+` "$@"
`)
	t.Setenv("ACP_AK", ak)
}

// synced is a fixture after `acp sync --all`, with a healthy ak and acp's hook installed.
func synced(t *testing.T) *testkit.Fixture {
	f := testkit.New(t)
	healthyAK(t, f)
	f.Symlink("rules", filepath.Join(f.Repo, ".claude/team-link"))
	f.Git(f.Repo, "add", ".claude/team-link")
	f.Git(f.Repo, "commit", "-qm", "tracked link")
	f.Git(f.WT, "merge", "-q", "main")
	root := &core.Root{Dir: f.CP}
	eng := core.NewEngine(root)
	fleet, _ := root.Fleet()
	for _, p := range fleet {
		for _, l := range eng.SyncProject(context.Background(), p, core.SyncOpts{Native: true}) {
			if len(l.Issues) > 0 {
				t.Fatalf("sync: %v", l.Issues)
			}
		}
	}
	bin := filepath.Join(f.Base, "bin/acp")
	f.Executable(bin, "#!/bin/sh\n")
	f.Write(filepath.Join(f.Home, ".claude/settings.json"), `{"hooks": {"SessionStart": [
  {"matcher": "startup|resume", "hooks": [{"type": "command", "command": "`+bin+` link --hook", "timeout": 10}]}]}}`)
	return f
}

func run(t *testing.T, f *testkit.Fixture, o Options) *Report {
	t.Helper()
	if o.Budget == 0 {
		o.Budget = 10 * time.Second
	}
	return Run(context.Background(), &core.Root{Dir: f.CP}, o)
}

func codes(r *Report) []string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, f.Code)
	}
	sort.Strings(out)
	return out
}

func TestHealthyFleetHasNoFindings(t *testing.T) {
	f := synced(t)
	if got := codes(run(t, f, Options{})); len(got) != 0 {
		t.Fatalf("findings on a synced fleet: %v", got)
	}
}

func TestScanReportsExactlyTheProblemsPresent(t *testing.T) {
	f := synced(t)
	f.Git(f.Repo, "worktree", "add", "-q", "-b", "late", filepath.Join(f.Base, "late"))
	os.Remove(filepath.Join(f.Repo, ".claude/team-link"))
	script := filepath.Join(f.Base, "hooks/missing.sh")
	f.Write(filepath.Join(f.Home, ".claude/settings.json"), `{"hooks": {
  "SessionStart": [{"matcher": "startup|resume", "hooks": [{"type": "command", "command": "`+filepath.Join(f.Base, "bin/acp")+` link --hook"}]}],
  "Stop": [{"hooks": [{"type": "command", "command": "say done"}, {"type": "command", "command": "say done"}]}],
  "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "bash `+script+` --strict"}]}]}}`)
	f.Symlink(filepath.Join(f.Base, "nowhere"), filepath.Join(f.Home, ".claude/skills/old"))
	f.Write(filepath.Join(f.CP, "agentkit-releases.lock.json"), `{"schemaVersion": 2, "channels": {}}`)

	r := run(t, f, Options{})
	want := []string{"hook_duplicate", "hook_target_missing", "pin_missing", "skill_link_broken",
		"tracked_agent_files_changed", "worktree_unlinked"}
	if got := codes(r); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("codes %v\nwant  %v", got, want)
	}
	for _, fd := range r.Findings {
		switch fd.Code {
		case "worktree_unlinked":
			if fd.Fix == nil || fd.Fix.Command() != "acp link "+filepath.Join(f.Base, "late") {
				t.Errorf("worktree fix %+v", fd.Fix)
			}
			if fd.Fix.Preview() != "acp link "+filepath.Join(f.Base, "late")+" --check" {
				t.Errorf("preview %q", fd.Fix.Preview())
			}
		case "pin_missing":
			if fd.Fix == nil || fd.Fix.Kind != "upgrade" || fd.Module != "agentkit" {
				t.Errorf("pin finding %+v", fd)
			}
			if want := "acp upgrade --channel " + fd.Params["channel"] + " --kit " + fd.Params["kit"] + " --dry-run"; fd.Params["channel"] == "" || fd.Fix.Preview() != want {
				t.Errorf("pin preview %q, want %q", fd.Fix.Preview(), want)
			}
		case "tracked_agent_files_changed":
			if fd.Severity != "decide" || fd.Fix != nil {
				t.Errorf("tracked finding %+v", fd)
			}
		case "hook_target_missing":
			if !strings.Contains(fd.Subject, script) || fd.Module != "hooks" {
				t.Errorf("hook target %+v", fd)
			}
		}
		if !strings.HasPrefix(fd.ID, fd.Code+"-") {
			t.Errorf("id %q", fd.ID)
		}
	}
	if s := r.Summary["projects"]; s["total"] != 2 {
		t.Errorf("projects summary %v", s)
	}
	only := run(t, f, Options{Module: "hooks"})
	for _, fd := range only.Findings {
		if fd.Module != "hooks" {
			t.Fatalf("--module hooks returned %+v", fd)
		}
	}
}

func TestScanFindsUntrackedLinksAndOldCopies(t *testing.T) {
	f := synced(t)
	// An acp link git shows as untracked: the worktree's exclude file was removed.
	gitDir, _ := core.GitDir(f.WT)
	os.Remove(filepath.Join(gitDir, "acp-exclude"))
	r := run(t, f, Options{})
	if got := codes(r); !contains(got, "untracked_acp_links") {
		t.Fatalf("codes %v", got)
	}
	f2 := synced(t)
	cook := filepath.Join(f2.WT, ".claude/skills/ak-cook")
	os.Remove(cook)
	f2.Write(filepath.Join(cook, "SKILL.md"), "an older copy")
	if got := codes(run(t, f2, Options{})); !contains(got, "old_copy_shadows_main") {
		t.Fatalf("codes %v", got)
	}
}

func TestScanFindsAkProblemsAndUntrackedProjects(t *testing.T) {
	f := synced(t)
	t.Setenv("ACP_AK", f.AK) // whoami and doctor print nothing: not signed in, not healthy
	app := filepath.Join(f.Home, "code/app")
	os.MkdirAll(app, 0o755)
	f.Git(app, "init", "-q")
	for _, n := range []string{"a", "b"} {
		f.Write(filepath.Join(f.Home, ".claude/projects/p", n+".jsonl"), `{"cwd":"`+app+`"}`+"\n")
	}
	got := codes(run(t, f, Options{}))
	for _, c := range []string{"ak_not_signed_in", "ak_doctor_unhealthy", "project_not_tracked"} {
		if !contains(got, c) {
			t.Fatalf("%s missing from %v", c, got)
		}
	}
	t.Setenv("ACP_AK", filepath.Join(f.Base, "missing-ak"))
	got = codes(run(t, f, Options{}))
	if !contains(got, "ak_missing") || contains(got, "ak_not_signed_in") {
		t.Fatalf("codes %v", got)
	}
}

func TestScanFindsHookAndSkillProblems(t *testing.T) {
	f := synced(t)
	f.Write(filepath.Join(f.Home, ".claude/settings.json"), `{"hooks": {"SessionStart": [
  {"hooks": [{"type": "command", "command": "python3 /old/agent-control-plane/scripts/acp link --hook"}]}]}}`)
	f.Write(filepath.Join(f.Home, ".codex/hooks.json"), `{"hooks": {"SessionStart": [
  {"hooks": [{"type": "command", "command": "/gone/bin/acp link --hook"}]}]}}`)
	f.Write(filepath.Join(f.CP, "global-skills.json"), `{"skills": [{"name": "herdr", "runtimes": ["claude"]}]}`)
	f.Write(filepath.Join(f.Home, ".claude/skills/notes/README.md"), "no SKILL.md here")
	f.Write(filepath.Join(f.Home, ".claude/skills/review/SKILL.md"), "v1")
	f.Write(filepath.Join(f.Home, ".codex/skills/review/SKILL.md"), "v2")
	f.Write(filepath.Join(f.Home, ".claude/skills/plan/SKILL.md"), "v1")
	f.Write(filepath.Join(f.Home, ".codex/skills/plan/SKILL.md"), "v2")
	findings := run(t, f, Options{})
	got := codes(findings)
	for _, c := range []string{"acp_hook_legacy", "acp_hook_stale", "global_skill_drift", "skill_dir_without_skill_md", "skill_copy_drift"} {
		if !contains(got, c) {
			t.Fatalf("%s missing from %v", c, got)
		}
	}
	// Copies that differ between the same homes are one finding, not one per skill.
	var drift []Finding
	for _, fd := range findings.Findings {
		if fd.Code == "skill_copy_drift" {
			drift = append(drift, fd)
		}
	}
	if len(drift) != 1 || drift[0].Params["count"] != "2" || drift[0].Params["skills"] != "plan, review" {
		t.Fatalf("drift %+v", drift)
	}
	f.Write(filepath.Join(f.Home, ".claude/settings.json"), `{"hooks": {}}`)
	if got := codes(run(t, f, Options{})); !contains(got, "acp_hook_missing") {
		t.Fatalf("codes %v", got)
	}
}

func TestMCPServersAreCheckedWithoutLeakingSecrets(t *testing.T) {
	f := synced(t)
	f.Write(filepath.Join(f.Home, ".claude.json"), `{
  "mcpServers": {
    "gone": {"type": "stdio", "command": "/opt/missing/server", "args": ["--key", "secret-x"],
      "env": {"GITHUB_TOKEN": "ghp_x"}},
    "remote": {"type": "http", "url": "https://mcp.example.com", "headers": {"Authorization": "Bearer secret-x"}}
  },
  "projects": {"/work": {"mcpServers": {"local": {"command": "definitely-not-on-path-acp"}}},
    "/other": {"allowedTools": []}, "/odd": "not an object"}
}`)
	f.Write(filepath.Join(f.Home, ".codex/config.toml"), `model = "x"
[mcp_servers.tools]
command = "/opt/missing/codex-tool"
env = { "API_KEY" = "secret-x" }

[mcp_servers.web]
url = "https://example.com/mcp"
`)
	r := run(t, f, Options{})
	var names []string
	for _, fd := range r.Findings {
		if fd.Code == "mcp_command_missing" {
			names = append(names, fd.Params["server"])
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "gone,local,tools" {
		t.Fatalf("mcp servers reported %v", names)
	}
	out, _ := core.MarshalJSON(r.Object())
	for _, secret := range []string{"secret-x", "ghp_x", "mcp.example.com"} {
		if strings.Contains(string(out), secret) {
			t.Fatalf("scan output contains %q:\n%s", secret, out)
		}
	}
}

func TestScanWritesNothing(t *testing.T) {
	f := synced(t)
	f.Git(f.Repo, "worktree", "add", "-q", "-b", "late", filepath.Join(f.Base, "late"))
	f.Write(filepath.Join(f.Repo, ".claude/rules/team.md"), "edited")
	before := testkit.Fingerprint(f.Base)
	run(t, f, Options{})
	after := testkit.Fingerprint(f.Base)
	for p, v := range before {
		if after[p] != v {
			t.Errorf("changed: %s\n  %s\n  %s", p, v, after[p])
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Errorf("created: %s", p)
		}
	}
}

func TestScanDoesNotRunTheRepoFSMonitor(t *testing.T) {
	f := synced(t)
	marker := filepath.Join(f.Base, "fsmonitor-ran")
	hook := filepath.Join(f.Base, "fsmonitor.sh")
	f.Executable(hook, "#!/bin/sh\necho ran >> "+marker+"\nexit 1\n")
	f.Git(f.Repo, "config", "core.fsmonitor", hook)
	run(t, f, Options{})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("scan ran the repo's fsmonitor hook")
	}
}

func TestAHookNamingAFIFODoesNotHangTheScan(t *testing.T) {
	f := synced(t)
	fifo := filepath.Join(f.Base, "hooks/pipe")
	os.MkdirAll(filepath.Dir(fifo), 0o755)
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	f.Write(filepath.Join(f.Home, ".claude/settings.json"), `{"hooks": {"Stop": [{"hooks": [
  {"type": "command", "command": "`+fifo+`"}, {"type": "command", "command": "sh `+fifo+`"}]}]}}`)
	done := make(chan *Report, 1)
	go func() { done <- run(t, f, Options{Budget: 3 * time.Second}) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("scan hung on a FIFO")
	}
}

func TestACheckThatRunsOutOfTimeIsReported(t *testing.T) {
	f := synced(t)
	ak := filepath.Join(f.Base, "bin/ak-slow")
	f.Executable(ak, "#!/bin/sh\nsleep 5\n")
	t.Setenv("ACP_AK", ak)
	start := time.Now()
	r := run(t, f, Options{Module: "agentkit", Budget: 500 * time.Millisecond})
	if !contains(codes(r), "check_timed_out") {
		t.Fatalf("codes %v", codes(r))
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("scan waited %s for a slow check", time.Since(start))
	}
}

func contains(list []string, s string) bool {
	for _, it := range list {
		if it == s {
			return true
		}
	}
	return false
}

// A link an older acp made to the main checkout's agent memory shows in git status; the fix scan
// offers (acp link) removes it, so the finding goes away.
func TestScanFixDropsOldStateLinks(t *testing.T) {
	f := synced(t)
	f.Write(filepath.Join(f.Repo, ".claude/agent-memory/m.md"), "main's")
	f.Symlink(filepath.Join(f.Repo, ".claude/agent-memory"), filepath.Join(f.WT, ".claude/agent-memory"))
	r := run(t, f, Options{})
	var fix *Fix
	for _, fd := range r.Findings {
		if fd.Code == "untracked_acp_links" {
			fix = fd.Fix
		}
	}
	if fix == nil || fix.Kind != "link" {
		t.Fatalf("codes %v", codes(r))
	}
	root := &core.Root{Dir: f.CP}
	p, _ := root.ByID("demo")
	if err := core.NewEngine(root).SyncCheckout(p, f.Repo, f.WT, core.NewLog(""), core.LinkOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := codes(run(t, f, Options{})); contains(got, "untracked_acp_links") || contains(got, "worktree_unlinked") {
		t.Fatalf("after acp link: %v", got)
	}
}
