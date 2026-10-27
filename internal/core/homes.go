package core

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GlobalRuntimes is the order user homes are reported in.
var GlobalRuntimes = []string{"claude", "codex", "pi", "omp", "pi-ak", "pi-omp"}

func envOr(def string, names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return def
}

// GlobalHomes are the user-level agent homes, from the same variables the Python acp read.
func GlobalHomes() map[string]string {
	home := HomeDir()
	return map[string]string{
		"claude": envOr(filepath.Join(home, ".claude"), "CLAUDE_CONFIG_DIR"),
		"codex":  envOr(filepath.Join(home, ".codex"), "CODEX_HOME"),
		"pi":     envOr(filepath.Join(home, ".pi/agent"), "PI_CODING_AGENT_DIR"),
		"omp":    envOr(filepath.Join(home, ".omp/agent"), "AGENTKIT_OMP_HOME", "OMP_HOME", "PI_CODING_AGENT_DIR"),
		"pi-ak":  envOr(filepath.Join(home, ".pi/profiles/pi-ak"), "PI_AK_AGENT_DIR"),
		"pi-omp": envOr(filepath.Join(home, ".omp/profiles/pi-omp/agent"), "PI_OMP_AGENT_DIR"),
	}
}

// GlobalHome is one user home's skill folder and the runtimes that share it.
type GlobalHome struct {
	Label    string // runtimes sharing this home, joined with "+"
	Folder   string
	Declared map[string]bool
}

// MergedHomes groups runtimes that resolve to the same home, so a home shared by pi and omp gets
// the union of both declarations and one run does not undo another.
func MergedHomes(declared map[string]map[string]bool) []*GlobalHome {
	homes := GlobalHomes()
	byPath := map[string]*GlobalHome{}
	var out []*GlobalHome
	for _, rt := range GlobalRuntimes {
		key := resolvePath(homes[rt])
		h := byPath[key]
		if h == nil {
			h = &GlobalHome{Label: rt, Folder: filepath.Join(homes[rt], "skills"), Declared: map[string]bool{}}
			byPath[key] = h
			out = append(out, h)
		} else {
			h.Label += "+" + rt
		}
		for name := range declared[rt] {
			h.Declared[name] = true
		}
	}
	return out
}

// GlobalSync links the personal skills global-skills.json declares into each user home and drops
// acp's links it no longer declares. With check it only reports.
func GlobalSync(root *Root, check bool) (*Log, error) {
	spec, err := ReadObjectOr(root.GlobalSkills())
	if err != nil {
		return nil, err
	}
	declared := map[string]map[string]bool{}
	log := NewLog("global skills")
	for _, it := range spec.Array("skills") {
		s, ok := it.(*Object)
		if !ok {
			continue
		}
		name := s.String("name")
		if !ValidName(name) {
			log.Issue("global-skills.json: invalid skill name %q", name)
			continue
		}
		for _, rt := range s.Strings("runtimes") {
			if declared[rt] == nil {
				declared[rt] = map[string]bool{}
			}
			declared[rt][name] = true
		}
	}
	skills := root.Skills()
	for _, h := range MergedHomes(declared) {
		names := make([]string, 0, len(h.Declared))
		for n := range h.Declared {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			source, dest := filepath.Join(skills, name), filepath.Join(h.Folder, name)
			switch {
			case !isFile(filepath.Join(source, "SKILL.md")):
				log.Issue("%s: '%s' is missing from %s", h.Label, name, skills)
				log.Fact("personal_skill_missing", name)
			case lstatIsLink(dest) && realPath(dest) == source:
				log.Ok("%s: %s", h.Label, name)
			case Exists(dest) && !(lstatIsLink(dest) && IsManaged(dest, []string{skills}, "")):
				log.Issue("%s exists and is not acp's link; left alone", dest)
				log.Fact("real_dir_blocks_link", dest)
			default:
				if !check {
					if lstatIsLink(dest) {
						os.Remove(dest)
					}
					if err := os.MkdirAll(h.Folder, 0o755); err != nil {
						log.Issue("%v", err)
						continue
					}
					if err := os.Symlink(source, dest); err != nil {
						log.Issue("%v", err)
						continue
					}
				}
				log.Change("+ %s: %s", h.Label, name)
			}
		}
		for _, name := range sortedNames(h.Folder) {
			p := filepath.Join(h.Folder, name)
			if !IsManaged(p, []string{skills}, "") || (h.Declared[name] && exists(p)) {
				continue
			}
			if !check {
				if err := os.Remove(p); err != nil {
					log.Issue("%v", err)
					continue
				}
			}
			log.Change("- %s: %s", h.Label, name)
		}
	}
	return log, nil
}

// HookTarget is one settings file that gets the session-start hook.
type HookTarget struct{ Name, Path string }

// HookTargets are Claude's settings, Codex's hooks.json, every CCS instance and the CCS shared
// file that exist, and each extra path, once per real file.
func HookTargets(extra []string) []HookTarget {
	homes := GlobalHomes()
	list := []HookTarget{
		{"claude", filepath.Join(homes["claude"], "settings.json")},
		{"codex", filepath.Join(homes["codex"], "hooks.json")},
	}
	ccs := filepath.Join(HomeDir(), ".ccs")
	instances, _ := filepath.Glob(filepath.Join(ccs, "instances", "*", "settings.json"))
	for _, p := range instances {
		list = append(list, HookTarget{"ccs " + filepath.Base(filepath.Dir(p)), p})
	}
	list = append(list, HookTarget{"ccs shared", filepath.Join(ccs, "shared", "settings.json")})
	for _, p := range extra {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		list = append(list, HookTarget{"settings " + p, p})
	}
	seen := map[string]bool{}
	var out []HookTarget
	for _, t := range list {
		key := resolvePath(t.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

// ShellQuote quotes a word for sh only when it needs it, like Python's shlex.quote.
func ShellQuote(s string) string {
	safe := s != ""
	for _, r := range s {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789@%+=:,./-_", r) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
