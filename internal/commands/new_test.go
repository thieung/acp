package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thieung/acp/internal/testkit"
)

// newStatus is git status without FakeAK's own call log, which a real ak never writes.
func newStatus(f *testkit.Fixture, dir string) string {
	var keep []string
	for _, line := range strings.SplitAfter(f.Status(dir), "\n") {
		if line != "" && line != "?? .ak-calls\n" {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "")
}

func targetsOf(t *testing.T, f *testkit.Fixture, id string) []any {
	t.Helper()
	return readMap(t, filepath.Join(f.CP, "projects", id+".json"))["agentkit"].(map[string]any)["targets"].([]any)
}

func TestNewCreatesAFolderWithAgentKitForClaude(t *testing.T) {
	f := testkit.New(t)
	foo := filepath.Join(f.Home, "foo")
	r := mustOK(t, acp(t, f, "", "new", foo))
	if !f.IsDir(filepath.Join(foo, ".git")) || f.Exists(filepath.Join(foo, ".agents")) {
		t.Fatalf("repo layout:\n%s", r.stdout)
	}
	if got := targetsOf(t, f, "foo"); !reflect.DeepEqual(got, []any{"claude-code"}) {
		t.Fatalf("targets %v", got)
	}
	if !f.Exists(filepath.Join(foo, ".agentkit/adapters/claude-code/engineer/.agentkit/install-manifest.json")) {
		t.Fatal("AgentKit was not installed for claude-code")
	}
	if calls := f.Read(filepath.Join(foo, ".ak-calls")); calls != "kit init engineer\n" {
		t.Fatalf("ak calls %q", calls)
	}
	if st := newStatus(f, foo); st != "" {
		t.Fatalf("git status shows AgentKit output:\n%s", st)
	}
	if repos := readMap(t, filepath.Join(f.CP, "projects.local.json"))["repos"].(map[string]any); repos["foo"] != foo {
		t.Fatalf("overlay %v", repos)
	}
}

func TestNewLinksDefaultAndFlagSkills(t *testing.T) {
	f := testkit.New(t)
	f.Write(filepath.Join(f.CP, "skills/extra/SKILL.md"), "---\nname: extra\n---\n")
	f.Write(filepath.Join(f.CP, "project-defaults.json"), `{"schemaVersion": 1, "skills": ["herdr"]}`)
	foo := filepath.Join(f.Home, "foo")
	mustOK(t, acp(t, f, "", "new", foo, "--skills", "extra,herdr"))
	ledger := f.Read(filepath.Join(foo, ".git/acp-links.json"))
	for _, name := range []string{"herdr", "extra"} {
		link := filepath.Join(foo, ".claude/skills", name)
		if f.RealPath(link) != filepath.Join(f.CP, "skills", name) || !strings.Contains(ledger, ".claude/skills/"+name) {
			t.Fatalf("%s is not an acp link in the ledger:\n%s", name, ledger)
		}
	}
	skills := readMap(t, filepath.Join(f.CP, "projects/foo.json"))["skills"].([]any)
	if len(skills) != 2 || skills[0].(map[string]any)["name"] != "herdr" {
		t.Fatalf("skills %v", skills)
	}
	if st := newStatus(f, foo); st != "" {
		t.Fatalf("status:\n%s", st)
	}
	mustOK(t, acp(t, f, "", "unlink", foo))
	if f.Exists(filepath.Join(foo, ".claude/skills/herdr")) || f.Exists(filepath.Join(foo, ".claude/skills/extra")) {
		t.Fatal("unlink left the links")
	}
}

func TestNewSettingsPrecedence(t *testing.T) {
	f := testkit.New(t)
	f.Write(filepath.Join(f.CP, "project-defaults.json"),
		`{"schemaVersion": 1, "agentkit": {"kit": "engineer", "channel": "beta"}, "runtimes": ["codex"], "skills": ["herdr"]}`)
	f.Write(filepath.Join(f.CP, "agentkit-releases.lock.json"),
		`{"schemaVersion": 2, "channels": {"beta": {"engineer": "3.0.0-beta.36"}, "stable": {"engineer": "3.0.0"}}}`)
	for _, c := range []struct {
		id      string
		args    []string
		targets []any
		channel string
	}{
		{"from-file", nil, []any{"codex"}, "beta"},
		{"flag-claude", []string{"--runtimes", "claude"}, []any{"claude-code"}, "beta"},
		{"flag-both", []string{"--runtimes", "claude,codex", "--channel", "stable"}, []any{"claude-code", "codex"}, "stable"},
	} {
		id := c.id
		mustOK(t, acp(t, f, "", append([]string{"new", filepath.Join(f.Home, id), "--no-sync"}, c.args...)...))
		m := readMap(t, filepath.Join(f.CP, "projects", id+".json"))
		ak := m["agentkit"].(map[string]any)
		if !reflect.DeepEqual(ak["targets"], c.targets) || ak["channel"] != c.channel {
			t.Errorf("%v: agentkit %v", c.args, ak)
		}
		want := []any{}
		for _, tgt := range c.targets {
			want = append(want, map[string]string{"claude-code": "claude", "codex": "codex"}[tgt.(string)])
		}
		if got := m["skills"].([]any)[0].(map[string]any)["runtimes"]; !reflect.DeepEqual(got, want) {
			t.Errorf("%v: personal runtimes %v", c.args, got)
		}
	}
}

func TestNewGitInitsAnExistingFolderAndKeepsUserFilesVisible(t *testing.T) {
	f := testkit.New(t)
	foo := filepath.Join(f.Home, "foo")
	f.Write(filepath.Join(foo, "notes.txt"), "mine")
	f.Write(filepath.Join(foo, ".claude/rules/team.md"), "mine")
	mustOK(t, acp(t, f, "", "new", foo))
	if f.Read(filepath.Join(foo, "notes.txt")) != "mine" {
		t.Fatal("notes.txt changed")
	}
	if st := newStatus(f, foo); st != "?? .claude/rules/team.md\n?? notes.txt\n" {
		t.Fatalf("status:\n%s", st)
	}
}

func TestNewOnAnExistingRepoActsLikeAdd(t *testing.T) {
	f := testkit.New(t)
	app := filepath.Join(f.Home, "app")
	f.Write(filepath.Join(app, "README.md"), "app")
	f.Git(app, "init", "-q")
	f.Git(app, "add", ".")
	f.Git(app, "commit", "-qm", "init")
	_, data := acpJSON(t, f, "new", app)
	if data["mode"] != "repo" {
		t.Fatalf("data %v", data)
	}
	if f.Exists(filepath.Join(app, ".git/acp-new.json")) ||
		strings.Contains(f.Read(filepath.Join(app, ".git/info/exclude")), "acp new") {
		t.Fatal("new hid files in a repo it did not create")
	}
	if !f.Exists(filepath.Join(f.CP, "projects/app.json")) {
		t.Fatal("not registered")
	}
}

func TestNewResumesAfterAnAgentKitFailure(t *testing.T) {
	f := testkit.New(t)
	foo := filepath.Join(f.Home, "foo")
	f.Write(filepath.Join(foo, ".agentkit/BROKEN"), "")
	r := acp(t, f, "", "new", foo)
	if r.code != 1 || !strings.Contains(r.stdout, "acp new "+foo) || !f.Exists(filepath.Join(f.CP, "projects/foo.json")) ||
		!f.Exists(filepath.Join(foo, ".git/acp-new.json")) {
		t.Fatalf("failed run: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	os.Remove(filepath.Join(foo, ".agentkit/BROKEN"))
	mustOK(t, acp(t, f, "", "new", foo))
	if !f.Exists(filepath.Join(foo, ".agentkit/adapters/claude-code/engineer/.agentkit/install-manifest.json")) {
		t.Fatal("resume did not install AgentKit")
	}
	// .agentkit was there before acp new, so only what ak added under it is hidden.
	if st := newStatus(f, foo); st != "" {
		t.Fatalf("status after resume:\n%s", st)
	}
	other := filepath.Join(f.Home, "other", "foo")
	os.MkdirAll(filepath.Dir(other), 0o755)
	if r := acp(t, f, "", "new", other); r.code != 1 || !strings.Contains(r.stderr, "already in the fleet") {
		t.Fatalf("same id, other path: %d %s", r.code, r.stderr)
	}
}

func TestNewRefusesAndWritesNothing(t *testing.T) {
	f := testkit.New(t)
	// A control plane inside HOME, like ~/agent-control-plane.
	cp := filepath.Join(f.Home, "agent-control-plane")
	if err := os.CopyFS(cp, os.DirFS(f.CP)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACP_ROOT", cp)
	app := filepath.Join(f.Home, "app")
	os.MkdirAll(filepath.Join(app, "sub"), 0o755)
	f.Git(app, "init", "-q")
	f.Write(filepath.Join(cp, "project-defaults.json"), `{"schemaVersion": 1}`)
	before := testkit.Fingerprint(f.Base)
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"/"}, 1, "refusing"},
		{[]string{f.Home}, 1, "home folder"},
		{[]string{filepath.Join(cp, "skills", "x")}, 1, "inside the control plane"},
		{[]string{filepath.Join(app, "sub")}, 1, "inside the git repo"},
		{[]string{filepath.Join(app, ".git", "x")}, 1, ".git"},
		{[]string{filepath.Join(f.Home, "a", "b")}, 1, "does not exist"},
		{[]string{filepath.Join(f.Base, "out")}, 1, "outside your home folder"},
		{[]string{filepath.Join(f.Home, "demo")}, 1, "already in the fleet"},
		{[]string{filepath.Join(f.Home, "x"), "--id", "demo"}, 1, "already in the fleet"},
		{[]string{f.Repo}, 1, "already in the fleet as demo"},
		{[]string{filepath.Join(f.Home, "x"), "--skills", "nope"}, 1, "personal skill 'nope'"},
		{[]string{filepath.Join(f.Home, "x"), "--runtimes", "grok"}, 2, "unknown runtime"},
		{[]string{filepath.Join(f.Home, "x"), "--runtimes", ""}, 2, "--runtimes is empty"},
	} {
		if c.args[0] == filepath.Join(f.Home, "demo") {
			os.MkdirAll(c.args[0], 0o755)
			before = testkit.Fingerprint(f.Base)
		}
		r := acp(t, f, "", append([]string{"new"}, c.args...)...)
		if r.code != c.code || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%v: %d %q", c.args, r.code, r.stderr)
		}
		if after := testkit.Fingerprint(f.Base); !reflect.DeepEqual(before, after) {
			t.Fatalf("%v wrote something", c.args)
		}
	}
	f.Write(filepath.Join(cp, "project-defaults.json"), `{"schemaVersion": 1, "typo": true}`)
	before = testkit.Fingerprint(f.Base)
	if r := acp(t, f, "", "new", filepath.Join(f.Home, "x")); r.code != 1 || !strings.Contains(r.stderr, "unknown key typo") {
		t.Errorf("bad defaults: %d %q", r.code, r.stderr)
	}
	if !reflect.DeepEqual(before, testkit.Fingerprint(f.Base)) {
		t.Fatal("bad defaults wrote something")
	}
}

