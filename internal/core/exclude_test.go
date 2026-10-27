package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExcludeHidesOnlyThisWorktreesLinks(t *testing.T) {
	f := newFixture(t)
	f.native()
	f.link(f.repo, LinkOpts{})
	wt2 := filepath.Join(f.base, "wt2")
	f.git(f.repo, "worktree", "add", "-q", "-b", "second", wt2)
	mainBefore := f.status(f.repo)
	f.link(f.wt, LinkOpts{})
	f.link(wt2, LinkOpts{})
	if s := f.status(f.wt); s != "" {
		t.Fatalf("worktree shows acp links:\n%s", s)
	}
	if f.status(f.repo) != mainBefore {
		t.Fatalf("main status changed:\n%s\n->\n%s", mainBefore, f.status(f.repo))
	}
	f.write(filepath.Join(f.wt, ".claude/notes.md"), "mine")
	if s := f.status(f.wt); !strings.Contains(s, "?? .claude/notes.md") {
		t.Fatalf("a real new file must stay visible:\n%s", s)
	}
	f.write(filepath.Join(f.wt, "plans/x.md"), "plan")
	if s := f.status(f.wt); strings.Contains(s, "plans/") {
		t.Fatalf("the global ignore no longer applies:\n%s", s)
	}
	os.Remove(filepath.Join(f.wt, ".claude/notes.md"))
	f.unlink(f.wt)
	if s := f.status(wt2); s != "" {
		t.Fatalf("unlinking one worktree changed the other:\n%s", s)
	}
}

func TestWorktreeAddedFromALinkedWorktreeInheritsNothing(t *testing.T) {
	f := newFixture(t)
	f.native()
	f.link(f.wt, LinkOpts{})
	child := filepath.Join(f.base, "child")
	f.git(f.wt, "worktree", "add", "-q", "-b", "child", child)
	f.write(filepath.Join(child, ".claude/agents/a.md"), "user work")
	f.write(filepath.Join(child, "plans/x.md"), "plan")
	s := f.status(child)
	if !strings.Contains(s, "?? .claude/agents/a.md") {
		t.Fatalf("a real file in a worktree acp never ran in is hidden:\n%s", s)
	}
	if strings.Contains(s, "plans/") {
		t.Fatalf("the global ignore no longer applies:\n%s", s)
	}
}

func TestARecreatedWorktreeDoesNotReuseTheOldExclude(t *testing.T) {
	f := newFixture(t)
	f.native()
	f.link(f.wt, LinkOpts{})
	f.git(f.repo, "worktree", "remove", "--force", f.wt)
	f.git(f.repo, "worktree", "add", "-q", "-b", "again", f.wt)
	f.write(filepath.Join(f.wt, ".claude/agents/a.md"), "user work")
	if s := f.status(f.wt); !strings.Contains(s, "?? .claude/agents/a.md") {
		t.Fatalf("a new worktree with the old name hides real files:\n%s", s)
	}
	f.link(f.wt, LinkOpts{})
	if out := f.git(f.repo, "config", "--local", "--get-regexp", `^includeif\.`); strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("stale includes are kept:\n%s", out)
	}
}

func TestGlobalIgnoreSetThroughAnIncludeIsKept(t *testing.T) {
	f := newFixture(t)
	f.native()
	ignore := filepath.Join(f.base, "global-ignore")
	f.write(filepath.Join(f.base, "included.gitconfig"), "[core]\n\texcludesFile = "+ignore+"\n")
	f.write(filepath.Join(f.base, "gitconfig"), "[user]\n\tname = t\n\temail = t@t\n[include]\n\tpath = "+
		filepath.Join(f.base, "included.gitconfig")+"\n")
	f.link(f.wt, LinkOpts{})
	f.write(filepath.Join(f.wt, "plans/x.md"), "plan")
	if s := f.status(f.wt); strings.Contains(s, "plans/") {
		t.Fatalf("the global ignore from an include no longer applies:\n%s", s)
	}
}

func TestOldWorktreeLevelSettingIsMigrated(t *testing.T) {
	f := newFixture(t)
	f.native()
	// An earlier build pointed config.worktree at the exclude file; git worktree add copies that file.
	gitDir, _ := GitDir(f.wt)
	f.git(f.repo, "config", "extensions.worktreeConfig", "true")
	f.git(f.wt, "config", "--worktree", "core.excludesFile", ExcludePath(gitDir))
	f.link(f.wt, LinkOpts{})
	if v, _ := GitRead(f.wt, "config", "--worktree", "--get", "core.excludesFile"); v != "" {
		t.Fatalf("worktree-level setting kept: %q", v)
	}
	if s := f.status(f.wt); s != "" {
		t.Fatalf("worktree shows acp links:\n%s", s)
	}
}

