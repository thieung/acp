package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An older acp linked per-checkout state from the main checkout into worktrees; a link pass drops
// those links and leaves a state link that points anywhere else alone.
func TestLinkDropsOldLinksToPerCheckoutState(t *testing.T) {
	f := newFixture(t)
	f.native()
	for _, rel := range []string{".claude/agent-memory/m.md", ".claude/hooks/.logs/x", ".codex/hooks/.agentkit-runtime.json"} {
		f.write(filepath.Join(f.repo, rel), "main's")
	}
	os.MkdirAll(filepath.Join(f.wt, ".claude/hooks"), 0o755)
	os.MkdirAll(filepath.Join(f.wt, ".codex/hooks"), 0o755)
	for _, rel := range []string{".claude/agent-memory", ".claude/hooks/.logs", ".codex/hooks/.agentkit-runtime.json"} {
		f.symlink(filepath.Join(f.repo, rel), filepath.Join(f.wt, rel))
	}
	mine := filepath.Join(f.base, "my-plans")
	os.MkdirAll(mine, 0o755)
	f.symlink(mine, filepath.Join(f.wt, ".claude/plans"))

	log := f.link(f.wt, LinkOpts{Dry: true})
	changes := strings.Join(log.Changes, "\n")
	for _, rel := range []string{".claude/agent-memory", ".claude/hooks/.logs", ".codex/hooks/.agentkit-runtime.json"} {
		if !strings.Contains(changes, "- "+rel+" (per-checkout state, not shared)") {
			t.Errorf("the preview does not drop %s:\n%s", rel, changes)
		}
		if !f.isLink(filepath.Join(f.wt, rel)) {
			t.Fatalf("the preview removed %s", rel)
		}
	}
	if strings.Contains(changes, ".claude/plans") {
		t.Fatalf("the preview touches the user's link:\n%s", changes)
	}

	f.link(f.wt, LinkOpts{})
	for _, rel := range []string{".claude/agent-memory", ".claude/hooks/.logs", ".codex/hooks/.agentkit-runtime.json"} {
		if Exists(filepath.Join(f.wt, rel)) {
			t.Errorf("%s is still there", rel)
		}
		if !Exists(filepath.Join(f.repo, rel)) {
			t.Errorf("main's %s was removed", rel)
		}
	}
	if !f.isLink(filepath.Join(f.wt, ".claude/plans")) {
		t.Fatal("the user's link was removed")
	}
	if s := f.status(f.wt); strings.Contains(s, "agent-memory") || strings.Contains(s, ".logs") {
		t.Fatalf("git status: %s", s)
	}
	if log := f.link(f.wt, LinkOpts{}); len(log.Changes) != 0 {
		t.Fatalf("a second pass changed: %v", log.Changes)
	}
}
