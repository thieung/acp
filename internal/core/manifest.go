package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// SkillDirs maps a runtime to its project skill folder.
var SkillDirs = map[string]string{"claude": ".claude/skills", "codex": ".agents/skills"}

// Runtimes is SkillDirs' keys in Python's order.
var Runtimes = []string{"claude", "codex"}

// Targets maps an AgentKit target to its runtime.
var Targets = map[string]string{"claude-code": "claude", "codex": "codex"}

// TargetNames is Targets' keys in Python's order.
var TargetNames = []string{"claude-code", "codex"}

// TargetOf is the AgentKit target that installs into a runtime.
func TargetOf(runtime string) (string, bool) {
	for _, t := range TargetNames {
		if Targets[t] == runtime {
			return t, true
		}
	}
	return "", false
}

// Project is one manifest plus the repo path from the machine overlay ("" when not on this machine).
type Project struct {
	ID       string
	Manifest *Object
	Repo     string
	File     string
}

// AK is the manifest's agentkit block (nil without AgentKit).
func (p *Project) AK() *Object {
	o := p.Manifest.Object("agentkit")
	if o == nil || o.Len() == 0 {
		return nil
	}
	return o
}

func (p *Project) Kit() string     { return p.AK().StringOr("kit", "engineer") }
func (p *Project) Channel() string { return p.AK().StringOr("channel", "beta") }

// AKTargets defaults to every target.
func (p *Project) AKTargets() []string {
	if ak := p.AK(); ak != nil && ak.Has("targets") {
		return ak.Strings("targets")
	}
	return append([]string(nil), TargetNames...)
}

// AKSkills is "all" or the selected skill list.
func (p *Project) AKSkills() (all bool, list []string) {
	ak := p.AK()
	v, ok := ak.Get("skills")
	if !ok {
		return true, nil
	}
	if s, ok := v.(string); ok && s == "all" {
		return true, nil
	}
	return false, StringList(asArray(v))
}

func (p *Project) AKExclude() []string { return p.AK().Strings("exclude") }

func asArray(v any) []any { a, _ := v.([]any); return a }

// Personal lists the personal skills the manifest selects for a runtime.
func (p *Project) Personal(runtime string) []string {
	out := []string{}
	for _, it := range p.Manifest.Array("skills") {
		s, ok := it.(*Object)
		if !ok {
			continue
		}
		for _, r := range s.Strings("runtimes") {
			if r == runtime {
				out = append(out, s.String("name"))
				break
			}
		}
	}
	return out
}

// Overlay returns id → repo path from projects.local.json.
func (r *Root) OverlayRepos() (map[string]string, error) {
	o, err := ReadObjectOr(r.Overlay())
	if err != nil {
		return nil, err
	}
	repos := map[string]string{}
	if m := o.Object("repos"); m != nil {
		for _, k := range m.Keys() {
			repos[k] = m.String(k)
		}
	}
	return repos, nil
}

func (r *Root) SaveOverlay(repos map[string]string) error {
	m := NewObject()
	keys := make([]string, 0, len(repos))
	for k := range repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		m.Set(k, repos[k])
	}
	return WriteJSON(r.Overlay(), Obj("schemaVersion", 1, "repos", m), 0o644)
}

// ReadPins returns the pin file; only schemaVersion 2 counts.
func (r *Root) ReadPins() (*Object, error) {
	o, err := ReadObjectOr(r.LockFile())
	if err != nil {
		return nil, err
	}
	if fmt.Sprint(must(o.Get("schemaVersion"))) != "2" {
		return NewObject(), nil
	}
	return o, nil
}

func must(v any, _ bool) any { return v }

// PinnedVersion is the kit version pinned for a channel, or "".
func (r *Root) PinnedVersion(channel, kit string) string {
	o, err := r.ReadPins()
	if err != nil {
		return ""
	}
	return o.Object("channels").Object(channel).String(kit)
}

// Fleet reads every manifest, sorted by file name.
func (r *Root) Fleet() ([]*Project, error) {
	repos, err := r.OverlayRepos()
	if err != nil {
		return nil, err
	}
	files, _ := filepath.Glob(filepath.Join(r.Projects(), "*.json"))
	sort.Strings(files)
	out := []*Project{}
	for _, f := range files {
		m, err := ReadObject(f)
		if err != nil {
			return nil, err
		}
		id := m.String("id")
		if !ValidName(id) {
			return nil, fmt.Errorf("%s: missing or invalid \"id\" %q", f, id)
		}
		out = append(out, &Project{ID: id, Manifest: m, Repo: repos[id], File: f})
	}
	return out, nil
}

// ByID finds one project.
func (r *Root) ByID(id string) (*Project, error) {
	fleet, err := r.Fleet()
	if err != nil {
		return nil, err
	}
	for _, p := range fleet {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no project '%s' (see `acp status`)", id)
}

// SaveManifest writes a project's manifest back to its file.
func (r *Root) SaveManifest(p *Project) error {
	path := p.File
	if path == "" {
		var err error
		if path, err = SafeJoin(r.Projects(), p.ID+".json"); err != nil {
			return err
		}
	}
	return WriteJSON(path, p.Manifest, 0o644)
}

// Exists reports whether a path exists without following a last symlink.
func Exists(p string) bool { _, err := os.Lstat(p); return err == nil }
