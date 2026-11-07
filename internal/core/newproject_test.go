package core

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestPlanNewPathModes(t *testing.T) {
	f := newFixture(t)
	other := filepath.Join(f.home, "code")
	os.MkdirAll(other, 0o755)
	repo := filepath.Join(f.home, "code", "app")
	os.MkdirAll(repo, 0o755)
	f.git(repo, "init", "-q")
	f.write(filepath.Join(repo, "README.md"), "app")
	f.git(repo, "add", ".")
	f.git(repo, "commit", "-qm", "init")
	wt := filepath.Join(f.home, "code", "app-wt")
	f.git(repo, "worktree", "add", "-q", "-b", "feature", wt)
	plain := filepath.Join(f.home, "code", "plain")
	f.write(filepath.Join(plain, "notes.txt"), "x")
	f.symlink(other, filepath.Join(f.home, "via"))

	for _, c := range []struct {
		path       string
		mode       NewMode
		real, main string
	}{
		{filepath.Join(other, "fresh"), ModeCreate, filepath.Join(other, "fresh"), filepath.Join(other, "fresh")},
		{plain, ModeInit, plain, plain},
		{repo, ModeRepo, repo, repo},
		{wt, ModeRepo, wt, repo},
		{filepath.Join(f.home, "via", "fresh"), ModeCreate, filepath.Join(other, "fresh"), filepath.Join(other, "fresh")},
		{filepath.Join(f.home, "via", "plain"), ModeInit, plain, plain},
	} {
		np, err := PlanNewPath(f.root, c.path)
		if err != nil {
			t.Errorf("%s: %v", c.path, err)
			continue
		}
		if np.Mode != c.mode || np.Real != c.real || np.Main != c.main {
			t.Errorf("%s: got %+v", c.path, np)
		}
	}
}

