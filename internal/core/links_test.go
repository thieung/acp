package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkPersonalAndWorktree(t *testing.T) {
	f := newFixture(t)
	f.native()
	f.link(f.repo, LinkOpts{})
	herdr := filepath.Join(f.cp, "skills/herdr")
	for _, rel := range []string{".claude/skills/herdr", ".agents/skills/herdr"} {
		if f.realpath(filepath.Join(f.repo, rel)) != herdr {
			t.Errorf("main %s is not linked to the personal skill", rel)
		}
	}
	log := f.link(f.wt, LinkOpts{})
	if len(log.Issues) > 0 {
		t.Fatalf("issues: %v", log.Issues)
	}
	checks := map[string]string{
		".claude/skills/ak-cook": filepath.Join(f.repo, ".claude/skills/ak-cook"),
		".claude/agents":         filepath.Join(f.repo, ".claude/agents"),
		".agents/skills/herdr":   herdr,
		".claude/skills/herdr":   herdr,
	}
	for rel, want := range checks {
		if got := f.realpath(filepath.Join(f.wt, rel)); got != want {
			t.Errorf("worktree %s -> %s, want %s", rel, got, want)
		}
	}
	if f.isLink(filepath.Join(f.wt, ".claude/rules")) {
		t.Error("tracked folders stay the branch's own")
	}
	if Exists(filepath.Join(f.wt, ".claude/settings.local.json")) {
		t.Error("settings.local.json must not be mirrored")
	}
	if f.isLink(filepath.Join(f.wt, ".claude/skills")) {
		t.Error("skills is a container: mirrored child by child")
	}
	if again := f.link(f.wt, LinkOpts{}); !clean(again) {
		t.Fatalf("second link is not in sync: %s", lines(again))
	}
	if check := f.link(f.wt, LinkOpts{Dry: true}); !clean(check) {
		t.Fatalf("check after link: %s", lines(check))
	}
}

func TestLinkCheckWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.native()
	log := f.link(f.wt, LinkOpts{Dry: true})
	if clean(log) {
		t.Fatal("a fresh worktree needs links")
	}
	if Exists(filepath.Join(f.wt, ".claude/skills/herdr")) || Exists(filepath.Join(f.wt, ".claude/agents")) {
		t.Fatal("dry run wrote links")
	}
	gitDir, _ := GitDir(f.wt)
	if Exists(LedgerPath(gitDir)) || Exists(ExcludePath(gitDir)) {
		t.Fatal("dry run wrote the ledger or exclude file")
	}
	for _, c := range log.Changes {
		if strings.Count(strings.Join(log.Changes, "\n"), c) > 1 {
			t.Fatalf("dry run lists %q twice", c)
		}
	}
}

func TestMirrorDropsLinksWhoseSourceDisappeared(t *testing.T) {
	f := newFixture(t)
	f.native()
	os.MkdirAll(filepath.Join(f.repo, ".claude/skills/gone"), 0o755)
	f.link(f.wt, LinkOpts{})
	gone := filepath.Join(f.wt, ".claude/skills/gone")
	if !f.isLink(gone) {
		t.Fatal("gone was not linked")
	}
	os.Remove(filepath.Join(f.repo, ".claude/skills/gone"))
	log := f.link(f.wt, LinkOpts{})
	if f.isLink(gone) {
		t.Fatal("a broken link stayed")
	}
	if !strings.Contains(strings.Join(log.Changes, "\n"), "- .claude/skills/gone (broken)") {
		t.Fatalf("changes: %v", log.Changes)
	}
}

func TestMirrorReportsOldCopyThenAdopts(t *testing.T) {
	f := newFixture(t)
	f.native()
	f.write(filepath.Join(f.wt, ".claude/skills/ak-cook/SKILL.md"), "old 2.x copy")
	log := f.link(f.wt, LinkOpts{})
	if !strings.Contains(strings.Join(log.Issues, "\n"), "shadows the main checkout") {
		t.Fatalf("issues: %v", log.Issues)
	}
	log = f.link(f.wt, LinkOpts{Adopt: true})
	if len(log.Issues) > 0 || !f.isLink(filepath.Join(f.wt, ".claude/skills/ak-cook")) {
		t.Fatalf("adopt: %s", lines(log))
	}
	backups := findFiles(t, filepath.Join(f.cp, "runtime/backups/worktree-copies"), "SKILL.md")
	if len(backups) != 1 || readFile(backups[0]) != "old 2.x copy" {
		t.Fatalf("backups: %v", backups)
	}
}

