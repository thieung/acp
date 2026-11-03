package core

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// ProjectFor finds the fleet project a path belongs to and returns (project, main, here). A folder
// nested in a repo resolves to the repo's project, as in Python; nested projects are reached by id.
func (r *Root) ProjectFor(path string) (*Project, string, string, error) {
	main, here := CheckoutOf(path)
	candidates := []string{main}
	if _, err := GitRead(main, "rev-parse", "--git-dir"); err != nil {
		for d := filepath.Dir(main); ; d = filepath.Dir(d) {
			candidates = append(candidates, d)
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	fleet, err := r.Fleet()
	if err != nil {
		return nil, main, here, err
	}
	for _, p := range fleet {
		if p.Repo == "" {
			continue
		}
		repo := resolvePath(p.Repo)
		for _, c := range candidates {
			if c == repo {
				if repo != main {
					here = repo
				}
				return p, repo, here, nil
			}
		}
	}
	return nil, main, here, nil
}

// NativeCounts counts AgentKit skills, agents and hooks of one target in a main checkout, plus
// leftover ak-* skills the recorded selection no longer has.
func (e *Engine) NativeCounts(main, target string, recorded *Object) *Object {
	entries := func(folder, prefix string) []string {
		var out []string
		for _, name := range sortedNames(folder) {
			// Personal skills named ak-* are acp's links into skills/, not AgentKit's.
			if strings.HasPrefix(name, prefix) && !strings.HasPrefix(name, ".") &&
				!IsManaged(filepath.Join(folder, name), []string{e.Root.Skills()}, "") {
				out = append(out, name)
			}
		}
		return out
	}
	native := filepath.Join(main, ".codex")
	if target == "claude-code" {
		native = filepath.Join(main, ".claude")
	}
	selection := setOf(recorded.Object("skill_selection").Strings("skills"))
	skills := entries(filepath.Join(main, SkillDirs[Targets[target]]), "ak-")
	count, stale := 0, []string{}
	for _, s := range skills {
		if len(selection) == 0 || selection[s] {
			count++
		} else {
			stale = append(stale, s)
		}
	}
	sort.Strings(stale)
	return Obj("skills", count, "stale", stale, "agents", len(entries(filepath.Join(native, "agents"), "")),
		"hooks", len(entries(filepath.Join(native, "hooks"), "")))
}

// ProjectStatus is everything the fleet table, --json and the dashboard show for one project.
func (e *Engine) ProjectStatus(p *Project) *Object {
	var repo any
	if p.Repo != "" {
		repo = p.Repo
	}
	skills := NewObject()
	for _, r := range Runtimes {
		skills.Set(r, p.Personal(r))
	}
	personal := Obj("linked", 0, "total", 0, "skills", skills)
	info := Obj("id", p.ID, "repo", repo, "agentkit", nil, "personal", personal,
		"worktrees", Obj("linked", 0, "total", 0))
	notes := []string{}
	if p.Repo == "" || !isDir(p.Repo) {
		shown := p.Repo
		if shown == "" {
			shown = "not in projects.local.json"
		}
		info.Set("notes", append(notes, "repo path is missing: "+shown))
		info.Set("healthy", false)
		return info
	}
	main := resolvePath(p.Repo)
	if kits := p.Kits(); kits != nil {
		channel := p.Channel()
		for _, bad := range p.BadExtraKits() {
			notes = append(notes, "! "+bad)
		}
		var block *Object
		extras := []any{}
		for _, k := range kits {
			kitInfo, kitNotes := e.kitStatus(p, k, channel, main, len(kits) > 1)
			notes = append(notes, kitNotes...)
			if k.Primary {
				block = kitInfo
			} else {
				extras = append(extras, kitInfo)
			}
		}
		if len(extras) > 0 {
			block.Set("extra_kits", extras)
		}
		info.Set("agentkit", block)
	}
	log := NewLog("")
	e.SyncCheckout(p, main, main, log, LinkOpts{Dry: true})
	total := 0
	for _, r := range Runtimes {
		total += len(p.Personal(r))
	}
	missing := 0
	for _, c := range log.Changes {
		if strings.HasPrefix(c, "+") {
			missing++
		}
	}
	personal.Set("linked", total-missing)
	personal.Set("total", total)
	for _, i := range log.Issues {
		notes = append(notes, "! "+i)
	}
	notes = append(notes, log.Changes...)
	trees := Worktrees(main)
	linked := 0
	for _, wt := range trees {
		wlog := NewLog("")
		e.SyncCheckout(p, main, wt, wlog, LinkOpts{Dry: true})
		if clean := len(wlog.Changes) == 0 && len(wlog.Issues) == 0; clean {
			linked++
		}
	}
	info.Set("worktrees", Obj("linked", linked, "total", len(trees)))
	if linked < len(trees) {
		notes = append(notes, fmt.Sprintf("%d worktree(s) need links", len(trees)-linked))
	}
	info.Set("notes", notes)
	info.Set("healthy", len(notes) == 0)
	return info
}

func pinOrNone(pin string) string {
	if pin == "" {
		return "None"
	}
	return pin
}

// kitStatus is one kit's pin, selection and per-target install state, plus a note for each
// target that is not at the pin. named prefixes the notes with the kit when a project has several.
func (e *Engine) kitStatus(p *Project, k KitSpec, channel, main string, named bool) (*Object, []string) {
	var notes []string
	pin := e.Root.PinnedVersion(channel, k.Name)
	targets := []any{}
	for _, target := range p.AKTargets() {
		if _, ok := Targets[target]; !ok {
			if k.Primary {
				notes = append(notes, fmt.Sprintf("! unknown AgentKit target %q", target))
			}
			continue
		}
		recorded := InstallManifest(main, target, k.Name)
		var have any
		if v := InstalledVersion(recorded); v != "" {
			have = v
		} else if recorded.Len() > 0 {
			have = "2.x"
		}
		same := e.SelectionMatches(k, recorded)
		missing := MissingKitFiles(main, target, k.Name)
		ok := have == any(pin) && pin != "" && same && len(e.StoreLinks(main, Targets[target])) == 0 &&
			len(missing) == 0
		t := Obj("target", target, "installed", have, "ok", ok, "selection_ok", same, "files_missing", len(missing))
		counts := e.NativeCounts(main, target, recorded)
		for _, key := range counts.Keys() {
			v, _ := counts.Get(key)
			t.Set(key, v)
		}
		targets = append(targets, t)
		if !ok {
			shown := "not installed"
			if have != nil {
				shown = have.(string)
			}
			note := fmt.Sprintf("%s: %s, pinned %s", target, shown, pinOrNone(pin))
			if named {
				note = k.Name + " " + note
			}
			if !same && recorded.Len() > 0 {
				note += ", selection differs"
			}
			if len(missing) > 0 {
				note += fmt.Sprintf(", %d recorded file(s) missing", len(missing))
			}
			notes = append(notes, note)
		}
	}
	var wanted any = "all"
	if !k.All {
		wanted = k.Skills
	}
	var pinned any
	if pin != "" {
		pinned = pin
	}
	return Obj("kit", k.Name, "channel", channel, "pin", pinned, "skills", wanted, "exclude", k.Exclude,
		"targets", targets), notes
}
