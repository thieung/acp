package core

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ActivityRow is one repo with recent agent sessions.
type ActivityRow struct {
	Repo    string
	Claude  int
	Codex   int
	Last    time.Time
	Project string // fleet id, or ""
}

// sessionCwd reads the working directory from the first 40 lines of a session log.
func sessionCwd(path string, codex bool) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for i := 0; i <= 40 && sc.Scan(); i++ {
		var line struct {
			Type    string `json:"type"`
			Cwd     string `json:"cwd"`
			Payload struct {
				Cwd string `json:"cwd"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if codex && line.Type == "session_meta" {
			return line.Payload.Cwd
		}
		if !codex && line.Cwd != "" {
			return line.Cwd
		}
	}
	return ""
}

// tempRoots are folders whose sessions are scratch work unless they sit under HOME or belong to
// the fleet.
func tempRoots() []string {
	roots := []string{"/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp", os.TempDir()}
	if real, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		roots = append(roots, real)
	}
	return roots
}

// Activity counts Claude and Codex sessions per main checkout since a cutoff, newest-heavy first.
func Activity(root *Root, days int) ([]ActivityRow, []string, error) {
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	home := resolvePath(HomeDir())
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	claudeHome := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeHome == "" {
		claudeHome = filepath.Join(home, ".claude")
	}
	fleet, err := root.Fleet()
	if err != nil {
		return nil, nil, err
	}
	members := map[string]string{}
	for _, p := range fleet {
		if p.Repo != "" && isDir(p.Repo) {
			members[resolvePath(p.Repo)] = p.ID
		}
	}
	temps := tempRoots()
	rows := map[string]*ActivityRow{}
	checkouts := map[string]string{}
	sources := []struct {
		pattern string
		codex   bool
	}{
		{filepath.Join(claudeHome, "projects", "*", "*.jsonl"), false},
		{filepath.Join(codexHome, "sessions", "*", "*", "*", "*.jsonl"), true},
	}
	for _, src := range sources {
		files, _ := filepath.Glob(src.pattern)
		for _, path := range files {
			fi, err := os.Stat(path)
			if err != nil || fi.ModTime().Before(cutoff) {
				continue
			}
			cwd := sessionCwd(path, src.codex)
			if cwd == "" || !isDir(cwd) {
				continue
			}
			main, ok := checkouts[cwd]
			if !ok {
				// Only a folder inside a git checkout needs git; the rest are their own main.
				main = resolvePath(cwd)
				if enclosingCheckout(cwd) != "" {
					main, _ = CheckoutOf(cwd)
				}
				checkouts[cwd] = main
			}
			if main == home || strings.Contains(main, "/.cache/") {
				continue
			}
			if _, member := members[main]; !member && !Within(main, home) && WithinAny(main, temps) {
				continue
			}
			row := rows[main]
			if row == nil {
				row = &ActivityRow{Repo: main, Project: members[main]}
				rows[main] = row
			}
			if src.codex {
				row.Codex++
			} else {
				row.Claude++
			}
			if fi.ModTime().After(row.Last) {
				row.Last = fi.ModTime()
			}
		}
	}
	out := make([]ActivityRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].Claude+out[i].Codex, out[j].Claude+out[j].Codex; a != b {
			return a > b
		}
		return out[i].Repo < out[j].Repo
	})
	idle := []string{}
	for repo, id := range members {
		if rows[repo] == nil {
			idle = append(idle, id)
		}
	}
	sort.Strings(idle)
	return out, idle, nil
}

// WithinAny reports whether p is inside any of dirs.
func WithinAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if d != "" && Within(p, filepath.Clean(d)) {
			return true
		}
	}
	return false
}