func TestMirrorNeverAdoptsWorktreeOwnConfig(t *testing.T) {
	f := newFixture(t)
	f.write(filepath.Join(f.repo, ".claude/launch.json"), `{"port": 3000}`)
	f.write(filepath.Join(f.wt, ".claude/launch.json"), `{"port": 4100}`)
	f.link(f.wt, LinkOpts{Adopt: true})
	if readFile(filepath.Join(f.wt, ".claude/launch.json")) != `{"port": 4100}` {
		t.Fatal("the worktree's own config was replaced")
	}
}

func TestMirrorLeavesIdenticalCopyAlone(t *testing.T) {
	f := newFixture(t)
	f.native()
	f.write(filepath.Join(f.wt, ".claude/skills/ak-cook/SKILL.md"), "---\nname: ak-cook\n---\n")
	f.link(f.wt, LinkOpts{})
	if log := f.link(f.wt, LinkOpts{}); !clean(log) {
		t.Fatalf("identical copy: %s", lines(log))
	}
	if f.isLink(filepath.Join(f.wt, ".claude/skills/ak-cook")) {
		t.Fatal("an identical copy was replaced")
	}
}

func TestMirrorSkipsPerCheckoutState(t *testing.T) {
	f := newFixture(t)
	for _, rel := range []string{".claude/scheduled_tasks.lock", ".claude/hooks/.logs/x", ".claude/hooks/start.sh",
		".codex/hooks/.agentkit-runtime.json", ".codex/hooks/run.cjs", ".claude/agent-memory/m.md",
		".claude/.ckignore", ".agentkit/.gitignore", ".claude/plans/p.md", ".codex/config.local.json"} {
		f.write(filepath.Join(f.repo, rel), "x")
	}
	f.link(f.wt, LinkOpts{})
	for _, rel := range []string{".claude/scheduled_tasks.lock", ".claude/hooks/.logs", ".codex/hooks/.agentkit-runtime.json",
		".claude/agent-memory", ".claude/plans", ".codex/config.local.json"} {
		if Exists(filepath.Join(f.wt, rel)) {
			t.Errorf("%s was mirrored", rel)
		}
	}
	for _, rel := range []string{".claude/.ckignore", ".agentkit/.gitignore", ".claude/hooks/start.sh", ".codex/hooks/run.cjs"} {
		if !f.isLink(filepath.Join(f.wt, rel)) {
			t.Errorf("%s was not mirrored", rel)
		}
	}
}

func TestUnlinkAllClearsWorktreeLinksOnly(t *testing.T) {
	f := newFixture(t)
	f.native()
	f.link(f.repo, LinkOpts{})
	f.link(f.wt, LinkOpts{})
	removed := f.unlink(f.wt)
	if len(removed) == 0 || Exists(filepath.Join(f.wt, ".claude/skills/ak-cook")) {
		t.Fatalf("removed %v", removed)
	}
	if !isRealDir(filepath.Join(f.repo, ".claude/skills/ak-cook")) || !f.isLink(filepath.Join(f.repo, ".claude/skills/herdr")) {
		t.Fatal("main was touched")
	}
	if Exists(filepath.Join(f.wt, ".claude/skills")) {
		t.Fatal("the skills folder acp created stayed behind")
	}
	if !Exists(filepath.Join(f.wt, ".claude/rules/team.md")) {
		t.Fatal("tracked content was removed")
	}
	gitDir, _ := GitDir(f.wt)
	if _, err := GitRead(f.wt, "config", "--local", "--get", includeSection(gitDir)+".path"); Exists(LedgerPath(gitDir)) || Exists(ExcludePath(gitDir)) || Exists(excludeConfigPath(gitDir)) || err == nil {
		t.Fatal("ledger, exclude file or worktree setting left behind")
	}
	if s := f.status(f.wt); s != "" {
		t.Fatalf("worktree not clean after unlink:\n%s", s)
	}
}

func TestTrackedStoreLinkIsNeverTouched(t *testing.T) {
	f := newFixture(t)
	// main tracks .agents/skills/ak-cook as a link into a store that no longer exists.
	f.symlink(filepath.Join(f.cp, "runtime/agentkit-store/beta/ak-cook"), filepath.Join(f.repo, ".agents/skills/ak-cook"))
	f.git(f.repo, "add", ".agents")
	f.git(f.repo, "commit", "-qm", "store link")
	f.git(f.wt, "merge", "-q", "main")
	mainBefore, wtBefore := f.status(f.repo), f.status(f.wt)
	f.link(f.repo, LinkOpts{})
	f.link(f.wt, LinkOpts{})
	f.unlink(f.wt)
	f.unlink(f.repo)
	for _, dir := range []string{f.repo, f.wt} {
		if !f.isLink(filepath.Join(dir, ".agents/skills/ak-cook")) {
			t.Fatalf("%s: the tracked link was removed", dir)
		}
	}
	if f.status(f.repo) != mainBefore || f.status(f.wt) != wtBefore {
		t.Fatalf("git status changed:\nmain %q -> %q\nwt %q -> %q", mainBefore, f.status(f.repo), wtBefore, f.status(f.wt))
	}
}

