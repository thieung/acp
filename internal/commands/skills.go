package commands

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/thieung/acp/internal/core"
)

// personalCatalog is every skill folder with a SKILL.md in the control plane.
func personalCatalog(root *core.Root) []string {
	files, _ := filepath.Glob(filepath.Join(root.Skills(), "*", "SKILL.md"))
	out := []string{}
	for _, f := range files {
		out = append(out, filepath.Base(filepath.Dir(f)))
	}
	sort.Strings(out)
	return out
}

func defaultRuntimes(p *core.Project) []string {
	if ak := p.AK(); ak != nil && ak.Has("targets") {
		out := []string{}
		for _, t := range p.AKTargets() {
			if rt, ok := core.Targets[t]; ok {
				out = append(out, rt)
			}
		}
		return out
	}
	return append([]string(nil), core.Runtimes...)
}

// names splits a comma-separated flag and checks each name.
func names(value string) ([]string, error) {
	out := []string{}
	for _, n := range strings.Split(value, ",") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		if !core.ValidName(n) {
			return nil, usageErr("invalid name %q", n)
		}
		out = append(out, n)
	}
	return out, nil
}

// skillKind says whether a name is an AgentKit skill of one of the project's kits (kit names it)
// or a personal skill. A kit catalog is checked first, in the project's kit order; an unknown
// "ak-" name belongs to the primary kit.
func skillKind(eng *core.Engine, root *core.Root, p *core.Project, name string) (kind, kit string, err error) {
	for _, k := range p.Kits() {
		if contains(eng.KitCatalog(k.Name), name) {
			return "agentkit", k.Name, nil
		}
	}
	if contains(personalCatalog(root), name) {
		return "personal", "", nil
	}
	if strings.HasPrefix(name, "ak-") && p.AK() != nil {
		return "agentkit", p.Kit(), nil
	}
	return "", "", fail("unknown skill '%s' (not in a kit catalog or %s)", name, root.Skills())
}

func without(list []string, name string) []string {
	out := []string{}
	for _, s := range list {
		if s != name {
			out = append(out, s)
		}
	}
	return out
}

// editSkills returns a copy of the manifest with skills added and removed; nothing is written.
func editSkills(eng *core.Engine, root *core.Root, p *core.Project, add, remove, runtimes []string) (*core.Object, error) {
	manifest := core.Clone(p.Manifest).(*core.Object)
	// Kit entries of the copy, so edits land in it and not in p.Manifest.
	entries := map[string]*core.Object{}
	for _, k := range (&core.Project{ID: p.ID, Manifest: manifest}).Kits() {
		entries[k.Name] = k.Entry
	}
	if !manifest.Has("skills") {
		manifest.Set("skills", []any{})
	}
	type edit struct {
		name   string
		adding bool
	}
	var edits []edit
	for _, n := range add {
		edits = append(edits, edit{n, true})
	}
	for _, n := range remove {
		edits = append(edits, edit{n, false})
	}
	for _, ed := range edits {
		kind, kit, err := skillKind(eng, root, p, ed.name)
		if err != nil {
			return nil, err
		}
		if kind == "agentkit" {
			ak := entries[kit]
			exclude := without(ak.Strings("exclude"), ed.name)
			v, _ := ak.Get("skills")
			if s, ok := v.(string); ok && s == "all" || v == nil {
				if !ed.adding {
					exclude = append(exclude, ed.name)
				}
			} else {
				list := ak.Strings("skills")
				if ed.adding && !contains(list, ed.name) {
					list = append(list, ed.name)
					sort.Strings(list)
				} else if !ed.adding {
					list = without(list, ed.name)
				}
				ak.Set("skills", core.AnyList(list))
			}
			if len(exclude) > 0 {
				sort.Strings(exclude)
				ak.Set("exclude", core.AnyList(dedupe(exclude)))
			} else {
				ak.Delete("exclude")
			}
			continue
		}
		personal := manifest.Array("skills")
		var entry *core.Object
		idx := -1
		for i, it := range personal {
			if s, ok := it.(*core.Object); ok && s.String("name") == ed.name {
				entry, idx = s, i
				break
			}
		}
		switch {
		case ed.adding && entry != nil:
			have := entry.Strings("runtimes")
			for _, r := range runtimes {
				if !contains(have, r) {
					have = append(have, r)
				}
			}
			entry.Set("runtimes", core.AnyList(have))
		case ed.adding:
			manifest.Set("skills", append(personal, core.Obj("name", ed.name, "runtimes", core.AnyList(runtimes))))
		case entry != nil:
			left := []string{}
			for _, r := range entry.Strings("runtimes") {
				if !contains(runtimes, r) {
					left = append(left, r)
				}
			}
			if len(left) > 0 {
				entry.Set("runtimes", core.AnyList(left))
			} else {
				manifest.Set("skills", append(personal[:idx:idx], personal[idx+1:]...))
			}
		}
	}
	return manifest, nil
}