func TestNewPreflightsThePinAndAk(t *testing.T) {
	f := testkit.New(t)
	foo := filepath.Join(f.Home, "foo")
	f.Write(filepath.Join(f.CP, "agentkit-releases.lock.json"), `{"schemaVersion": 2, "channels": {}}`)
	for _, extra := range [][]string{nil, {"--dry-run"}} {
		code, data := acpJSON(t, f, append([]string{"new", foo}, extra...)...)
		if code != 1 || !strings.Contains(data["error"].(string), "acp upgrade") || f.Exists(foo) {
			t.Fatalf("no pin %v: %d %v", extra, code, data)
		}
	}
	f.Write(filepath.Join(f.CP, "agentkit-releases.lock.json"),
		`{"schemaVersion": 2, "channels": {"beta": {"engineer": "3.0.0-beta.36"}}}`)
	before := testkit.Fingerprint(f.Base)
	t.Setenv("ACP_AK", filepath.Join(f.Base, "missing-ak"))
	if r := acp(t, f, "", "new", foo); r.code != 1 || !strings.Contains(r.stderr, "ak is not installed") {
		t.Fatalf("no ak: %d %q", r.code, r.stderr)
	}
	if !reflect.DeepEqual(before, testkit.Fingerprint(f.Base)) {
		t.Fatal("a refused preflight wrote something")
	}
	mustOK(t, acp(t, f, "", "new", foo, "--no-sync"))
}

