package commands

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/thieung/acp/internal/core"
)

// newSettings are acp new's merged choices: flags, then project-defaults.json, then acp's defaults.
type newSettings struct {
	kit, channel string
	runtimes     []string // in core.Runtimes order
	targets      []string
	skills       []string
	akSkills     []string // AgentKit skills of the kit; nil selects all
	defaults     string   // project-defaults.json when it set anything, else ""
}

func newChoices(root *core.Root, a *Args) (newSettings, error) {
	d, err := root.LoadNewDefaults()
	if err != nil {
		return newSettings{}, err
	}
	s := newSettings{kit: a.Str("kit"), channel: a.Str("channel"), akSkills: d.AKSkills, defaults: d.File}
	if !a.Given("kit") && d.Kit != "" {
		s.kit = d.Kit
	}
	if !a.Given("channel") && d.Channel != "" {
		s.channel = d.Channel
	}
	runtimes := []string{"claude"}
	if a.Given("runtimes") {
		runtimes = splitList(a.Str("runtimes"))
		if len(runtimes) == 0 {
			return newSettings{}, usageErr("--runtimes is empty (choose from %s)", strings.Join(core.Runtimes, ", "))
		}
	} else if len(d.Runtimes) > 0 {
		runtimes = d.Runtimes
	}
	for _, r := range runtimes {
		if _, ok := core.TargetOf(r); !ok {
			return newSettings{}, usageErr("unknown runtime %q (choose from %s)", r, strings.Join(core.Runtimes, ", "))
		}
	}
	for _, r := range core.Runtimes {
		if slices.Contains(runtimes, r) {
			t, _ := core.TargetOf(r)
			s.runtimes, s.targets = append(s.runtimes, r), append(s.targets, t)
		}
	}
	if !core.ValidName(s.kit) {
		return newSettings{}, usageErr("invalid kit name %q", s.kit)
	}
	if s.channel != "stable" && s.channel != "beta" {
		return newSettings{}, usageErr("--channel must be stable or beta, not %q", s.channel)
	}
	if d.AKPreset != "" {
		// Resolved against the final kit, so --kit and a kit-specific preset are checked together.
		all, list, err := core.ResolvePresets(d.AKPreset, s.kit)
		if err != nil {
			return newSettings{}, fail("%s: agentkit.skills: %v", d.File, err)
		}
		if !all {
			s.akSkills = list
		}
	}
	for _, name := range append(slices.Clone(d.Skills), splitList(a.Str("skills"))...) {
		if slices.Contains(s.skills, name) {
			continue
		}
		if !core.ValidName(name) {
			return newSettings{}, fail("invalid personal skill name %q", name)
		}
		if _, err := os.Stat(filepath.Join(root.Skills(), name, "SKILL.md")); err != nil {
			return newSettings{}, fail("personal skill '%s' is missing from %s", name, root.Skills())
		}
		s.skills = append(s.skills, name)
	}
	return s, nil
}

func splitList(s string) []string {
	var out []string
	for _, it := range strings.Split(s, ",") {
		if it = strings.TrimSpace(it); it != "" {
			out = append(out, it)
		}
	}
	return out
}

func (s newSettings) manifest(id string) *core.Object {
	skills := []any{}
	for _, name := range s.skills {
		skills = append(skills, core.Obj("name", name, "runtimes", core.AnyList(s.runtimes)))
	}
	var akSkills any = core.AllSkills
	if s.akSkills != nil {
		akSkills = core.AnyList(s.akSkills)
	}
	return core.Obj("id", id, "skills", skills, "agentkit", core.Obj("kit", s.kit, "channel", s.channel,
		"targets", core.AnyList(s.targets), "skills", akSkills))
}

