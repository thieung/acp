package core

import (
	"os"
	"path/filepath"
	"sort"
)

// planPersonal links the manifest's personal skills into the checkout's skill folders and drops
// links into skills/ the manifest no longer selects.
func (s *session) planPersonal(p *Project) []op {
	root := s.e.Root
	tracked := s.e.Tracked(s.dir)
	var ops []op
	for _, runtime := range Runtimes {
		rel := SkillDirs[runtime]
		want := map[string]bool{}
		for _, name := range p.Personal(runtime) {
			want[name] = true
		}
		names := make([]string, 0, len(want))
		for n := range want {
			names = append(names, n)
		}
		sort.Strings(names)
		if s.linkedAncestor(rel + "/x") {
			continue // the whole skill folder is a link to the main checkout's
		}
		folder := filepath.Join(s.dir, rel)
		for _, name := range names {
			key := rel + "/" + name
			source := filepath.Join(root.Skills(), name)
			dest := filepath.Join(folder, name)
			if !ValidName(name) {
				s.log.Issue("personal skill name %q is not valid", name)
				continue
			}
			if !isFile(filepath.Join(source, "SKILL.md")) {
				s.log.Issue("personal skill '%s' is missing from %s", name, root.Skills())
				s.log.Fact("personal_skill_missing", name)
				continue
			}
			if tracked[key] {
				s.log.Issue("%s is tracked by git; left alone", key)
				continue
			}
			switch {
			case lstatIsLink(dest):
				if realPath(dest) == source {
					s.record(key)
					continue
				}
				if !IsManaged(dest, []string{root.Dir, s.main}, s.dir) {
					s.log.Issue("%s is a link acp did not make; left alone", key)
					s.log.Fact("foreign_link", key)
					continue
				}
				ops = append(ops, op{kind: opLink, rel: key, target: source, replace: true, line: "+ " + key})
			case Exists(dest):
				s.log.Issue("%s is a real directory; left alone", key)
				s.log.Fact("real_dir_blocks_link", key)
			default:
				ops = append(ops, op{kind: opLink, rel: key, target: source, line: "+ " + key})
			}
		}
		if fi, err := os.Lstat(folder); err != nil || !fi.IsDir() {
			continue
		}
		entries, _ := os.ReadDir(folder)
		for _, entry := range entries {
			key := rel + "/" + entry.Name()
			path := filepath.Join(folder, entry.Name())
			if tracked[key] || !IsManaged(path, []string{root.Skills()}, s.dir) {
				continue
			}
			if want[entry.Name()] && exists(path) {
				continue
			}
			ops = append(ops, op{kind: opUnlink, rel: key, line: "- " + key})
		}
	}
	return ops
}