func TestNewDryRunWritesNothing(t *testing.T) {
	f := testkit.New(t)
	foo := filepath.Join(f.Home, "foo")
	f.Write(filepath.Join(f.CP, "project-defaults.json"), `{"schemaVersion": 1, "skills": ["herdr"]}`)
	before := testkit.Fingerprint(f.Base)
	r := mustOK(t, acp(t, f, "", "new", foo, "--dry-run"))
	if !strings.HasSuffix(strings.TrimSpace(r.stdout), "dry run: nothing was written") {
		t.Fatalf("dry run:\n%s", r.stdout)
	}
	code, data := acpJSON(t, f, "new", foo, "--dry-run")
	if code != 0 || data["mode"] != "create" {
		t.Fatalf("json: %d %v", code, data)
	}
	changes := strings.Join(toStrings(data["logs"].([]any)[0].(map[string]any)["changes"]), "\n")
	for _, want := range []string{"create " + foo, "git init", ".git/info/exclude", "projects/foo.json",
		"AgentKit engineer 3.0.0-beta.36 (claude-code)", ".claude/skills/herdr"} {
		if !strings.Contains(changes, want) {
			t.Errorf("changes lack %q:\n%s", want, changes)
		}
	}
	if !reflect.DeepEqual(before, testkit.Fingerprint(f.Base)) || f.Exists(foo) {
		t.Fatal("dry run wrote something")
	}
	// A real folder where a link should go is shown before anything happens.
	f.Write(filepath.Join(foo, ".claude/skills/herdr/SKILL.md"), "mine")
	_, data = acpJSON(t, f, "new", foo, "--dry-run")
	issues, _ := data["logs"].([]any)[0].(map[string]any)["issues"].([]any)
	if len(issues) != 1 || !strings.Contains(issues[0].(string), "real directory") {
		t.Fatalf("issues %v", issues)
	}
}

