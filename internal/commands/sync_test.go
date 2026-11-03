package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thieung/acp/internal/core"
	"github.com/thieung/acp/internal/testkit"
)

func TestSyncAllInstallsLinksAndDoctorPasses(t *testing.T) {
	f := testkit.New(t)
	f.StoreLink()
	out := mustOK(t, acp(t, f, "", "sync", "--all"))
	if !strings.Contains(out.stdout, "demo") {
		t.Fatalf("stdout:\n%s", out.stdout)
	}
	cook := filepath.Join(f.Repo, ".claude/skills/ak-cook")
	if !f.IsDir(cook) {
		t.Fatal("ak owns skills in the main checkout")
	}
	herdr := filepath.Join(f.CP, "skills/herdr")
	for path, want := range map[string]string{
		filepath.Join(f.Repo, ".claude/skills/herdr"): herdr, filepath.Join(f.Repo, ".agents/skills/herdr"): herdr,
		filepath.Join(f.WT, ".claude/skills/ak-cook"): cook, filepath.Join(f.WT, ".agents/skills/herdr"): herdr,
		filepath.Join(f.WT, ".claude/agents"): filepath.Join(f.Repo, ".claude/agents"),
	} {
		if f.RealPath(path) != want {
			t.Errorf("%s -> %s, want %s", path, f.RealPath(path), want)
		}
	}
	if f.IsLink(filepath.Join(f.WT, ".claude/rules")) || f.Exists(filepath.Join(f.WT, ".claude/settings.local.json")) {
		t.Fatal("tracked folders stay the branch's own; settings.local.json is not mirrored")
	}
	if s := f.Status(f.WT); s != "" {
		t.Fatalf("worktree git status shows acp links:\n%s", s)
	}
	if r := acp(t, f, "", "doctor"); r.code != 0 {
		t.Fatalf("doctor after sync: %d\n%s", r.code, r.stdout)
	}
	if again := mustOK(t, acp(t, f, "", "sync", "--all")); !strings.Contains(again.stdout, "in sync") {
		t.Fatalf("second sync:\n%s", again.stdout)
	}
}

func TestSyncTheProjectYouAreIn(t *testing.T) {
	f := testkit.New(t)
	mustOK(t, acpIn(t, filepath.Join(f.WT, ".claude"), "", "sync", "--offline"))
	if !f.IsLink(filepath.Join(f.WT, ".claude/skills/herdr")) {
		t.Fatal("sync from a worktree did not sync its project")
	}
	r := acp(t, f, "", "sync")
	if r.code != 1 || !strings.Contains(r.stderr, "this directory is not in the fleet") {
		t.Fatalf("outside the fleet: %d %q", r.code, r.stderr)
	}
}

