package core

import (
	"path/filepath"
	"strings"
	"testing"
)

// twoKits gives the demo project a second kit, marketing, pinned next to engineer.
func (f *fixture) twoKits() {
	f.write(filepath.Join(f.cp, "agentkit-releases.lock.json"),
		`{"schemaVersion": 2, "channels": {"beta": {"engineer": "3.0.0-beta.36", "marketing": "3.0.0-beta.36"}}}`)
	f.manifest(`{"id": "demo", "skills": [], "agentkit": {"kit": "engineer", "channel": "beta",
  "targets": ["claude-code", "codex"], "skills": "all", "extra_kits": [{"kit": "marketing", "skills": "all"}]}}`)
}

func changes(logs []*Log) string {
	var out []string
	for _, l := range logs {
		out = append(out, l.Changes...)
	}
	return strings.Join(out, "\n")
}

func TestKitsListsThePrimaryKitFirst(t *testing.T) {
	ak, _ := ParseJSON([]byte(`{"kit": "engineer", "skills": ["ak-cook"], "exclude": ["ak-plan"], "extra_kits": [
  {"kit": "marketing", "exclude": ["mk-seo"]}, {"kit": "engineer"}, {"kit": "../x"}, "design", {"kit": "marketing"},
  {"kit": "design", "skills": ["d-one"]}]}`))
	p := &Project{ID: "x", Manifest: Obj("agentkit", ak)}
	kits := p.Kits()
	var names []string
	for _, k := range kits {
		names = append(names, k.Name)
	}
	if got := strings.Join(names, ","); got != "engineer,marketing,design" {
		t.Fatalf("kits %s", got)
	}
	if !kits[0].Primary || kits[1].Primary || kits[0].All || kits[0].Skills[0] != "ak-cook" || kits[0].Exclude[0] != "ak-plan" {
		t.Fatalf("primary %+v", kits[0])
	}
	if !kits[1].All || kits[1].Exclude[0] != "mk-seo" || kits[2].All || kits[2].Skills[0] != "d-one" {
		t.Fatalf("extras %+v %+v", kits[1], kits[2])
	}
	if !p.UsesKit("design") || p.UsesKit("sales") {
		t.Fatal("UsesKit")
	}
	bad := strings.Join(p.BadExtraKits(), "\n")
	for _, want := range []string{`[1] repeats kit "engineer"`, `[2] has no valid "kit"`, `[3] has no valid "kit"`,
		`[4] repeats kit "marketing"`} {
		if !strings.Contains(bad, want) {
			t.Errorf("BadExtraKits lacks %s:\n%s", want, bad)
		}
	}
	if (&Project{ID: "y", Manifest: NewObject()}).Kits() != nil {
		t.Fatal("a project without AgentKit has kits")
	}
}

func TestNativeSyncsEveryKitThePrimaryFirst(t *testing.T) {
	f := newFixture(t)
	f.twoKits()
	logs := f.syncProject(SyncOpts{Native: true})
	if s := issues(logs); s != "" {
		t.Fatalf("issues: %s", s)
	}
	want := "kit init engineer,kit init marketing,kit init engineer,kit init marketing,kit refresh engineer"
	if got := strings.Join(f.akCalls(), ","); got != want {
		t.Fatalf("ak calls: %s", got)
	}
	for _, p := range []string{".claude/skills/ak-cook", ".claude/skills/marketing-brief", ".agents/skills/marketing-brief"} {
		if !isRealDir(filepath.Join(f.repo, p)) {
			t.Errorf("missing %s", p)
		}
	}
	// Claude Code: the first kit keeps the shared file; Codex: the primary kit wrote last.
	if got := f.Read(filepath.Join(f.repo, ".claude/agents/planner.md")); got != "planner" {
		t.Errorf("planner.md %q", got)
	}
	if got := f.Read(filepath.Join(f.repo, ".codex/agents/planner.toml")); got != "engineer" {
		t.Errorf("planner.toml %q", got)
	}
	logs = f.syncProject(SyncOpts{Native: true})
	if s := issues(logs) + changes(logs); s != "" {
		t.Fatalf("second sync: %s", s)
	}
	if n := len(f.akCalls()); n != 5 {
		t.Fatalf("second sync ran ak: %v", f.akCalls())
	}
}