// savedChoices are the settings of a project acp new started: a resumed run finishes what the
// first run chose, so flags that would change it are refused.
func savedChoices(root *core.Root, id string, a *Args) (newSettings, error) {
	p, err := root.ByID(id)
	if err != nil {
		return newSettings{}, err
	}
	s := newSettings{kit: p.Kit(), channel: p.Channel()}
	if all, list := p.AKSkills(); !all {
		s.akSkills = list
	}
	for _, t := range p.AKTargets() {
		s.targets, s.runtimes = append(s.targets, t), append(s.runtimes, core.Targets[t])
	}
	for _, r := range s.runtimes {
		for _, name := range p.Personal(r) {
			if !slices.Contains(s.skills, name) {
				s.skills = append(s.skills, name)
			}
		}
	}
	// The same flags again (the dashboard's retry sends them) are fine; other values are not.
	changed := ""
	switch {
	case a.Given("kit") && a.Str("kit") != s.kit:
		changed = "kit"
	case a.Given("channel") && a.Str("channel") != s.channel:
		changed = "channel"
	case a.Given("runtimes") && !sameSet(splitList(a.Str("runtimes")), s.runtimes):
		changed = "runtimes"
	}
	for _, name := range splitList(a.Str("skills")) {
		if changed == "" && !slices.Contains(s.skills, name) {
			changed = "skills"
		}
	}
	if changed != "" {
		return newSettings{}, usageErr("%s is being finished with the settings it was started with; drop --%s or edit projects/%s.json", id, changed, id)
	}
	return s, nil
}

func sameSet(a, b []string) bool {
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	for _, x := range b {
		if !slices.Contains(a, x) {
			return false
		}
	}
	return true
}

// newState reads what acp new left in a repo: the record when it git-inited the repo, nil otherwise
// (also before git init).
func newState(main string) (*core.NewRecord, error) {
	gitDir, err := core.GitDir(main)
	if err != nil {
		return nil, nil
	}
	return core.ReadNewRecord(gitDir)
}

// checkFleet refuses an id or a repo the fleet already has. It returns the id to resume when the
// repo is in the fleet because acp new started it and that run did not finish.
func checkFleet(root *core.Root, id string, idGiven bool, main string, rec *core.NewRecord) (string, error) {
	repos, err := root.OverlayRepos()
	if err != nil {
		return "", err
	}
	owner := ""
	for other, path := range repos {
		if resolve(path) == main {
			owner = other
		}
	}
	if owner != "" {
		file, _ := core.SafeJoin(root.Projects(), owner+".json")
		switch {
		case rec != nil && !rec.Done && core.Exists(file) && idGiven && id != owner:
			return "", fail("acp new started %s as %s; run it again without --id to finish it", main, owner)
		case rec != nil && !rec.Done && core.Exists(file):
			return owner, nil
		}
		return "", fail("%s is already in the fleet as %s; run: acp sync %s", main, owner, owner)
	}
	if file, _ := core.SafeJoin(root.Projects(), id+".json"); core.Exists(file) {
		return "", fail("%s is already in the fleet", id)
	}
	return "", nil
}