func TestUnreadableIndexStopsBeforeTouchingTrackedLinks(t *testing.T) {
	f := newFixture(t)
	f.symlink(filepath.Join(f.cp, "runtime/agentkit-store/beta/ak-cook"), filepath.Join(f.repo, ".agents/skills/ak-cook"))
	f.git(f.repo, "add", ".agents")
	f.git(f.repo, "commit", "-qm", "store link")
	f.git(f.wt, "merge", "-q", "main")
	gitDir, _ := GitDir(f.wt)
	f.write(filepath.Join(gitDir, "index"), "garbage")
	log := f.link(f.wt, LinkOpts{})
	if len(log.Issues) == 0 {
		t.Fatalf("no issue reported: %s", lines(log))
	}
	if _, err := NewEngine(f.root).UnlinkAll(f.repo, f.wt, false); err == nil {
		t.Fatal("unlink went ahead without knowing the tracked paths")
	}
	if !f.isLink(filepath.Join(f.wt, ".agents/skills/ak-cook")) {
		t.Fatal("the tracked link was removed")
	}
}

func TestNestedWorktreeKeepsItsOwnLinks(t *testing.T) {
	f := newFixture(t)
	f.native()
	nested := filepath.Join(f.repo, ".claude/worktrees/wt2")
	f.git(f.repo, "worktree", "add", "-q", "-b", "nested", nested)
	// A tracked relative link into the worktree itself, and an untracked one that is broken.
	f.write(filepath.Join(nested, "docs/guide.md"), "guide")
	f.symlink("../../docs/guide.md", filepath.Join(nested, ".claude/commands/guide.md"))
	f.git(nested, "add", "docs", ".claude/commands")
	f.git(nested, "commit", "-qm", "own link")
	f.symlink(filepath.Join(nested, "docs/missing.md"), filepath.Join(nested, ".claude/own-broken.md"))
	f.link(nested, LinkOpts{})
	f.link(f.repo, LinkOpts{})
	f.unlink(nested)
	f.unlink(f.repo)
	if !f.isLink(filepath.Join(nested, ".claude/commands/guide.md")) {
		t.Fatal("the worktree's tracked link was removed")
	}
	if !f.isLink(filepath.Join(nested, ".claude/own-broken.md")) {
		t.Fatal("the worktree's own link was treated as acp's")
	}
}

func TestPersonalSkillNamedLikeATrackedLinkIsReported(t *testing.T) {
	f := newFixture(t)
	f.symlink("../../docs/herdr", filepath.Join(f.repo, ".claude/skills/herdr"))
	f.git(f.repo, "add", ".claude/skills/herdr")
	f.git(f.repo, "commit", "-qm", "own herdr")
	log := f.link(f.repo, LinkOpts{})
	if !strings.Contains(strings.Join(log.Issues, "\n"), ".claude/skills/herdr is tracked by git") {
		t.Fatalf("issues: %v", log.Issues)
	}
	if dest, _ := os.Readlink(filepath.Join(f.repo, ".claude/skills/herdr")); dest != "../../docs/herdr" {
		t.Fatalf("tracked link replaced with %q", dest)
	}
}

func findFiles(t *testing.T, dir, name string) []string {
	var out []string
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == name {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func TestUnreadableIndexStopsANestedProjectToo(t *testing.T) {
	f := newFixture(t)
	f.symlink(filepath.Join(f.cp, "runtime/agentkit-store/beta/ak-cook"), filepath.Join(f.repo, "pkg/.agents/skills/ak-cook"))
	f.git(f.repo, "add", "pkg")
	f.git(f.repo, "commit", "-qm", "nested project with a store link")
	f.git(f.wt, "merge", "-q", "main")
	gitDir, _ := GitDir(f.wt)
	f.write(filepath.Join(gitDir, "index"), "garbage")
	if _, err := NewEngine(f.root).UnlinkAll(filepath.Join(f.repo, "pkg"), filepath.Join(f.wt, "pkg"), false); err == nil {
		t.Fatal("unlink went ahead without knowing the tracked paths")
	}
	if !f.isLink(filepath.Join(f.wt, "pkg/.agents/skills/ak-cook")) {
		t.Fatal("the tracked link was removed")
	}
}