func TestNewWarnsWhenHooksAreMissing(t *testing.T) {
	f := testkit.New(t)
	r := mustOK(t, acp(t, f, "", "new", filepath.Join(f.Home, "a")))
	if !strings.Contains(r.stdout, "acp hooks install") {
		t.Fatalf("no hooks warning:\n%s", r.stdout)
	}
	settings := filepath.Join(f.Home, ".claude/settings.json")
	f.Write(settings, `{"hooks": {"SessionStart": [{"matcher": "startup|resume", "hooks": [
  {"type": "command", "command": "/gone/acp link --hook"}]}]}}`)
	if _, data := acpJSON(t, f, "new", filepath.Join(f.Home, "b"), "--no-sync"); data["hooks_installed"] != false {
		t.Fatalf("a hook to a deleted binary counted: %v", data)
	}
	bin := installableBinary(t, f, filepath.Join(f.Base, "bin2"))
	f.Write(settings, `{"hooks": {"SessionStart": [{"matcher": "startup|resume", "hooks": [
  {"type": "command", "command": "`+bin+` link --hook"}]}]}}`)
	r = mustOK(t, acp(t, f, "", "new", filepath.Join(f.Home, "c"), "--no-sync"))
	if strings.Contains(r.stdout, "acp hooks install") {
		t.Fatalf("warned with hooks installed:\n%s", r.stdout)
	}
}

