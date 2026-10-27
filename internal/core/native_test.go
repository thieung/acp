package core

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func (f *fixture) syncProject(o SyncOpts) []*Log {
	f.t.Helper()
	return NewEngine(f.root).SyncProject(context.Background(), f.project(), o)
}

func (f *fixture) akCalls() []string {
	data := strings.TrimSpace(f.Read(filepath.Join(f.repo, ".ak-calls")))
	if data == "" {
		return nil
	}
	return strings.Split(data, "\n")
}

func issues(logs []*Log) string {
	var out []string
	for _, l := range logs {
		out = append(out, l.Issues...)
	}
	return strings.Join(out, "\n")
}

func allClean(logs []*Log) bool {
	for _, l := range logs {
		if !clean(l) {
			return false
		}
	}
	return true
}

func TestNativeInstallThenInSync(t *testing.T) {
	f := newFixture(t)
	f.StoreLink()
	logs := f.syncProject(SyncOpts{Native: true})
	if s := issues(logs); s != "" {
		t.Fatalf("issues: %s", s)
	}
	if got := strings.Join(f.akCalls(), ","); got != "kit init engineer,kit init engineer" {
		t.Fatalf("ak calls: %s", got)
	}
	cook := filepath.Join(f.repo, ".claude/skills/ak-cook")
	if !isRealDir(cook) {
		t.Fatal("ak owns skills in the main checkout")
	}
	if f.realpath(filepath.Join(f.wt, ".claude/skills/ak-cook")) != cook {
		t.Fatal("the worktree does not link main's ak-cook")
	}
	backups, _ := filepath.Glob(filepath.Join(f.cp, "runtime/backups/store-links-*.json"))
	if len(backups) != 1 {
		t.Fatalf("store link backup: %v", backups)
	}
	if again := f.syncProject(SyncOpts{Native: true}); !allClean(again) {
		t.Fatalf("second sync: %s", lines(again[0]))
	}
	if len(f.akCalls()) != 2 {
		t.Fatalf("an in-sync project ran ak again: %v", f.akCalls())
	}
}

func TestNativeUpgradePaths(t *testing.T) {
	f := newFixture(t)
	f.write(filepath.Join(f.repo, ".agentkit/adapters/codex/engineer/.agentkit/install-manifest.json"), `{"kit": "engineer"}`)
	f.write(filepath.Join(f.repo, "engineer/.codex/hooks.json"), `{"hooks": {}}`)
	f.symlink(filepath.Join(f.repo, "engineer/.codex/hooks.json"), filepath.Join(f.repo, ".codex/hooks.json"))
	if s := issues(f.syncProject(SyncOpts{Native: true})); s != "" {
		t.Fatal(s)
	}
	hooks := filepath.Join(f.repo, ".codex/hooks.json")
	if f.isLink(hooks) || f.Read(hooks) != `{"hooks": {}}` {
		t.Fatal("hooks.json was not materialized with its content")
	}
	if got := strings.Join(f.akCalls(), ","); got != "kit init engineer,kit init engineer --fresh" {
		t.Fatalf("ak calls: %s", got)
	}
	f.write(filepath.Join(f.cp, "agentkit-releases.lock.json"),
		`{"schemaVersion": 2, "channels": {"beta": {"engineer": "3.0.0-beta.40"}}}`)
	f.syncProject(SyncOpts{Native: true})
	if got := strings.Join(f.akCalls()[2:], ","); got != "kit refresh engineer,kit refresh engineer" {
		t.Fatalf("after a pin bump: %s", got)
	}
}

func TestNativeDropsBrokenLegacyHooksLink(t *testing.T) {
	f := newFixture(t)
	f.symlink(filepath.Join(f.repo, "engineer/.codex/hooks.json"), filepath.Join(f.repo, ".codex/hooks.json"))
	f.syncProject(SyncOpts{Native: true})
	if f.isLink(filepath.Join(f.repo, ".codex/hooks.json")) {
		t.Fatal("broken legacy hooks link stayed")
	}
}

func TestNativeFailureRestoresStoreLinks(t *testing.T) {
	f := newFixture(t)
	store := f.StoreLink()
	fail := filepath.Join(f.base, "ak-fail")
	f.Executable(fail, "#!/bin/sh\necho boom >&2\nexit 1\n")
	t.Setenv("ACP_AK", fail)
	logs := f.syncProject(SyncOpts{Native: true})
	if !strings.Contains(issues(logs), "boom") {
		t.Fatalf("issues: %s", issues(logs))
	}
	if f.realpath(filepath.Join(f.repo, ".claude/skills/ak-cook")) != store {
		t.Fatal("the store link was not restored")
	}
}

func TestNativeMissingAkChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.StoreLink()
	t.Setenv("ACP_AK", filepath.Join(f.base, "no-such-ak"))
	t.Setenv("PATH", "/nonexistent")
	logs := f.syncProject(SyncOpts{Native: true})
	if !strings.Contains(issues(logs), "ak is not installed") {
		t.Fatalf("issues: %s", issues(logs))
	}
	if !f.isLink(filepath.Join(f.repo, ".claude/skills/ak-cook")) {
		t.Fatal("a store link was removed although ak is missing")
	}
}

func TestNativeTimeoutKillsAkAndRestores(t *testing.T) {
	f := newFixture(t)
	store := f.StoreLink()
	pidFile := filepath.Join(f.base, "child.pid")
	hang := filepath.Join(f.base, "ak-hang")
	f.Executable(hang, "#!/bin/sh\n[ \"$1\" = warm ] && exit 0\nsleep 30 &\necho $! > "+pidFile+"\nsleep 30\n")
	// macOS checks a new executable on its first run; do that before the timed run.
	Exec(context.Background(), ExecOpts{Timeout: 10 * time.Second}, hang, "warm")
	t.Setenv("ACP_AK", hang)
	t.Setenv("ACP_AK_TIMEOUT", "1s")
	start := time.Now()
	logs := f.syncProject(SyncOpts{Native: true})
	if time.Since(start) > 10*time.Second {
		t.Fatalf("sync took %v", time.Since(start))
	}
	if !strings.Contains(issues(logs), "timed out") {
		t.Fatalf("issues: %s", issues(logs))
	}
	if f.realpath(filepath.Join(f.repo, ".claude/skills/ak-cook")) != store {
		t.Fatal("the store link was not restored after the timeout")
	}
	data := strings.TrimSpace(f.Read(pidFile))
	pid := 0
	for _, c := range data {
		pid = pid*10 + int(c-'0')
	}
	if pid == 0 {
		t.Fatal("the fake ak never ran")
	}
	for i := 0; syscall.Kill(pid, 0) == nil; i++ {
		if i > 20 {
			t.Fatalf("ak's child %d is still alive", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestNativeRestoreReportsAnOccupiedPath(t *testing.T) {
	f := newFixture(t)
	store := f.StoreLink()
	occupy := filepath.Join(f.base, "ak-occupy")
	f.Executable(occupy, "#!/bin/sh\nmkdir -p .claude/skills/ak-cook\necho half-done >&2\nexit 1\n")
	t.Setenv("ACP_AK", occupy)
	logs := f.syncProject(SyncOpts{Native: true})
	if !strings.Contains(issues(logs), store) {
		t.Fatalf("the issue must name the saved target %s: %s", store, issues(logs))
	}
}

func TestSelectionMatches(t *testing.T) {
	f := newFixture(t)
	f.write(filepath.Join(f.repo, ".agentkit/adapters/claude-code/engineer/.agentkit/install-manifest.json"),
		`{"skill_selection": {"mode": "list", "skills": ["ak-cook", "ak-fix", "ak-plan"]}}`)
	e := NewEngine(f.root)
	all := Obj("skill_selection", Obj("mode", "all"))
	list := func(names ...string) *Object {
		return Obj("skill_selection", Obj("mode", "list", "skills", AnyList(names)))
	}
	cases := []struct {
		ak       string
		recorded *Object
		want     bool
	}{
		{`{"skills": "all"}`, all, true},
		{`{"skills": "all"}`, list("ak-cook"), false},
		{`{"skills": ["ak-fix", "ak-cook"]}`, list("ak-cook", "ak-fix"), true},
		{`{"skills": ["ak-fix"]}`, list("ak-cook", "ak-fix"), false},
		{`{"skills": "all", "exclude": ["ak-plan"]}`, list("ak-cook", "ak-fix"), true},
		{`{"skills": "all", "exclude": ["ak-plan"]}`, all, false},
		{`{"skills": "all", "exclude": ["ak-plan"]}`, list("ak-cook", "ak-plan"), false},
	}
	for _, c := range cases {
		ak, _ := ParseJSON([]byte(c.ak))
		p := &Project{ID: "x", Manifest: Obj("agentkit", ak)}
		if got := e.SelectionMatches(p, c.recorded); got != c.want {
			t.Errorf("%s vs %v: got %v", c.ak, c.recorded, got)
		}
	}
}