func TestPlanNewPathRefusals(t *testing.T) {
	f := newFixture(t)
	code := filepath.Join(f.home, "code")
	repo := filepath.Join(code, "app")
	os.MkdirAll(repo, 0o755)
	f.git(repo, "init", "-q")
	os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
	bare := filepath.Join(code, "mirror.git")
	f.git(code, "init", "-q", "--bare", bare)
	bareish := filepath.Join(code, "mirror")
	f.git(code, "init", "-q", "--bare", bareish)
	broken := filepath.Join(code, "broken")
	f.write(filepath.Join(broken, ".git"), "gitdir: /nowhere/at/all\n")
	holder := filepath.Join(f.home, "holder")
	os.MkdirAll(filepath.Join(holder, "inner", ".git"), 0o755)
	file := filepath.Join(code, "file.txt")
	f.write(file, "x")
	os.MkdirAll(filepath.Join(f.home, ".pi", "agent"), 0o755)

	// A control plane inside HOME, like the real ~/agent-control-plane.
	cp := filepath.Join(f.home, "agent-control-plane")
	os.MkdirAll(filepath.Join(cp, "skills"), 0o755)
	root := &Root{Dir: cp}
	// A worktree whose main checkout is the control plane.
	f.git(cp, "init", "-q")
	f.write(filepath.Join(cp, "README.md"), "cp")
	f.git(cp, "add", ".")
	f.git(cp, "commit", "-qm", "init")
	cpWT := filepath.Join(code, "cp-wt")
	f.git(cp, "worktree", "add", "-q", "-b", "side", cpWT)

	for _, c := range []struct{ path, want string }{
		{filepath.Join(repo, "sub"), "is inside the git repo"},
		{filepath.Join(repo, "sub", "new"), "is inside the git repo"},
		{filepath.Join(repo, ".git"), ".git"},
		{filepath.Join(repo, ".git", "hooks", "x"), ".git"},
		{bare, "git directory or bare repository"},
		{bareish, "git directory or bare repository"},
		{broken, "git"},
		{"/", "refusing"},
		{f.home, "home folder"},
		{filepath.Join(cp, "skills"), "inside the control plane"},
		{filepath.Join(cp, "skills", "new"), "inside the control plane"},
		{f.home + "/../home", "home folder"},
		{filepath.Join(f.home, ".pi"), "agent home"},
		{filepath.Join(f.home, ".claude", "x"), "agent home"},
		{holder, "contains git repositories"},
		{filepath.Join(f.base, "outside"), "outside your home folder"},
		{filepath.Join(code, "a", "b"), "does not exist"},
		{file, "not a folder"},
		{cpWT, "inside the control plane"},
	} {
		_, err := PlanNewPath(root, c.path)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", c.path, c.want, err)
		}
	}
	// A folder that contains the control plane.
	if _, err := PlanNewPath(&Root{Dir: filepath.Join(holder, "cp")}, holder); err == nil ||
		!strings.Contains(err.Error(), "contains") {
		t.Errorf("folder holding the control plane: %v", err)
	}
	// An agent home kept in git is still an agent home; a dotfiles repo holding one is a project.
	f.git(f.home, "init", "-q", filepath.Join(f.home, ".claude"))
	if _, err := PlanNewPath(root, filepath.Join(f.home, ".claude")); err == nil || !strings.Contains(err.Error(), "agent home") {
		t.Errorf("agent home as a repo: %v", err)
	}
	dots := filepath.Join(f.home, "dotfiles")
	os.MkdirAll(filepath.Join(dots, "codex"), 0o755)
	f.git(dots, "init", "-q")
	os.RemoveAll(filepath.Join(f.home, ".codex"))
	f.symlink(filepath.Join(dots, "codex"), filepath.Join(f.home, ".codex"))
	if np, err := PlanNewPath(root, dots); err != nil || np.Mode != ModeRepo {
		t.Errorf("dotfiles repo holding an agent home: %+v %v", np, err)
	}
	// A link to a folder that is gone is not a missing folder to create.
	f.symlink(filepath.Join(code, "gone"), filepath.Join(code, "dangling"))
	if _, err := PlanNewPath(root, filepath.Join(code, "dangling")); err == nil || !strings.Contains(err.Error(), "missing folder") {
		t.Errorf("dangling link: %v", err)
	}
	// A case variant names the same folder on a case-insensitive disk (the macOS default).
	if upper := strings.ToUpper(f.home); upper != f.home && Exists(upper) {
		for _, c := range []struct{ path, want string }{
			{upper, "refusing to use your home folder"},
			{filepath.Join(strings.ToUpper(cp), "skills"), "inside the control plane"},
			{filepath.Join(strings.ToUpper(f.home), ".pi"), "agent home"},
		} {
			if _, err := PlanNewPath(root, c.path); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: want %q, got %v", c.path, c.want, err)
			}
		}
		if np, err := PlanNewPath(root, strings.ToUpper(repo)); err != nil || np.Mode != ModeRepo {
			t.Errorf("case variant of a repo: %+v %v", np, err)
		}
	}
	// git answers in English whatever the user's locale.
	t.Setenv("LC_ALL", "vi_VN.UTF-8")
	t.Setenv("LANGUAGE", "vi")
	if np, err := PlanNewPath(root, filepath.Join(code, "fresh")); err != nil || np.Mode != ModeCreate {
		t.Errorf("non-English locale: %+v %v", np, err)
	}
	for _, v := range gitDirVars {
		t.Run(v, func(t *testing.T) {
			t.Setenv(v, filepath.Join(repo, ".git"))
			if _, err := PlanNewPath(root, filepath.Join(code, "fresh")); err == nil || !strings.Contains(err.Error(), v) {
				t.Errorf("%s set: %v", v, err)
			}
		})
	}
}