func TestRemoveDropsTheNewExcludeBlock(t *testing.T) {
	f := testkit.New(t)
	foo := filepath.Join(f.Home, "foo")
	mustOK(t, acp(t, f, "", "new", foo))
	if !strings.Contains(f.Read(filepath.Join(foo, ".git/info/exclude")), "acp new") {
		t.Fatal("no block")
	}
	mustOK(t, acp(t, f, "", "remove", "foo", "--yes"))
	if strings.Contains(f.Read(filepath.Join(foo, ".git/info/exclude")), "acp new") || f.Exists(filepath.Join(foo, ".git/acp-new.json")) {
		t.Fatal("remove left the block")
	}
}

func TestNewOnAFinishedProjectPointsToSync(t *testing.T) {
	f := testkit.New(t)
	foo := filepath.Join(f.Home, "foo")
	mustOK(t, acp(t, f, "", "new", foo))
	if rec := readMap(t, filepath.Join(foo, ".git/acp-new.json")); rec["done"] != true {
		t.Fatalf("record %v", rec)
	}
	r := acp(t, f, "", "new", foo)
	if r.code != 1 || !strings.Contains(r.stderr, "already in the fleet as foo; run: acp sync foo") {
		t.Fatalf("second run: %d %s", r.code, r.stderr)
	}
}

func TestNewResumeKeepsTheSettingsAndIDItStartedWith(t *testing.T) {
	f := testkit.New(t)
	foo := filepath.Join(f.Home, "foo")
	f.Write(filepath.Join(foo, ".agentkit/BROKEN"), "")
	if r := acp(t, f, "", "new", foo, "--id", "app", "--runtimes", "codex"); r.code != 1 {
		t.Fatalf("failed run: %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	os.Remove(filepath.Join(foo, ".agentkit/BROKEN"))
	head := f.Read(filepath.Join(foo, ".git/HEAD"))
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"--id", "foo"}, 1, "started " + foo + " as app"},
		{[]string{"--runtimes", "claude"}, 2, "drop --runtimes"},
		{[]string{"--kit", "marketing"}, 2, "drop --kit"},
	} {
		if r := acp(t, f, "", append([]string{"new", foo}, c.args...)...); r.code != c.code || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%v: %d %s", c.args, r.code, r.stderr)
		}
	}
	r := mustOK(t, acp(t, f, "", "new", foo, "--id", "app"))
	if !strings.Contains(r.stdout, "finishing app -> "+foo) || strings.Contains(r.stdout, "git init") ||
		f.Read(filepath.Join(foo, ".git/HEAD")) != head {
		t.Fatalf("resume:\n%s", r.stdout)
	}
	if got := targetsOf(t, f, "app"); !reflect.DeepEqual(got, []any{"codex"}) {
		t.Fatalf("targets %v", got)
	}
	if !f.Exists(filepath.Join(foo, ".agentkit/adapters/codex/engineer/.agentkit/install-manifest.json")) {
		t.Fatal("resume did not install AgentKit for codex")
	}
	if st := newStatus(f, foo); st != "" {
		t.Fatalf("status:\n%s", st)
	}
}

func TestNewForCodexLinksAndHides(t *testing.T) {
	f := testkit.New(t)
	foo := filepath.Join(f.Home, "foo")
	mustOK(t, acp(t, f, "", "new", foo, "--runtimes", "codex", "--skills", "herdr"))
	if calls := f.Read(filepath.Join(foo, ".ak-calls")); calls != "kit init engineer\n" {
		t.Fatalf("ak calls %q", calls)
	}
	if !f.Exists(filepath.Join(foo, ".agentkit/adapters/codex/engineer/.agentkit/install-manifest.json")) ||
		f.Exists(filepath.Join(foo, ".claude")) {
		t.Fatal("AgentKit layout is not codex only")
	}
	if dest, err := os.Readlink(filepath.Join(foo, ".agents/skills/herdr")); err != nil || dest != filepath.Join(f.CP, "skills/herdr") {
		t.Fatalf("herdr link: %q %v", dest, err)
	}
	if st := newStatus(f, foo); st != "" {
		t.Fatalf("status:\n%s", st)
	}
}