func TestExcludeEscapesPatterns(t *testing.T) {
	for rel, want := range map[string]string{
		".claude/agents/a.md": "/.claude/agents/a.md",
		".claude/x*y?[z]":     `/.claude/x\*y\?\[z\]`,
		".claude/#a!b ":       `/.claude/\#a\!b\ `,
	} {
		if got := escapePattern(rel); got != want {
			t.Errorf("escapePattern(%q) = %q, want %q", rel, got, want)
		}
	}
}

func TestMirrorLinkOutsideAnchorsIsRemovedViaLedger(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(f.base, "elsewhere/tool.md")
	f.write(outside, "tool")
	f.symlink(outside, filepath.Join(f.repo, ".claude/commands/tool.md"))
	f.link(f.wt, LinkOpts{})
	link := filepath.Join(f.wt, ".claude/commands/tool.md")
	if f.realpath(link) != outside {
		t.Fatal("the link was not mirrored")
	}
	f.unlink(f.wt)
	if f.isLink(link) {
		t.Fatal("UnlinkAll left a link whose target is outside every anchor")
	}
}

func TestCrashMidMirrorLeavesNothingBehind(t *testing.T) {
	f := newFixture(t)
	f.native()
	for i := 0; i < 6; i++ {
		f.write(filepath.Join(f.repo, ".claude/commands", string(rune('a'+i))+".md"), "x")
	}
	e := NewEngine(f.root)
	n := 0
	crash := errors.New("crash")
	e.Fault = func(string) error {
		if n++; n > 3 {
			return crash
		}
		return nil
	}
	f.linkWith(e, f.wt, LinkOpts{})
	if s := f.status(f.wt); s != "" {
		t.Fatalf("links made before the crash show in git status:\n%s", s)
	}
	f.unlink(f.wt)
	if s := f.status(f.wt); s != "" {
		t.Fatalf("not clean after unlink:\n%s", s)
	}
	if links := (&session{e: NewEngine(f.root), dir: f.wt, main: f.repo}).managedLinks([]string{f.repo, f.cp}); len(links) > 0 {
		t.Fatalf("links left: %v", links)
	}
	if Exists(filepath.Join(f.wt, ".claude/commands")) {
		t.Fatal("folder acp created stayed behind")
	}
}

func TestBackfillAdoptsLinksMadeByPythonAcp(t *testing.T) {
	f := newFixture(t)
	f.native()
	// What the Python acp left: whole-folder links, no ledger, no exclude.
	f.symlink(filepath.Join(f.repo, ".claude/skills"), filepath.Join(f.wt, ".claude/skills"))
	f.symlink(filepath.Join(f.repo, ".claude/agents"), filepath.Join(f.wt, ".claude/agents"))
	if s := f.status(f.wt); s == "" {
		t.Fatal("setup: Python links should show in git status")
	}
	if check := f.link(f.wt, LinkOpts{Dry: true}); clean(check) {
		t.Fatal("a dry run must report links that are not recorded yet")
	}
	f.link(f.wt, LinkOpts{})
	if s := f.status(f.wt); s != "" {
		t.Fatalf("not clean after link:\n%s", s)
	}
	if !f.isLink(filepath.Join(f.wt, ".claude/skills")) {
		t.Fatal("a correct folder link was replaced")
	}
	f.unlink(f.wt)
	if Exists(filepath.Join(f.wt, ".claude/skills")) || Exists(filepath.Join(f.wt, ".claude/agents")) {
		t.Fatal("backfilled links were not removed")
	}
	if !Exists(filepath.Join(f.repo, ".claude/skills/herdr")) && !Exists(filepath.Join(f.repo, ".claude/skills/ak-cook")) {
		t.Fatal("unlink went through a folder link into main")
	}
}