func dedupe(sorted []string) []string {
	out := []string{}
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

func jsonLines(v any) []string {
	b, _ := core.MarshalJSON(v)
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// applyManifest previews a manifest change (diff plus a dry-run sync plan) or, with --apply,
// writes it and syncs the project.
func applyManifest(e *Env, root *core.Root, p *core.Project, manifest *core.Object, apply bool) (int, error) {
	return applyManifestWith(e, root, p, manifest, apply, nil)
}

// applyManifestWith is applyManifest that also uninstalls the kits the change drops, before the
// sync (core.SyncOpts.RemoveKits).
func applyManifestWith(e *Env, root *core.Root, p *core.Project, manifest *core.Object, apply bool, removeKits []string) (int, error) {
	rel := "projects/" + filepath.Base(p.File)
	if p.File == "" {
		rel = "projects/" + p.ID + ".json"
	}
	diff := unifiedDiff(jsonLines(p.Manifest), jsonLines(manifest), rel, rel)
	e.Set("project", p.ID)
	e.Set("diff", nonNilStrings(diff))
	e.Set("applied", false)
	if len(diff) == 0 {
		e.Printf("nothing to change\n")
		return 0, nil
	}
	for _, line := range diff {
		color := "2"
		if strings.HasPrefix(line, "+") {
			color = "32"
		} else if strings.HasPrefix(line, "-") {
			color = "31"
		}
		e.Printf("%s\n", e.Paint(color, line))
	}
	planned := &core.Project{ID: p.ID, Manifest: manifest, Repo: p.Repo, File: p.File}
	if !apply {
		logs := core.NewEngine(root).SyncProject(context.Background(), planned,
			core.SyncOpts{Dry: true, Native: true, RemoveKits: removeKits})
		printLogs(e, logs)
		plan := []any{}
		for _, l := range logs {
			plan = append(plan, l.ToObject())
		}
		e.Set("plan", plan)
		e.Printf("%s\n", e.Paint("2", "preview: nothing was written; pass --apply"))
		return 0, nil
	}
	lock, err := lockRoot(e, root)
	if err != nil {
		return 0, err
	}
	err = root.SaveManifest(planned)
	lock.Release()
	if err != nil {
		return 0, err
	}
	failed := syncProjects(e, root, []*core.Project{planned}, core.SyncOpts{Native: true, RemoveKits: removeKits})
	e.Set("applied", true)
	e.Set("healthy", !failed)
	if failed {
		return 1, nil
	}
	return 0, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func cmdSkills(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	p, err := root.ByID(a.Str("id"))
	if err != nil {
		return 0, err
	}
	eng := core.NewEngine(root)
	if a.Str("action") == "set" {
		add, err := names(a.Str("add"))
		if err != nil {
			return 0, err
		}
		remove, err := names(a.Str("remove"))
		if err != nil {
			return 0, err
		}
		if len(add) == 0 && len(remove) == 0 {
			return 0, usageErr("pass --add and/or --remove")
		}
		runtimes, err := names(a.Str("runtimes"))
		if err != nil {
			return 0, err
		}
		for _, r := range runtimes {
			if _, ok := core.SkillDirs[r]; !ok {
				return 0, usageErr("unknown runtime %q (choose from %s)", r, strings.Join(core.Runtimes, ", "))
			}
		}
		if len(runtimes) == 0 {
			runtimes = defaultRuntimes(p)
		}
		manifest, err := editSkills(eng, root, p, add, remove, runtimes)
		if err != nil {
			return 0, err
		}
		return applyManifest(e, root, p, manifest, a.Bool("apply"))
	}
	selected := core.NewObject()
	for _, it := range p.Manifest.Array("skills") {
		if s, ok := it.(*core.Object); ok {
			selected.Set(s.String("name"), s.Strings("runtimes"))
		}
	}
	e.Set("project", p.ID)
	e.Set("runtimes", defaultRuntimes(p))
	var agentkit *core.Object
	extras := []any{}
	for _, k := range p.Kits() {
		catalog := eng.KitCatalog(k.Name)
		mode, chosen := "list", k.Skills
		if k.All {
			mode, chosen = "all", []string{}
			for _, s := range catalog {
				if !contains(k.Exclude, s) {
					chosen = append(chosen, s)
				}
			}
		}
		o := core.Obj("kit", k.Name, "mode", mode, "exclude", k.Exclude, "selected", nonNilStrings(chosen),
			"catalog", nonNilStrings(catalog))
		if k.Primary {
			agentkit = o
		} else {
			extras = append(extras, o)
		}
		line := "AgentKit " + k.Name + ": " + strconv.Itoa(len(chosen)) + "/" + strconv.Itoa(len(catalog)) + " skills"
		if len(k.Exclude) > 0 {
			line += ", excluding " + strings.Join(k.Exclude, ", ")
		}
		e.Printf("%s\n", e.Paint("1", line))
	}
	if agentkit != nil && len(extras) > 0 {
		agentkit.Set("extra_kits", extras)
	}
	if agentkit != nil {
		e.Set("agentkit", agentkit)
	} else {
		e.Set("agentkit", nil)
	}
	e.Set("personal", core.Obj("catalog", personalCatalog(root), "selected", selected))
	for _, name := range selected.Keys() {
		e.Printf("  personal %s (%s)\n", name, strings.Join(selected.Strings(name), ", "))
	}
	return 0, nil
}

// cmdKit gives a project AgentKit (its primary kit), adds another kit to a project that has
// AgentKit, or removes an added kit. Added kits share the project's channel and targets and go
// in agentkit.extra_kits; the primary kit is not removed here.
func cmdKit(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	p, err := root.ByID(a.Str("id"))
	if err != nil {
		return 0, err
	}
	kit := a.Str("kit")
	if !core.ValidName(kit) {
		return 0, usageErr("invalid kit name %q", kit)
	}
	manifest := core.Clone(p.Manifest).(*core.Object)
	if a.Str("action") == "remove" {
		if p.AK() == nil {
			return 0, fail("%s does not use AgentKit", p.ID)
		}
		if !a.Given("kit") {
			return 0, usageErr("pass --kit NAME, the kit to remove")
		}
		if kit == p.Kit() {
			return 0, fail("%s is the primary kit of %s; acp removes only kits added with `acp kit add`", kit, p.ID)
		}
		ak := manifest.Object("agentkit")
		left, found := []any{}, false
		for _, it := range ak.Array("extra_kits") {
			if o, ok := it.(*core.Object); ok && o.String("kit") == kit {
				found = true
				continue
			}
			left = append(left, it)
		}
		if !found {
			return 0, fail("%s does not use AgentKit %s", p.ID, kit)
		}
		if len(left) > 0 {
			ak.Set("extra_kits", left)
		} else {
			ak.Delete("extra_kits")
		}
		return applyManifestWith(e, root, p, manifest, a.Bool("apply"), []string{kit})
	}
	if p.AK() == nil {
		targets, err := targetList(a.Str("targets"))
		if err != nil {
			return 0, err
		}
		manifest.Set("agentkit", core.Obj("kit", kit, "channel", a.Str("channel"),
			"targets", core.AnyList(targets), "skills", "all"))
		return applyManifest(e, root, p, manifest, a.Bool("apply"))
	}
	if !a.Given("kit") || p.UsesKit(kit) {
		return 0, fail("%s already uses AgentKit %s; pass --kit NAME to add another kit", p.ID, kit)
	}
	if a.Given("channel") && a.Str("channel") != p.Channel() || a.Given("targets") {
		return 0, usageErr("an added kit shares the project's channel (%s) and targets; drop --channel and --targets",
			p.Channel())
	}
	ak := manifest.Object("agentkit")
	ak.Set("extra_kits", append(ak.Array("extra_kits"), core.Obj("kit", kit, "skills", "all")))
	return applyManifest(e, root, p, manifest, a.Bool("apply"))
}