func TestSyncDryRunWritesNothing(t *testing.T) {
	f := testkit.New(t)
	r := mustOK(t, acp(t, f, "", "sync", "--all", "--dry-run"))
	if !strings.Contains(r.stdout, "dry run: nothing was written") || !strings.Contains(r.stdout, "+ .claude/skills/herdr") {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
	if f.Exists(filepath.Join(f.Repo, ".claude/skills/herdr")) || f.Exists(filepath.Join(f.Repo, ".ak-calls")) {
		t.Fatal("dry run changed the repo")
	}
}

func TestSyncWithoutAkReportsEachProjectAndKeepsGoing(t *testing.T) {
	f := testkit.New(t)
	store := f.StoreLink()
	other, _ := addProject(t, f, "other")
	t.Setenv("ACP_AK", filepath.Join(f.Base, "missing-ak"))
	r := acp(t, f, "", "sync", "--all")
	if r.code != 1 || strings.Count(r.stdout, "ak is not installed") != 2 {
		t.Fatalf("exit %d\n%s", r.code, r.stdout)
	}
	if f.RealPath(filepath.Join(f.Repo, ".claude/skills/ak-cook")) != store {
		t.Fatal("a store link was removed although ak is missing")
	}
	if !f.IsLink(filepath.Join(other, ".claude/skills/herdr")) {
		t.Fatal("the second project was not processed")
	}
}

func TestSyncUnknownProjectEnvelope(t *testing.T) {
	f := testkit.New(t)
	r := acp(t, f, "", "sync", "nope", "--json")
	var env map[string]any
	json.Unmarshal([]byte(r.stdout), &env)
	if r.code != 1 || env["error"] != "no project 'nope' (see `acp status`)" || env["kind"] != "acp.sync" {
		t.Fatalf("%d %s", r.code, r.stdout)
	}
}

func TestStatusJSONFields(t *testing.T) {
	f := testkit.New(t)
	mustOK(t, acp(t, f, "", "sync", "--all"))
	f.Overlay(map[string]string{"demo": f.Repo, "ghost": "/nowhere"})
	r := acp(t, f, "", "status", "--json")
	var env struct {
		Kind string
		Data struct {
			Projects []map[string]any
			Orphans  []string
			Pins     map[string]map[string]string
			Healthy  bool
			Text     string
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil || r.code != 0 {
		t.Fatalf("%v %d\n%s", err, r.code, r.stdout)
	}
	if env.Kind != "acp.status" || len(env.Data.Projects) != 1 || env.Data.Healthy {
		t.Fatalf("%+v", env)
	}
	if strings.Join(env.Data.Orphans, ",") != "ghost" || env.Data.Pins["beta"]["engineer"] != "3.0.0-beta.36" {
		t.Fatalf("orphans %v pins %v", env.Data.Orphans, env.Data.Pins)
	}
	p := env.Data.Projects[0]
	for _, k := range []string{"id", "repo", "agentkit", "personal", "worktrees", "notes", "healthy"} {
		if _, ok := p[k]; !ok {
			t.Errorf("project is missing %q", k)
		}
	}
	ak := p["agentkit"].(map[string]any)
	target := ak["targets"].([]any)[0].(map[string]any)
	for _, k := range []string{"target", "installed", "ok", "selection_ok", "files_missing", "skills", "stale", "agents", "hooks"} {
		if _, ok := target[k]; !ok {
			t.Errorf("target is missing %q", k)
		}
	}
	if p["healthy"] != true || target["installed"] != "3.0.0-beta.36" || target["agents"].(float64) != 1 ||
		target["files_missing"].(float64) != 0 {
		t.Fatalf("project %v", p)
	}
	personal := p["personal"].(map[string]any)
	if personal["linked"].(float64) != 2 || personal["total"].(float64) != 2 {
		t.Fatalf("personal %v", personal)
	}
	if !strings.Contains(env.Data.Text, "PROJECT") || !strings.Contains(env.Data.Text, "ghost") {
		t.Fatalf("text:\n%s", env.Data.Text)
	}
	if r := acp(t, f, "", "doctor"); r.code != 1 {
		t.Fatal("doctor must fail with an orphan overlay entry")
	}
}

func TestMalformedManifest(t *testing.T) {
	f := testkit.New(t)
	f.Write(filepath.Join(f.CP, "projects/demo.json"), "{bad")
	r := acp(t, f, "", "status", "--json")
	var env map[string]any
	json.Unmarshal([]byte(r.stdout), &env)
	if r.code != 1 || !strings.Contains(env["error"].(string), "demo.json") {
		t.Fatalf("%d %s", r.code, r.stdout)
	}
	h := acp(t, f, `{"cwd": "`+f.WT+`"}`, "link", "--hook")
	if h.code != 0 || h.stdout != "" {
		t.Fatalf("hook: %+v", h)
	}
}

func TestHookLinksANewWorktreeFromStdinCwd(t *testing.T) {
	f := testkit.New(t)
	mustOK(t, acp(t, f, "", "sync", "--all"))
	late := filepath.Join(f.Base, "late")
	f.Git(f.Repo, "worktree", "add", "-q", "-b", "late", late)
	if r := acp(t, f, "", "doctor"); r.code != 1 {
		t.Fatal("doctor must see the unlinked worktree")
	}
	r := acp(t, f, `{"cwd": "`+filepath.Join(late, ".claude")+`"}`, "link", "--hook")
	if r.code != 0 || r.stdout != "" {
		t.Fatalf("hook: %+v", r)
	}
	if !f.IsLink(filepath.Join(late, ".claude/skills/ak-cook")) {
		t.Fatal("the hook did not link the worktree")
	}
	if r := acp(t, f, "", "doctor"); r.code != 0 {
		t.Fatalf("doctor after the hook:\n%s", r.stdout)
	}
}

func TestLinkCheckReportsWithoutWriting(t *testing.T) {
	f := testkit.New(t)
	if r := acp(t, f, "", "link", f.WT, "--check"); r.code != 1 {
		t.Fatalf("check before link: %d", r.code)
	}
	if f.Exists(filepath.Join(f.WT, ".claude/skills/herdr")) {
		t.Fatal("--check wrote a link")
	}
	mustOK(t, acp(t, f, "", "link", f.WT))
	if r := acp(t, f, "", "link", f.WT, "--check"); r.code != 0 {
		t.Fatalf("check after link: %d\n%s", r.code, r.stdout)
	}
	if r := acp(t, f, "", "link", filepath.Join(f.Base, "gone")); r.code != 1 || !strings.Contains(r.stderr, "does not exist") {
		t.Fatalf("missing path: %+v", r)
	}
}

func TestHookIsSilentOnBadInput(t *testing.T) {
	f := testkit.New(t)
	gone := filepath.Join(f.Base, "gone")
	os.MkdirAll(gone, 0o755)
	os.Remove(gone)
	for _, stdin := range []string{`{"cwd": "` + f.Base + `"}`, `[]`, `"x"`, `{"cwd": 3}`, `{not json`,
		`{"cwd": "` + gone + `"}`, strings.Repeat("x", 2<<20)} {
		r := acp(t, f, stdin, "link", "--hook")
		if r.code != 0 || r.stdout != "" {
			t.Errorf("stdin %.30q: %+v", stdin, r)
		}
	}
}

func TestUnlinkClearsWorktreeLinksOnly(t *testing.T) {
	f := testkit.New(t)
	mustOK(t, acp(t, f, "", "sync", "--all"))
	r := mustOK(t, acp(t, f, "", "unlink", f.WT))
	if !strings.Contains(r.stdout, "- .claude/skills/ak-cook") {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
	if f.Exists(filepath.Join(f.WT, ".claude/skills/ak-cook")) || !f.IsDir(filepath.Join(f.Repo, ".claude/skills/ak-cook")) {
		t.Fatal("unlink removed the wrong thing")
	}
}

func TestConcurrentHooksLinkEveryWorktree(t *testing.T) {
	f := testkit.New(t)
	mustOK(t, acp(t, f, "", "sync", "--all", "--offline"))
	var trees []string
	for i := 0; i < 8; i++ {
		wt := filepath.Join(f.Base, "par", string(rune('a'+i)))
		f.Git(f.Repo, "worktree", "add", "-q", "-b", "par-"+string(rune('a'+i)), wt)
		trees = append(trees, wt)
	}
	var wg sync.WaitGroup
	results := make([]result, len(trees))
	for i, wt := range trees {
		wg.Add(1)
		go func(i int, wt string) {
			defer wg.Done()
			results[i] = acp(t, f, `{"cwd": "`+wt+`"}`, "link", "--hook")
		}(i, wt)
	}
	wg.Wait()
	for i, wt := range trees {
		if !f.IsLink(filepath.Join(wt, ".claude/skills/herdr")) {
			t.Errorf("%s not linked: %+v", wt, results[i])
		}
		if s := f.Status(wt); s != "" {
			t.Errorf("%s not clean:\n%s", wt, s)
		}
	}
}

func TestHookOnAnotherRepoIsNotBlocked(t *testing.T) {
	f := testkit.New(t)
	_, otherWT := addProject(t, f, "other")
	root := &core.Root{Dir: f.CP}
	lock, err := core.AcquireWithin(root.RepoLock(f.Repo), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	start := time.Now()
	r := acp(t, f, `{"cwd": "`+otherWT+`"}`, "link", "--hook")
	if r.code != 0 || !f.IsLink(filepath.Join(otherWT, ".claude/skills/herdr")) || time.Since(start) > 4*time.Second {
		t.Fatalf("hook on another repo: %+v after %v", r, time.Since(start))
	}
}

func TestHookSkipsABusyRepoAndSaysSo(t *testing.T) {
	f := testkit.New(t)
	root := &core.Root{Dir: f.CP}
	lock, err := core.AcquireWithin(root.RepoLock(f.Repo), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	r := acp(t, f, `{"cwd": "`+f.WT+`"}`, "link", "--hook")
	if r.code != 0 || r.stdout != "" || strings.Count(strings.TrimSpace(r.stderr), "\n") != 0 || !strings.Contains(r.stderr, "busy") {
		t.Fatalf("busy hook: %+v", r)
	}
	if f.Exists(filepath.Join(f.WT, ".claude/skills/herdr")) {
		t.Fatal("a busy hook wrote links")
	}
}
