package commands

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"

	"github.com/thieung/acp/internal/core"
)

//go:embed assets/agents-template.md
var agentsTemplate string

const initGitignore = "projects.local.json\nruntime/\n"

// cmdInit creates whatever a control plane is missing; it never overwrites an existing file. It
// records the root in the config file so later commands find it without ACP_ROOT. With ACP_ROOT
// set it records nothing: that variable already wins over the record, and it is usually a
// one-shell override (a test or a throwaway control plane) that must not move the root every
// other shell, the app and the hooks use.
func cmdInit(e *Env, a *Args) (int, error) {
	dir := a.Str("root")
	if dir == "" {
		// Without --root, init completes the control plane acp already uses (the installer runs
		// a bare `acp init` on every install), and only then falls back to the default.
		dir = os.Getenv("ACP_ROOT")
	}
	if dir == "" {
		dir = core.RecordedRoot()
	}
	if dir == "" {
		dir = core.DefaultRoot()
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return 0, err
	}
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		return 0, fail("%s exists and is not a directory", dir)
	}
	dry := a.Bool("dry-run")
	if !dry {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, err
		}
		dir = resolve(dir)
	}
	root := &core.Root{Dir: dir}
	created, kept := []string{}, []string{}
	folders := []string{"projects", "skills", "runtime"}
	files := []struct{ name, content string }{
		{"projects.local.json", "{\n  \"schemaVersion\": 1,\n  \"repos\": {}\n}\n"},
		{"agentkit-releases.lock.json", "{\n  \"schemaVersion\": 2,\n  \"channels\": {}\n}\n"},
		{"global-skills.json", "{\n  \"version\": 1,\n  \"skills\": []\n}\n"},
		{".gitignore", initGitignore},
		{"AGENTS.md", agentsTemplate},
	}
	for _, name := range folders {
		path := filepath.Join(dir, name)
		if fi, err := os.Stat(path); err == nil {
			if !fi.IsDir() {
				return 0, fail("%s exists and is not a directory", path)
			}
			kept = append(kept, name+"/")
			continue
		}
		if !dry {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return 0, err
			}
		}
		created = append(created, name+"/")
	}
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if core.Exists(path) {
			kept = append(kept, f.name)
			continue
		}
		if !dry {
			if err := core.WriteFileAtomic(path, []byte(f.content), 0o644); err != nil {
				return 0, err
			}
		}
		created = append(created, f.name)
	}
	for _, name := range created {
		e.Printf("  %s %s\n", e.Paint("32", "+"), name)
	}
	for _, name := range kept {
		e.Printf("  %s %s\n", e.Paint("2", "kept"), name)
	}
	var notes []string
	if gi, err := os.ReadFile(filepath.Join(dir, ".gitignore")); err == nil {
		lines := strings.Split(string(gi), "\n")
		for _, want := range []string{"projects.local.json", "runtime/"} {
			if !contains(lines, want) && !contains(lines, "/"+want) {
				notes = append(notes, ".gitignore does not ignore "+want+"; add it so machine paths and backups stay out of git")
			}
		}
	}
	for _, n := range notes {
		e.Printf("  %s %s\n", e.Paint("33", "!"), n)
	}
	conf := core.ConfigFile()
	e.Set("root", dir)
	e.Set("created", created)
	e.Set("kept", kept)
	e.Set("notes", nonNilStrings(notes))
	e.Set("config", conf)
	e.Set("applied", !dry)
	if dry {
		e.Printf("%s\n", e.Paint("2", "dry run: nothing was written"))
		return 0, nil
	}
	e.Set("recorded", os.Getenv("ACP_ROOT") == "")
	if os.Getenv("ACP_ROOT") != "" {
		e.Printf("control plane: %s (not recorded in %s: ACP_ROOT is set)\n", root.Dir, conf)
		e.Printf("%s\n", e.Paint("2", "to record it, run `acp init --root "+root.Dir+"` without ACP_ROOT"))
		return 0, nil
	}
	if old, err := os.ReadFile(conf); err != nil || strings.TrimSpace(string(old)) != dir {
		if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
			return 0, err
		}
		if err := core.WriteFileAtomic(conf, []byte(dir+"\n"), 0o644); err != nil {
			return 0, err
		}
	}
	e.Printf("control plane: %s (recorded in %s)\n", root.Dir, conf)
	e.Printf("next:\n  acp activity        repos you work in most\n  acp add <path>      add one to the fleet\n  acp scan            check that everything is linked\n")
	return 0, nil
}