func TestNativeReportsAMissingExtraKitPin(t *testing.T) {
	f := newFixture(t)
	f.twoKits()
	f.write(filepath.Join(f.cp, "agentkit-releases.lock.json"),
		`{"schemaVersion": 2, "channels": {"beta": {"engineer": "3.0.0-beta.36"}}}`)
	logs := f.syncProject(SyncOpts{Native: true})
	if s := issues(logs); !strings.Contains(s, "no beta/marketing pin") {
		t.Fatalf("issues: %s", s)
	}
	if got := strings.Join(f.akCalls(), ","); got != "kit init engineer,kit init engineer" {
		t.Fatalf("the primary kit was not synced alone: %s", got)
	}
}

func TestRemoveKitsUninstallsThenRefreshesThePrimaryKit(t *testing.T) {
	f := newFixture(t)
	f.twoKits()
	if s := issues(f.syncProject(SyncOpts{Native: true})); s != "" {
		t.Fatal(s)
	}
	f.manifest(`{"id": "demo", "skills": [], "agentkit": {"kit": "engineer", "channel": "beta",
  "targets": ["claude-code", "codex"], "skills": "all"}}`)
	before := len(f.akCalls())

	logs := f.syncProject(SyncOpts{Native: true, Dry: true, RemoveKits: []string{"marketing"}})
	if c := changes(logs); !strings.Contains(c, "- AgentKit marketing: ak kit uninstall") ||
		!strings.Contains(c, "refresh 3.0.0-beta.36 after the uninstall") {
		t.Fatalf("preview: %s", c)
	}
	if len(f.akCalls()) != before {
		t.Fatal("the preview ran ak")
	}

	logs = f.syncProject(SyncOpts{Native: true, RemoveKits: []string{"marketing"}})
	if s := issues(logs); s != "" {
		t.Fatalf("issues: %s", s)
	}
	if got := strings.Join(f.akCalls()[before:], ","); got != "kit uninstall marketing,kit refresh engineer,kit refresh engineer" {
		t.Fatalf("ak calls: %s", got)
	}
	// The uninstall deleted the files the kits shared; the refresh wrote them back.
	if got := f.Read(filepath.Join(f.repo, ".claude/agents/planner.md")); got != "planner" {
		t.Errorf("planner.md %q", got)
	}
	if got := f.Read(filepath.Join(f.repo, ".codex/agents/planner.toml")); got != "engineer" {
		t.Errorf("planner.toml %q", got)
	}
	if f.Exists(filepath.Join(f.repo, ".agentkit/adapters/codex/marketing")) || f.Exists(filepath.Join(f.repo, ".claude/skills/marketing-brief")) {
		t.Error("marketing is still installed")
	}
	if missing := MissingKitFiles(f.repo, "codex", "engineer"); len(missing) != 0 {
		t.Errorf("engineer misses %v", missing)
	}
}

func TestRemoveKitsKeepsAKitTheManifestStillLists(t *testing.T) {
	f := newFixture(t)
	f.twoKits()
	f.syncProject(SyncOpts{Native: true})
	before := len(f.akCalls())
	logs := f.syncProject(SyncOpts{Native: true, RemoveKits: []string{"marketing"}})
	if s := issues(logs); !strings.Contains(s, "AgentKit marketing is still in the manifest") {
		t.Fatalf("issues: %s", s)
	}
	if len(f.akCalls()) != before {
		t.Fatalf("ak ran: %v", f.akCalls()[before:])
	}
}