func cmdNew(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	np, err := core.PlanNewPath(root, a.Str("path"))
	if err != nil {
		return 0, err
	}
	id := a.Str("id")
	if id == "" {
		id = defaultID(np.Main)
	}
	if !core.ValidName(id) {
		return 0, fail("invalid project id %q (lowercase letters, digits, '.', '_' and '-'); pass --id", id)
	}
	rec, err := newState(np.Main)
	if err != nil {
		return 0, err
	}
	resumeID, err := checkFleet(root, id, a.Given("id"), np.Main, rec)
	if err != nil {
		return 0, err
	}
	var s newSettings
	if resumeID != "" {
		id = resumeID
		s, err = savedChoices(root, id, a)
	} else {
		s, err = newChoices(root, a)
	}
	if err != nil {
		return 0, err
	}
	noSync := a.Bool("no-sync")
	pin := root.PinnedVersion(s.channel, s.kit)
	if !noSync {
		if pin == "" {
			return 0, fail("no %s/%s pin in agentkit-releases.lock.json; run `acp upgrade` first (or pass --no-sync)", s.channel, s.kit)
		}
		if _, err := core.AkBin(); err != nil {
			return 0, err
		}
	}
	manifest := s.manifest(id)
	if resumeID != "" {
		if p, err := root.ByID(id); err == nil {
			manifest = p.Manifest
		}
	}
	// acp new owns the exclude block of a repo it git-inits (now, or in the run being finished).
	owned := np.Mode != core.ModeRepo || (rec != nil && !rec.Done)
	hooks := hooksMissing(s.runtimes)
	e.Set("project", id)
	e.Set("repo", np.Main)
	e.Set("mode", string(np.Mode))
	e.Set("resume", resumeID != "")
	e.Set("manifest", manifest)
	e.Set("runtimes", core.AnyList(s.runtimes))
	e.Set("defaults", nilIfEmpty(s.defaults))
	e.Set("hooks_installed", len(hooks) == 0)
	e.Set("hooks_missing", core.AnyList(hooks))
	if a.Bool("dry-run") {
		e.PrintLog(newPlanLog(root, s, np, id, pin, resumeID != "", owned, noSync), false)
		text, _ := core.MarshalJSON(manifest)
		e.Printf("%s", text)
		printHooksWarning(e, hooks)
		e.Printf("%s\n", e.Paint("2", "dry run: nothing was written"))
		return 0, nil
	}

	lock, err := lockRoot(e, root)
	if err != nil {
		return 0, err
	}
	again, err := core.PlanNewPath(root, a.Str("path"))
	if err == nil && again != np {
		err = fail("%s changed while acp new was starting; run it again", a.Str("path"))
	}
	if err == nil {
		rec, err = newState(np.Main)
	}
	resumed := ""
	if err == nil {
		resumed, err = checkFleet(root, id, a.Given("id") || resumeID != "", np.Main, rec)
	}
	if err == nil && resumed != resumeID {
		err = fail("%s changed while acp new was starting; run it again", a.Str("path"))
	}
	if err == nil && np.Mode != core.ModeRepo {
		err = gitInitNew(e, np)
	}
	if err == nil && resumeID == "" {
		_, err = registerProject(e, root, np.Main, id, manifest, true)
	}
	lock.Release()
	e.Set("resume", resumeID != "")
	if err != nil {
		return 0, err
	}
	if resumeID != "" {
		e.Printf("finishing %s -> %s\n", id, np.Main)
	}

	next := "acp sync " + id
	if owned {
		next = "acp new " + core.ShellQuote(np.Main)
	}
	if noSync {
		if owned {
			e.Printf("AgentKit, links and the exclude block wait; finish with: %s\n", next)
		}
		printHooksWarning(e, hooks)
		return 0, nil
	}
	code := 0
	p, err := root.ByID(id)
	if err != nil {
		return 0, err
	}
	if syncProjects(e, root, []*core.Project{p}, core.SyncOpts{Native: true}) {
		code = 1
	}
	if owned {
		if err := hideNewPaths(e, root, np.Main, code == 0); err != nil {
			e.Printf("the exclude block was not written; fix the issue below, then run: %s\n", next)
			return 0, err
		}
	}
	if code != 0 {
		e.Printf("AgentKit or links did not finish; fix the issue above, then run: %s\n", next)
	}
	printHooksWarning(e, hooks)
	return code, nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// gitInitNew creates the folder when it is missing, runs git init, and records the agent paths
// already there (as git lists them) so they are never hidden.
func gitInitNew(e *Env, np core.NewPath) error {
	if np.Mode == core.ModeCreate {
		if err := os.Mkdir(np.Real, 0o755); err != nil {
			return err
		}
		e.Printf("+ created %s\n", np.Real)
	}
	if _, err := core.GitWrite(np.Real, "init", "-q"); err != nil {
		return err
	}
	gitDir, err := core.GitDir(np.Real)
	if err != nil {
		return err
	}
	if want := filepath.Join(np.Real, ".git"); resolve(gitDir) != resolve(want) {
		return fail("git init put the repo in %s, not %s; the project was not added", gitDir, want)
	}
	pre, err := core.SnapshotAgentPaths(np.Real)
	if err != nil {
		return err
	}
	if err := core.WriteNewRecord(gitDir, &core.NewRecord{Preexisting: pre}); err != nil {
		return err
	}
	e.Printf("+ git init %s\n", np.Real)
	return nil
}

// hideNewPaths writes what AgentKit and acp created into the repo's info/exclude, under the repo
// lock, and marks the record done when the run finished.
func hideNewPaths(e *Env, root *core.Root, main string, done bool) error {
	lock, err := lockRepo(e, root, main, linkLockWait)
	if err != nil {
		return fail("%s is busy syncing; try again later", main)
	}
	defer lock.Release()
	gitDir, err := core.GitDir(main)
	if err != nil {
		return err
	}
	rec, err := core.ReadNewRecord(gitDir)
	if err != nil || rec == nil {
		return err
	}
	lines, err := core.NewRepoExcludeLines(main, rec)
	if err != nil {
		return err
	}
	if err := core.WriteNewRepoExclude(gitDir, lines); err != nil {
		return err
	}
	if done {
		rec.Done = true
		if err := core.WriteNewRecord(gitDir, rec); err != nil {
			return err
		}
	}
	e.Set("excluded", core.AnyList(lines))
	e.Printf("+ hid %d path(s) AgentKit and acp created in .git/info/exclude (local; your own files stay visible)\n", len(lines))
	return nil
}

// newPlanLog is the dry run: every step acp new would take, and what would stop a link.
func newPlanLog(root *core.Root, s newSettings, np core.NewPath, id, pin string, resume, owned, noSync bool) *core.Log {
	log := core.NewLog(id)
	switch {
	case resume:
		log.Change("~ finish %s: it is in the fleet and acp new git-inited it", np.Main)
	case np.Mode == core.ModeCreate:
		log.Change("+ create %s", np.Real)
		fallthrough
	case np.Mode == core.ModeInit:
		log.Change("+ git init %s (no commit)", np.Real)
	}
	if !resume {
		log.Change("+ projects/%s.json and projects.local.json: %s -> %s", id, id, np.Main)
	}
	switch {
	case noSync && owned:
		log.Change("~ --no-sync: AgentKit, links and the exclude block wait for `acp new %s`", core.ShellQuote(np.Main))
	case noSync:
		log.Change("~ --no-sync: AgentKit and links wait for `acp sync %s`", id)
	default:
		only := ""
		if s.akSkills != nil {
			only = ": " + strings.Join(s.akSkills, ", ")
		}
		for _, t := range s.targets {
			log.Change("+ AgentKit %s %s (%s)%s", s.kit, pin, t, only)
		}
		for _, r := range s.runtimes {
			for _, name := range s.skills {
				rel := core.SkillDirs[r] + "/" + name
				dest := filepath.Join(np.Real, filepath.FromSlash(rel))
				if fi, err := os.Lstat(dest); err == nil && fi.Mode()&os.ModeSymlink == 0 {
					log.Issue("%s is a real directory; acp will leave it alone and not link %s", rel, name)
					continue
				} else if err != nil && !errors.Is(err, os.ErrNotExist) {
					log.Issue("%s: %v", rel, err)
					continue
				}
				log.Change("+ %s -> %s", rel, filepath.Join(root.Skills(), name))
			}
		}
	}
	if owned && !noSync {
		log.Change("+ hide what AgentKit and acp create in .git/info/exclude (local; files already there stay visible)")
	}
	return log
}

func printHooksWarning(e *Env, missing []string) {
	if len(missing) == 0 {
		return
	}
	e.Printf("%s session-start hooks are not installed for %s; worktrees added later will not link themselves. Run: acp hooks install\n",
		e.Paint("33", "!"), strings.Join(missing, ", "))
}