func TestLoadNewDefaults(t *testing.T) {
	f := newFixture(t)
	d, err := f.root.LoadNewDefaults()
	if err != nil || d.File != "" || d.Kit != "" || len(d.Runtimes) != 0 {
		t.Fatalf("missing file: %+v %v", d, err)
	}
	file := f.root.ProjectDefaults()
	f.write(file, `{"schemaVersion": 1, "agentkit": {"kit": "marketing", "channel": "stable"},
  "runtimes": ["codex"], "skills": ["herdr"]}`)
	d, err = f.root.LoadNewDefaults()
	if err != nil || d.File != file || d.Kit != "marketing" || d.Channel != "stable" ||
		!slices.Equal(d.Runtimes, []string{"codex"}) || !slices.Equal(d.Skills, []string{"herdr"}) || d.AKSkills != nil {
		t.Fatalf("valid file: %+v %v", d, err)
	}
	for body, want := range map[string]NewDefaults{
		`{"schemaVersion": 1, "agentkit": {"skills": "all"}}`:                  {AKPreset: "all"},
		`{"schemaVersion": 1, "agentkit": {"skills": "minimal,pr"}}`:           {AKPreset: "minimal,pr"},
		`{"schemaVersion": 1, "agentkit": {"skills": ["ak-plan", "ak-cook"]}}`: {AKSkills: []string{"ak-plan", "ak-cook"}},
	} {
		f.write(file, body)
		if d, err = f.root.LoadNewDefaults(); err != nil || d.AKPreset != want.AKPreset || !slices.Equal(d.AKSkills, want.AKSkills) {
			t.Errorf("%s: %q %v %v", body, d.AKPreset, d.AKSkills, err)
		}
	}
	for _, body := range []string{
		`{"schemaVersion": 1, "agentkit": {"skills": "tiny"}}`,
		`{"schemaVersion": 1, "agentkit": {"skills": "all,minimal"}}`,
		`{"schemaVersion": 1, "agentkit": {"skills": ""}}`,
		`{"schemaVersion": 1, "agentkit": {"skills": []}}`,
		`{"schemaVersion": 1, "agentkit": {"skills": ["ak-plan", 1]}}`,
		`{"schemaVersion": 1, "agentkit": {"skills": ["../x"]}}`,
		`{"schemaVersion": 1, "agentkit": {"skills": 3}}`,
		`{"runtimes": ["claude"]}`,
		`{"schemaVersion": 2}`,
		`{"schemaVersion": 1, "extra": 1}`,
		`{"schemaVersion": 1, "agentkit": {"kit": "engineer", "pin": "x"}}`,
		`{"schemaVersion": 1, "runtimes": "codex"}`,
		`{"schemaVersion": 1, "runtimes": []}`,
		`{"schemaVersion": 1, "runtimes": ["grok"]}`,
		`{"schemaVersion": 1, "skills": ["x", 1]}`,
		`{"schemaVersion": 1, "skills": ["../x"]}`,
		`{"schemaVersion": 1, "agentkit": []}`,
		`{"schemaVersion": 1, "agentkit": {"kit": "Bad Kit"}}`,
		`{"schemaVersion": 1, "agentkit": {"channel": "nightly"}}`,
		`[]`,
	} {
		f.write(file, body)
		if _, err := f.root.LoadNewDefaults(); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestTargetOf(t *testing.T) {
	for runtime, want := range map[string]string{"claude": "claude-code", "codex": "codex"} {
		if got, ok := TargetOf(runtime); !ok || got != want {
			t.Errorf("%s: %q %v", runtime, got, ok)
		}
	}
	if _, ok := TargetOf("grok"); ok {
		t.Error("grok has no runtime")
	}
}

func TestNewRepoExclude(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.home, "app")
	f.write(filepath.Join(dir, ".claude/rules/team.md"), "mine")
	// A decomposed name (as a Mac app may write it): git composes it, so the snapshot must too.
	nfd := ".claude/rules/caf" + "e\u0301" + ".md"
	f.write(filepath.Join(dir, nfd), "mine")
	f.write(filepath.Join(dir, "notes.txt"), "mine")
	f.git(dir, "init", "-q")
	pre, err := SnapshotAgentPaths(dir)
	nfc := ".claude/rules/caf\u00e9.md"
	if runtime.GOOS != "darwin" {
		nfc = nfd
	}
	if err != nil || !slices.Equal(pre, []string{".claude", ".claude/rules", nfc, ".claude/rules/team.md"}) {
		t.Fatalf("snapshot: %q %v", pre, err)
	}
	gitDir := filepath.Join(dir, ".git")
	if err := WriteNewRecord(gitDir, &NewRecord{Preexisting: pre}); err != nil {
		t.Fatal(err)
	}
	rec, err := ReadNewRecord(gitDir)
	if err != nil || rec == nil || !slices.Equal(rec.Preexisting, pre) || rec.Done {
		t.Fatalf("record: %+v %v", rec, err)
	}
	// What ak and acp would add.
	f.write(filepath.Join(dir, ".claude/skills/ak-cook/SKILL.md"), "x")
	f.write(filepath.Join(dir, ".claude/skills/ak-cook/references/a.md"), "x")
	f.write(filepath.Join(dir, ".claude/agents/planner.md"), "x")
	f.write(filepath.Join(dir, ".claude/rules/ak-rule.md"), "x")
	f.write(filepath.Join(dir, ".claude/settings.json"), "{}")
	f.write(filepath.Join(dir, ".agentkit/config.yaml"), "x")
	f.write(filepath.Join(dir, ".agentkit/adapters/claude-code/engineer/x.json"), "{}")
	f.write(filepath.Join(dir, ".agents/plugins/p/plugin.json"), "{}")
	f.symlink(f.cp+"/skills/herdr", filepath.Join(dir, ".claude/skills/herdr"))

	lines, err := NewRepoExcludeLines(dir, rec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/.agentkit/", "/.agents/plugins/p/", "/.claude/agents/planner.md", "/.claude/rules/ak-rule.md",
		"/.claude/settings.json", "/.claude/skills/ak-cook/", "/.claude/skills/herdr"}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines:\n%s", strings.Join(lines, "\n"))
	}
	exclude := filepath.Join(gitDir, "info", "exclude")
	f.write(exclude, "# git's own comment\n*.mine\n")
	if err := WriteNewRepoExclude(gitDir, lines); err != nil {
		t.Fatal(err)
	}
	if err := WriteNewRepoExclude(gitDir, lines); err != nil {
		t.Fatal(err)
	}
	text := f.Read(exclude)
	if strings.Count(text, newBlockStart) != 1 || !strings.HasPrefix(text, "# git's own comment\n*.mine\n") {
		t.Fatalf("exclude:\n%s", text)
	}
	if st := f.status(dir); !strings.Contains(st, "?? .claude/rules/team.md\n?? notes.txt\n") || strings.Count(st, "\n") != 3 {
		t.Fatalf("status:\n%s", st)
	}
	if err := RemoveNewRepoExclude(gitDir); err != nil {
		t.Fatal(err)
	}
	if text := f.Read(exclude); text != "# git's own comment\n*.mine\n" || Exists(filepath.Join(gitDir, "acp-new.json")) {
		t.Fatalf("after remove:\n%s", text)
	}
	if rec, err := ReadNewRecord(gitDir); rec != nil || err != nil {
		t.Fatalf("record after remove: %+v %v", rec, err)
	}
	if err := WriteNewRecord(gitDir, &NewRecord{Done: true}); err != nil {
		t.Fatal(err)
	}
	if rec, err := ReadNewRecord(gitDir); err != nil || !rec.Done || len(rec.Preexisting) != 0 {
		t.Fatalf("done record: %+v %v", rec, err)
	}
	// A block whose end marker was edited away loses only its start line.
	f.write(exclude, "a\n"+newBlockStart+"\n/.agentkit/\nb\n")
	if err := RemoveNewRepoExclude(gitDir); err != nil {
		t.Fatal(err)
	}
	if text := f.Read(exclude); text != "a\n/.agentkit/\nb\n" {
		t.Fatalf("unterminated block:\n%s", text)
	}
}
