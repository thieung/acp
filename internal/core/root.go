package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Root is the control plane: manifests, the machine overlay, the AgentKit pin and runtime state.
type Root struct{ Dir string }

func (r *Root) Projects() string     { return filepath.Join(r.Dir, "projects") }
func (r *Root) Overlay() string      { return filepath.Join(r.Dir, "projects.local.json") }
func (r *Root) LockFile() string     { return filepath.Join(r.Dir, "agentkit-releases.lock.json") }
func (r *Root) Skills() string       { return filepath.Join(r.Dir, "skills") }
func (r *Root) GlobalSkills() string { return filepath.Join(r.Dir, "global-skills.json") }
func (r *Root) Runtime() string      { return filepath.Join(r.Dir, "runtime") }
func (r *Root) Backups() string      { return filepath.Join(r.Dir, "runtime", "backups") }

// RootLock guards manifests, the overlay, the pin file, global homes and settings files.
func (r *Root) RootLock() string { return filepath.Join(r.Dir, "runtime", "acp.lock") }

// RepoLock guards one main checkout: its links, ledger, exclude file and ak runs. It lives in the
// control plane's runtime folder so acp never writes lock files into a repository.
func (r *Root) RepoLock(main string) string {
	sum := sha256.Sum256([]byte(main))
	return filepath.Join(r.Dir, "runtime", "locks", hex.EncodeToString(sum[:8])+".lock")
}

// RecordedRoot is the folder `acp init` recorded in ConfigFile, or "" when there is none.
func RecordedRoot() string {
	data, err := os.ReadFile(ConfigFile())
	if err != nil {
		return ""
	}
	if dir := strings.TrimSpace(string(data)); dir != "" && isDir(dir) {
		return dir
	}
	return ""
}

// ConfigFile is where `acp init` records the root: $XDG_CONFIG_HOME/acp/root.
func ConfigFile() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(HomeDir(), ".config")
	}
	return filepath.Join(base, "acp", "root")
}

func HomeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// DefaultRoot is ~/agent-control-plane.
func DefaultRoot() string { return filepath.Join(HomeDir(), "agent-control-plane") }

func resolvePath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func isControlPlane(dir string) bool {
	return isDir(filepath.Join(dir, "projects")) && isFile(filepath.Join(dir, "agentkit-releases.lock.json"))
}

// enclosingCheckout returns the top of the git checkout that contains dir, or "".
func enclosingCheckout(dir string) string {
	for d := resolvePath(dir); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// ResolveRoot finds the control plane: ACP_ROOT, then the git checkout around cwd when it is a
// control plane with a projects.local.json, then the path in ConfigFile, then
// ~/agent-control-plane.
func ResolveRoot(cwd string) (*Root, error) {
	if env := os.Getenv("ACP_ROOT"); env != "" {
		if !isDir(env) {
			return nil, fmt.Errorf("ACP_ROOT=%s is not a directory", env)
		}
		return &Root{Dir: resolvePath(env)}, nil
	}
	looked := []string{"ACP_ROOT (unset)"}
	if top := enclosingCheckout(cwd); top != "" {
		// Only a checkout set up on this machine counts: a control-plane worktree has no
		// projects.local.json, and its fleet would be empty.
		if isControlPlane(top) && isFile(filepath.Join(top, "projects.local.json")) {
			return &Root{Dir: top}, nil
		}
		looked = append(looked, top+" (git checkout without projects/, agentkit-releases.lock.json and projects.local.json)")
	} else {
		looked = append(looked, cwd+" (not inside a git checkout)")
	}
	conf := ConfigFile()
	if data, err := os.ReadFile(conf); err == nil {
		dir := strings.TrimSpace(string(data))
		if dir != "" && isDir(filepath.Join(dir, "projects")) {
			return &Root{Dir: resolvePath(dir)}, nil
		}
		looked = append(looked, fmt.Sprintf("%s (points to %q, which has no projects/)", conf, dir))
	} else {
		looked = append(looked, conf+" (missing)")
	}
	def := DefaultRoot()
	if isDir(filepath.Join(def, "projects")) {
		return &Root{Dir: resolvePath(def)}, nil
	}
	looked = append(looked, def+" (no projects/)")
	return nil, fmt.Errorf("no control plane found; looked at: %s. Run `acp init` or set ACP_ROOT",
		strings.Join(looked, "; "))
}