func TestNestedProjectGetsNoLedgerInTheParent(t *testing.T) {
	f := newFixture(t)
	nested := filepath.Join(f.repo, "tools/maint")
	os.MkdirAll(nested, 0o755)
	f.write(filepath.Join(f.cp, "projects/nested.json"),
		`{"id": "nested", "skills": [{"name": "herdr", "runtimes": ["codex"]}]}`)
	p := &Project{ID: "nested", Manifest: Obj("id", "nested", "skills", []any{Obj("name", "herdr", "runtimes", []any{"codex"})})}
	log := NewLog("")
	NewEngine(f.root).SyncCheckout(p, nested, nested, log, LinkOpts{})
	if f.realpath(filepath.Join(nested, ".agents/skills/herdr")) != filepath.Join(f.cp, "skills/herdr") {
		t.Fatalf("no personal link: %s", lines(log))
	}
	gitDir, _ := GitDir(f.repo)
	if out, _ := GitRead(f.repo, "config", "--local", "--get-regexp", `^includeif\.`); Exists(LedgerPath(gitDir)) || out != "" {
		t.Fatal("a nested folder wrote the parent repo's ledger or exclude setting")
	}
	removed, _ := NewEngine(f.root).UnlinkAll(nested, nested, false)
	if len(removed) != 1 || f.isLink(filepath.Join(nested, ".agents/skills/herdr")) {
		t.Fatalf("unlink of a nested folder: %v", removed)
	}
}

func TestAdoptCollisionAndCrossDevice(t *testing.T) {
	f := newFixture(t)
	f.native()
	a, b := filepath.Join(f.base, "x/repo"), filepath.Join(f.base, "y/repo")
	f.git(f.repo, "worktree", "add", "-q", "-b", "a", a)
	f.git(f.repo, "worktree", "add", "-q", "-b", "b", b)
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.Local)
	for i, dir := range []string{a, b} {
		f.write(filepath.Join(dir, ".claude/skills/ak-cook/SKILL.md"), "old "+dir)
		e := NewEngine(f.root)
		e.Now = func() time.Time { return now }
		if i == 1 {
			e.Rename = func(string, string) error { return &os.LinkError{Op: "rename", Err: syscall.EXDEV} }
		}
		if log := f.linkWith(e, dir, LinkOpts{Adopt: true}); len(log.Issues) > 0 {
			t.Fatalf("%s: %v", dir, log.Issues)
		}
		if !f.isLink(filepath.Join(dir, ".claude/skills/ak-cook")) {
			t.Fatalf("%s not adopted", dir)
		}
	}
	backups := findFiles(t, filepath.Join(f.cp, "runtime/backups/worktree-copies"), "SKILL.md")
	if len(backups) != 2 {
		t.Fatalf("backups collided: %v", backups)
	}
}

func TestOldWorktreeLevelSettingOnMainIsRemoved(t *testing.T) {
	f := newFixture(t)
	f.native()
	// An older acp gave main its own exclude; git worktree add copied it into every new worktree.
	gitDir, _ := GitDir(f.repo)
	f.git(f.repo, "config", "extensions.worktreeConfig", "true")
	f.git(f.repo, "config", "--worktree", "core.excludesFile", ExcludePath(gitDir))
	f.link(f.repo, LinkOpts{})
	if v, _ := GitRead(f.repo, "config", "--worktree", "--get", "core.excludesFile"); v != "" {
		t.Fatalf("main kept the worktree-level setting: %q", v)
	}
	fresh := filepath.Join(f.base, "fresh")
	f.git(f.repo, "worktree", "add", "-q", "-b", "fresh", fresh)
	f.write(filepath.Join(fresh, ".claude/agents/mine.md"), "mine")
	if !strings.Contains(f.status(fresh), ".claude/agents/mine.md") {
		t.Fatalf("a real file in a new worktree is hidden:\n%s", f.status(fresh))
	}
}

func TestDeletedWorktreeIncludeIsPrunedWhenThePathHasASpace(t *testing.T) {
	f := newFixture(t)
	f.native()
	spaced := filepath.Join(f.base, "wt one")
	f.git(f.repo, "worktree", "add", "-q", "-b", "spaced", spaced)
	f.link(spaced, LinkOpts{})
	gitDir, _ := GitDir(spaced)
	if !excludeConfigured(spaced, gitDir) {
		t.Fatal("the spaced worktree got no exclude")
	}
	f.git(f.repo, "worktree", "remove", "--force", spaced)
	f.link(f.wt, LinkOpts{})
	out, _ := GitRead(f.repo, "config", "--local", "--get-regexp", `^includeif\.`)
	if strings.Contains(out, "wt one") || strings.Contains(out, "wt-one") {
		t.Fatalf("the removed worktree's include was kept:\n%s", out)
	}
}
