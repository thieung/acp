package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/thieung/acp/internal/core"
)

var nonID = regexp.MustCompile(`[^a-z0-9]+`)

// checkRepoPath refuses paths acp must never register: a missing folder, /, HOME and anything in
// the control plane itself.
func checkRepoPath(root *core.Root, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if !isDir(abs) {
		return "", fail("%s is not an existing directory", path)
	}
	real, _ := filepath.EvalSymlinks(abs)
	home, _ := filepath.EvalSymlinks(core.HomeDir())
	switch {
	case real == "/":
		return "", fail("refusing to add / to the fleet")
	case real == home:
		return "", fail("refusing to add your home folder to the fleet; pass a repo inside it")
	case core.Within(real, root.Dir):
		return "", fail("%s is inside the control plane %s", path, root.Dir)
	}
	return real, nil
}

func cmdAdd(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	path := a.Str("path")
	if path == "" {
		path = e.Cwd
	}
	if path, err = checkRepoPath(root, path); err != nil {
		return 0, err
	}
	main, _ := core.CheckoutOf(path)
	if main, err = checkRepoPath(root, main); err != nil {
		return 0, err
	}
	id := a.Str("id")
	if id == "" {
		id = defaultID(main)
	}
	if !core.ValidName(id) {
		return 0, fail("invalid project id %q (lowercase letters, digits, '.', '_' and '-')", id)
	}
	manifest := core.Obj("id", id, "skills", []any{})
	if !a.Bool("no-agentkit") {
		targets, err := targetList(a.Str("targets"))
		if err != nil {
			return 0, err
		}
		if !core.ValidName(a.Str("kit")) {
			return 0, usageErr("invalid kit name %q", a.Str("kit"))
		}
		manifest.Set("agentkit", core.Obj("kit", a.Str("kit"), "channel", a.Str("channel"),
			"targets", core.AnyList(targets), "skills", "all"))
	}
	file, _ := core.SafeJoin(root.Projects(), id+".json")
	e.Set("project", id)
	e.Set("repo", main)
	e.Set("manifest", manifest)
	if core.Exists(file) {
		return 0, fail("%s is already in the fleet", id)
	}
	if a.Bool("dry-run") {
		text, _ := core.MarshalJSON(manifest)
		e.Printf("would add %s -> %s\n%s", id, main, text)
		e.Printf("%s\n", e.Paint("2", "dry run: nothing was written"))
		return 0, nil
	}
	return registerProject(e, root, main, id, manifest, a.Bool("no-sync"))
}

// defaultID is a project id from a folder name.
func defaultID(main string) string {
	return strings.Trim(nonID.ReplaceAllString(strings.ToLower(filepath.Base(main)), "-"), "-")
}

// registerProject writes the manifest and the overlay entry under the root lock, then syncs the
// project unless noSync. It returns 1 when the sync left issues.
func registerProject(e *Env, root *core.Root, main, id string, manifest *core.Object, noSync bool) (int, error) {
	file, _ := core.SafeJoin(root.Projects(), id+".json")
	lock, err := lockRoot(e, root)
	if err != nil {
		return 0, err
	}
	repos, err := root.OverlayRepos()
	if err == nil && core.Exists(file) {
		err = fail("%s is already in the fleet", id)
	}
	if err == nil {
		err = core.WriteJSON(file, manifest, 0o644)
	}
	if err == nil {
		repos[id] = main
		err = root.SaveOverlay(repos)
	}
	lock.Release()
	if err != nil {
		return 0, err
	}
	e.Printf("added %s -> %s  (edit projects/%s.json for personal skills)\n", id, main, id)
	if noSync {
		return 0, nil
	}
	p, err := root.ByID(id)
	if err != nil {
		return 0, err
	}
	if syncProjects(e, root, []*core.Project{p}, core.SyncOpts{Native: true}) {
		return 1, nil
	}
	return 0, nil
}