func TestNewKitFlagOverridesTheFile(t *testing.T) {
	f := testkit.New(t)
	f.Write(filepath.Join(f.CP, "project-defaults.json"), `{"schemaVersion": 1, "agentkit": {"kit": "engineer"}}`)
	foo := filepath.Join(f.Home, "foo")
	r := acp(t, f, "", "new", foo, "--kit", "marketing")
	if r.code != 1 || !strings.Contains(r.stderr, "no beta/marketing pin") || f.Exists(foo) {
		t.Fatalf("missing pin: %d %s", r.code, r.stderr)
	}
	mustOK(t, acp(t, f, "", "new", foo, "--kit", "marketing", "--no-sync"))
	if ak := readMap(t, filepath.Join(f.CP, "projects/foo.json"))["agentkit"].(map[string]any); ak["kit"] != "marketing" {
		t.Fatalf("agentkit %v", ak)
	}
}

func TestNewNoSyncLeavesTheRunToFinish(t *testing.T) {
	f := testkit.New(t)
	foo := filepath.Join(f.Home, "foo")
	dry := mustOK(t, acp(t, f, "", "new", foo, "--no-sync", "--dry-run"))
	if !strings.Contains(dry.stdout, "wait for `acp new "+foo+"`") || strings.Contains(dry.stdout, "+ hide") {
		t.Fatalf("dry run:\n%s", dry.stdout)
	}
	r := mustOK(t, acp(t, f, "", "new", foo, "--no-sync"))
	if !strings.Contains(r.stdout, "finish with: acp new "+foo) || f.Exists(filepath.Join(foo, ".git/info/exclude")) &&
		strings.Contains(f.Read(filepath.Join(foo, ".git/info/exclude")), "acp new") {
		t.Fatalf("no-sync run:\n%s", r.stdout)
	}
	if rec := readMap(t, filepath.Join(foo, ".git/acp-new.json")); rec["done"] == true {
		t.Fatal("a --no-sync run is not done")
	}
	mustOK(t, acp(t, f, "", "new", foo))
	if rec := readMap(t, filepath.Join(foo, ".git/acp-new.json")); rec["done"] != true {
		t.Fatal("finishing run did not mark the record done")
	}
	if st := newStatus(f, foo); st != "" {
		t.Fatalf("status:\n%s", st)
	}
}

// A run that stopped after git init but before the project was added still hides what it creates.
func TestNewFinishesARepoItGitInitedButDidNotAdd(t *testing.T) {
	f := testkit.New(t)
	foo := filepath.Join(f.Home, "foo")
	f.Write(filepath.Join(foo, "notes.txt"), "mine")
	f.Git(foo, "init", "-q")
	f.Write(filepath.Join(foo, ".git/acp-new.json"), `{"schemaVersion": 1, "preexisting": [], "done": false}`)
	mustOK(t, acp(t, f, "", "new", foo))
	if st := newStatus(f, foo); st != "?? notes.txt\n" {
		t.Fatalf("status:\n%s", st)
	}
}

func TestRemoveDropsTheBlockWhenTheRecordIsUnreadable(t *testing.T) {
	f := testkit.New(t)
	foo := filepath.Join(f.Home, "foo")
	mustOK(t, acp(t, f, "", "new", foo))
	exclude := filepath.Join(foo, ".git/info/exclude")
	dry := mustOK(t, acp(t, f, "", "remove", "foo"))
	if !strings.Contains(dry.stdout, "- "+filepath.Join(foo, ".git/info/exclude")+": the block acp new wrote") ||
		!strings.Contains(f.Read(exclude), "acp new") {
		t.Fatalf("dry run:\n%s", dry.stdout)
	}
	f.Write(filepath.Join(foo, ".git/acp-new.json"), "{not json")
	r := mustOK(t, acp(t, f, "", "remove", "foo", "--yes"))
	if !strings.Contains(r.stdout, "removing it and the acp new block anyway") || strings.Contains(f.Read(exclude), "acp new") ||
		f.Exists(filepath.Join(foo, ".git/acp-new.json")) {
		t.Fatalf("remove:\n%s", r.stdout)
	}
}
