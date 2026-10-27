package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thieung/acp/internal/testkit"
)

func TestAddRegistersTheMainCheckoutFromAWorktree(t *testing.T) {
	f := testkit.New(t)
	os.Remove(filepath.Join(f.CP, "projects/demo.json"))
	f.Overlay(map[string]string{})
	mustOK(t, acp(t, f, "", "add", f.WT, "--id", "fresh", "--no-sync"))
	repos := readMap(t, filepath.Join(f.CP, "projects.local.json"))["repos"].(map[string]any)
	if repos["fresh"] != f.Repo {
		t.Fatalf("overlay: %v", repos)
	}
	m := readMap(t, filepath.Join(f.CP, "projects/fresh.json"))
	if m["id"] != "fresh" || m["agentkit"].(map[string]any)["kit"] != "engineer" {
		t.Fatalf("manifest: %v", m)
	}
	if r := acp(t, f, "", "add", f.Repo, "--id", "fresh", "--no-sync"); r.code != 1 || !strings.Contains(r.stderr, "already in the fleet") {
		t.Fatalf("second add: %d %q", r.code, r.stderr)
	}
}

func TestAddDryRunAndSync(t *testing.T) {
	f := testkit.New(t)
	other, _ := addProject(t, f, "other")
	os.Remove(filepath.Join(f.CP, "projects/other.json"))
	f.Overlay(map[string]string{"demo": f.Repo})
	r := mustOK(t, acp(t, f, "", "add", other, "--dry-run"))
	if !strings.Contains(r.stdout, "would add other -> "+other) || f.Exists(filepath.Join(f.CP, "projects/other.json")) {
		t.Fatalf("dry run:\n%s", r.stdout)
	}
	mustOK(t, acp(t, f, "", "add", other, "--no-agentkit"))
	if m := readMap(t, filepath.Join(f.CP, "projects/other.json")); m["agentkit"] != nil {
		t.Fatalf("--no-agentkit wrote %v", m)
	}
}

func TestAddRejectsUnsafePathsAndIds(t *testing.T) {
	f := testkit.New(t)
	for _, args := range [][]string{
		{"add", f.Repo, "--id", "../x"},
		{"add", "/"},
		{"add", f.Home},
		{"add", filepath.Join(f.CP, "skills")},
		{"add", filepath.Join(f.Base, "missing")},
	} {
		if r := acp(t, f, "", append(args, "--no-sync")...); r.code == 0 {
			t.Errorf("%v was accepted:\n%s", args, r.stdout)
		}
	}
	if f.Exists(filepath.Join(f.Base, "x.json")) {
		t.Fatal("an id with .. wrote outside projects/")
	}
}

func TestRemoveIsADryRunUntilYes(t *testing.T) {
	f := testkit.New(t)
	mustOK(t, acp(t, f, "", "sync", "--all"))
	r := mustOK(t, acp(t, f, "", "remove", "demo"))
	if !strings.Contains(r.stdout, "dry run: pass --yes to remove") || !f.Exists(filepath.Join(f.CP, "projects/demo.json")) {
		t.Fatalf("dry run:\n%s", r.stdout)
	}
	if !f.IsLink(filepath.Join(f.WT, ".claude/skills/ak-cook")) {
		t.Fatal("dry run removed a link")
	}
	mustOK(t, acp(t, f, "", "remove", "demo", "--yes"))
	if f.Exists(filepath.Join(f.CP, "projects/demo.json")) || f.Exists(filepath.Join(f.Repo, ".claude/skills/herdr")) ||
		f.Exists(filepath.Join(f.WT, ".claude/skills/ak-cook")) {
		t.Fatal("remove left the manifest or acp's links")
	}
	if !f.IsDir(filepath.Join(f.Repo, ".claude/skills/ak-cook")) {
		t.Fatal("native content stays")
	}
	if repos := readMap(t, filepath.Join(f.CP, "projects.local.json"))["repos"].(map[string]any); repos["demo"] != nil {
		t.Fatalf("overlay: %v", repos)
	}
	if s := f.Status(f.WT); s != "" {
		t.Fatalf("worktree status after remove:\n%s", s)
	}
}

func TestRemoveOfANestedFolderLeavesTheParentWorktreesAlone(t *testing.T) {
	f := testkit.New(t)
	mustOK(t, acp(t, f, "", "sync", "--all"))
	nested := filepath.Join(f.Repo, "tools/maintainer")
	f.Symlink(filepath.Join(f.CP, "skills/herdr"), filepath.Join(nested, ".agents/skills/herdr"))
	f.Overlay(map[string]string{"demo": f.Repo, "nested": nested})
	mustOK(t, acp(t, f, "", "remove", "nested", "--yes"))
	if f.IsLink(filepath.Join(nested, ".agents/skills/herdr")) {
		t.Fatal("nested link stayed")
	}
	if !f.IsLink(filepath.Join(f.WT, ".claude/skills/herdr")) {
		t.Fatal("parent repo worktree touched")
	}
}

func TestRemoveRejectsBadIdsAndKeepsSharedRepos(t *testing.T) {
	f := testkit.New(t)
	if r := acp(t, f, "", "remove", "../x", "--yes"); r.code == 0 {
		t.Fatal("remove ../x was accepted")
	}
	if r := acp(t, f, "", "remove", "nope"); r.code != 1 || !strings.Contains(r.stderr, "no project 'nope'") {
		t.Fatalf("unknown id: %d %q", r.code, r.stderr)
	}
	mustOK(t, acp(t, f, "", "sync", "--all"))
	f.Overlay(map[string]string{"demo": f.Repo, "alias": f.Repo})
	r := mustOK(t, acp(t, f, "", "remove", "alias", "--yes"))
	if !strings.Contains(r.stdout, "also used by another project id") || !f.IsLink(filepath.Join(f.Repo, ".claude/skills/herdr")) {
		t.Fatalf("shared repo lost its links:\n%s", r.stdout)
	}
}
