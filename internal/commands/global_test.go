package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thieung/acp/internal/testkit"
)

func TestGlobalLinksAndPrunes(t *testing.T) {
	f := testkit.New(t)
	f.Write(filepath.Join(f.CP, "global-skills.json"), `{"skills": [{"name": "herdr", "runtimes": ["claude"]}]}`)
	stale := filepath.Join(f.Home, ".codex/skills/herdr")
	f.Symlink(filepath.Join(f.CP, "skills/herdr"), stale)
	if r := acp(t, f, "", "global", "--check"); r.code != 1 {
		t.Fatalf("--check before apply: %d\n%s", r.code, r.stdout)
	}
	if !f.IsLink(stale) || f.Exists(filepath.Join(f.Home, ".claude/skills/herdr")) {
		t.Fatal("--check wrote")
	}
	mustOK(t, acp(t, f, "", "global"))
	if !f.IsLink(filepath.Join(f.Home, ".claude/skills/herdr")) || f.IsLink(stale) {
		t.Fatal("global did not link or prune")
	}
	mustOK(t, acp(t, f, "", "global", "--check"))
}

func TestGlobalLeavesForeignEntriesAlone(t *testing.T) {
	f := testkit.New(t)
	f.Write(filepath.Join(f.CP, "global-skills.json"), `{"skills": [{"name": "herdr", "runtimes": ["claude"]},
  {"name": "gone", "runtimes": ["claude"]}]}`)
	own := filepath.Join(f.Home, ".claude/skills/herdr/SKILL.md")
	f.Write(own, "mine")
	foreign := filepath.Join(f.Home, ".claude/skills/other")
	f.Symlink(filepath.Join(f.Base, "elsewhere"), foreign)
	if r := acp(t, f, "", "global"); r.code != 1 {
		t.Fatalf("exit %d\n%s", r.code, r.stdout)
	}
	if f.Read(own) != "mine" || !f.IsLink(foreign) {
		t.Fatal("global replaced a real folder or a foreign link")
	}
}

func TestGlobalWithASharedPiHomeIsStable(t *testing.T) {
	f := testkit.New(t)
	shared := filepath.Join(f.Home, ".shared-agent")
	t.Setenv("PI_CODING_AGENT_DIR", shared)
	f.Write(filepath.Join(f.CP, "skills/omp-only/SKILL.md"), "---\nname: omp-only\n---\n")
	f.Write(filepath.Join(f.CP, "global-skills.json"), `{"skills": [{"name": "herdr", "runtimes": ["pi"]},
  {"name": "omp-only", "runtimes": ["omp"]}]}`)
	mustOK(t, acp(t, f, "", "global"))
	for _, name := range []string{"herdr", "omp-only"} {
		if !f.IsLink(filepath.Join(shared, "skills", name)) {
			t.Fatalf("%s missing from the shared home", name)
		}
	}
	mustOK(t, acp(t, f, "", "global", "--check"))
	mustOK(t, acp(t, f, "", "global"))
	entries, _ := os.ReadDir(filepath.Join(shared, "skills"))
	if len(entries) != 2 {
		t.Fatalf("shared home has %d entries", len(entries))
	}
}