// targetList splits --targets and checks every name.
func targetList(s string) ([]string, error) {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if _, ok := core.Targets[t]; !ok {
			return nil, usageErr("unknown AgentKit target %q (choose from %s)", t, strings.Join(core.TargetNames, ", "))
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, usageErr("--targets is empty")
	}
	return out, nil
}

func cmdRemove(e *Env, a *Args) (int, error) {
	id := a.Str("id")
	if !core.ValidName(id) {
		return 0, fail("invalid project id %q", id)
	}
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	dry := !a.Bool("yes")
	if !dry {
		lock, err := lockRoot(e, root)
		if err != nil {
			return 0, err
		}
		defer lock.Release()
	}
	repos, err := root.OverlayRepos()
	if err != nil {
		return 0, err
	}
	manifest, _ := core.SafeJoin(root.Projects(), id+".json")
	if p, err := root.ByID(id); err == nil {
		manifest = p.File
	}
	repo, inOverlay := repos[id]
	if !inOverlay && !core.Exists(manifest) {
		return 0, fail("no project '%s' (see `acp status`)", id)
	}
	removed := []string{}
	if repo != "" {
		repo = resolve(repo)
		shared := false
		for k, v := range repos {
			shared = shared || (k != id && resolve(v) == repo)
		}
		switch {
		case shared:
			e.Printf("%s is also used by another project id; its links stay\n", repo)
		case isDir(repo):
			if err := unlinkRepo(e, root, repo, dry, &removed); err != nil {
				return 0, err
			}
			if err := dropNewExclude(e, root, repo, dry); err != nil {
				return 0, err
			}
		}
	}
	if core.Exists(manifest) {
		rel, _ := filepath.Rel(root.Dir, manifest)
		e.Printf("- %s\n", rel)
	}
	if inOverlay {
		e.Printf("- projects.local.json: %s\n", id)
	}
	e.Set("project", id)
	e.Set("unlinked", removed)
	e.Set("applied", !dry)
	if dry {
		e.Printf("%s\n", e.Paint("2", "dry run: pass --yes to remove"))
		return 0, nil
	}
	if err := os.Remove(manifest); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	delete(repos, id)
	if err := root.SaveOverlay(repos); err != nil {
		return 0, err
	}
	e.Printf("removed %s; native AgentKit files in the repo were left in place\n", id)
	return 0, nil
}

// unlinkRepo removes acp's links from a main checkout and each of its worktrees.
func unlinkRepo(e *Env, root *core.Root, repo string, dry bool, removed *[]string) error {
	if !dry {
		lock, err := lockRepo(e, root, repo, linkLockWait)
		if err != nil {
			return fail("%s is busy syncing; try again later", repo)
		}
		defer lock.Release()
	}
	eng := core.NewEngine(root)
	for _, checkout := range append([]string{repo}, core.Worktrees(repo)...) {
		rels, err := eng.UnlinkAll(repo, checkout, dry)
		for _, rel := range rels {
			line := fmt.Sprintf("%s/%s", checkout, rel)
			*removed = append(*removed, line)
			e.Printf("- %s\n", line)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// dropNewExclude takes out the info/exclude block `acp new` wrote when it git-inited the repo. An
// unreadable record is reported and removed with the block.
func dropNewExclude(e *Env, root *core.Root, repo string, dry bool) error {
	gitDir, err := core.GitDir(repo)
	if err != nil {
		return nil
	}
	if rec, err := core.ReadNewRecord(gitDir); err != nil {
		e.Printf("%s %v; removing it and the acp new block anyway\n", e.Paint("33", "!"), err)
	} else if rec == nil {
		return nil
	}
	e.Printf("- %s: the block acp new wrote\n", filepath.Join(gitDir, "info", "exclude"))
	if dry {
		return nil
	}
	lock, err := lockRepo(e, root, repo, linkLockWait)
	if err != nil {
		return fail("%s is busy syncing; try again later", repo)
	}
	defer lock.Release()
	return core.RemoveNewRepoExclude(gitDir)
}

func resolve(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}
