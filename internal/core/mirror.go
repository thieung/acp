package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

// MirrorDepth is how deep mirror walks into the agent roots.
const MirrorDepth = 3

// mirrorSkip is per-checkout state that stays local to each worktree.
var mirrorSkip = map[string]bool{
	".DS_Store": true, "worktrees": true, "settings.local.json": true, "projects": true, "todos": true,
	"plans": true, "control-plane": true, "backups": true, "locks": true, "state": true,
	"session-state": true, "session-states": true, "logs": true, "agent-memory": true,
	"scheduled_tasks.lock": true, ".logs": true, ".agentkit-runtime.json": true,
}

// mirrorContainers are folders mirrored child by child, so per-checkout files inside them (hook
// logs, runtime state) stay local.
var mirrorContainers = map[string]bool{"skills": true, "plugins": true, "adapters": true, "hooks": true}

// SkipName reports whether a mirror-root entry is per-checkout state.
func SkipName(name string) bool {
	return mirrorSkip[name] || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".local.json")
}

var akOwned = regexp.MustCompile(`^(\.claude|\.agents)/skills/ak-[^/]+$` +
	`|^\.claude/(agents|hooks|rules|scripts|output-styles)/` +
	`|^\.codex/(agents|hooks)/` +
	`|^\.agentkit/adapters/`)

// AKOwned reports paths AgentKit generates; a differing copy of anything else is the worktree's own.
func AKOwned(key string) bool { return akOwned.MatchString(key) }

func isRealDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

func sortedNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	sort.Strings(names)
	return names
}

// planMirror links every untracked agent file of the main checkout that the worktree lacks.
func (s *session) planMirror(adopt bool) []op {
	root := s.e.Root
	keep := map[string]bool{}
	for k := range s.e.Tracked(s.main) {
		keep[k] = true
	}
	for k := range s.e.Tracked(s.dir) {
		keep[k] = true
	}
	anchors := []string{s.main, root.Dir}
	var ops []op
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		for _, name := range sortedNames(filepath.Join(s.main, rel)) {
			if SkipName(name) {
				continue
			}
			key := rel + "/" + name
			src := filepath.Join(s.main, filepath.FromSlash(key))
			dest := s.abs(key)
			if s.planned[key] {
				continue
			}
			replace := false
			switch {
			case lstatIsLink(dest):
				if exists(dest) {
					if !keep[key] && IsManaged(dest, anchors, s.dir) && realPath(dest) == realPath(src) {
						s.record(key)
					}
					continue
				}
				if keep[key] || !IsManaged(dest, anchors, s.dir) {
					continue
				}
				replace = true
			case Exists(dest):
				container := depth == 1 || mirrorContainers[name]
				if container && isRealDir(src) && isRealDir(dest) {
					if depth < MirrorDepth {
						walk(key, depth+1)
					}
					continue
				}
				if keep[key] || lstatIsLink(src) || !AKOwned(key) || sameTree(src, dest) {
					continue
				}
				if !adopt {
					s.log.Issue("%s is an old copy that shadows the main checkout; "+
						"`acp sync --adopt` backs it up and links it", key)
					s.log.Fact("old_copy_shadows_main", key)
					continue
				}
				ops = append(ops, op{kind: opAdopt, rel: key, target: src,
					line: "~ " + key + " (old copy moved to runtime/backups)"})
				continue
			}
			if keep[key] || (lstatIsLink(src) && !exists(src)) {
				continue
			}
			if IsManaged(src, []string{root.Runtime()}, "") {
				continue
			}
			if !replace && isRealDir(src) && mirrorContainers[name] && depth < MirrorDepth {
				ops = append(ops, op{kind: opMkdir, rel: key})
				s.planned[key] = true
				walk(key, depth+1)
				continue
			}
			target := src
			if lstatIsLink(src) {
				target = realPath(src)
			}
			ops = append(ops, op{kind: opLink, rel: key, target: target, replace: replace, line: "+ " + key})
		}
	}
	for _, r := range MirrorRoots {
		if isRealDir(filepath.Join(s.main, r)) && !s.linkedAncestor(r+"/x") && !lstatIsLink(s.abs(r)) {
			walk(r, 1)
		}
	}
	return ops
}

// adopt moves an old copy to runtime/backups and links main's version in its place.
func (s *session) adopt(o op) {
	dest := s.abs(o.rel)
	sum := sha256.Sum256([]byte(s.dir))
	backup := filepath.Join(s.e.Root.Backups(), "worktree-copies", s.e.Now().Format("20060102-150405"),
		filepath.Base(s.dir)+"-"+hex.EncodeToString(sum[:4]), filepath.FromSlash(o.rel))
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		s.log.Issue("cannot back up %s: %v", o.rel, err)
		return
	}
	if err := s.e.move(dest, backup); err != nil {
		s.log.Issue("cannot back up %s: %v", o.rel, err)
		return
	}
	if err := os.Symlink(o.target, dest); err != nil {
		s.log.Issue("cannot link %s (%v); the copy was put back", o.rel, err)
		if lstatIsLink(dest) {
			os.Remove(dest)
		}
		s.e.move(backup, dest)
	}
}

// move renames, and across filesystems copies (modes and links kept), checks and deletes.
func (e *Engine) move(from, to string) error {
	err := e.Rename(from, to)
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyTree(from, to); err != nil {
		os.RemoveAll(to)
		return err
	}
	if !sameTree(from, to) {
		os.RemoveAll(to)
		return fmt.Errorf("the copy of %s does not match", from)
	}
	return os.RemoveAll(from)
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		out := filepath.Join(to, rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			dest, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(dest, out)
		case info.IsDir():
			return os.MkdirAll(out, info.Mode().Perm())
		case info.Mode().IsRegular():
			return copyFile(p, out, info.Mode().Perm())
		}
		return nil // sockets and FIFOs are not copied
	})
}

func copyFile(from, to string, mode fs.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// sameTree compares two files or folders by content, following symlinks like Python's filecmp.
func sameTree(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return false
	}
	if fa.Mode().IsRegular() && fb.Mode().IsRegular() {
		return sameFile(a, b, fa.Size(), fb.Size())
	}
	if !fa.IsDir() || !fb.IsDir() {
		return false
	}
	na, nb := sortedNames(a), sortedNames(b)
	if strings.Join(na, "\x00") != strings.Join(nb, "\x00") {
		return false
	}
	for _, n := range na {
		if !sameTree(filepath.Join(a, n), filepath.Join(b, n)) {
			return false
		}
	}
	return true
}

func sameFile(a, b string, sa, sb int64) bool {
	if sa != sb {
		return false
	}
	da, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	db, err := os.ReadFile(b)
	return err == nil && bytes.Equal(da, db)
}
