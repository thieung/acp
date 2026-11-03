package core

import (
	"os"
	"path/filepath"
)

// planSweep drops broken links acp made in a worktree: the ledger's first, then managed links
// found by walking.
func (s *session) planSweep() []op {
	tracked := s.e.Tracked(s.dir)
	var ops []op
	seen := map[string]bool{}
	add := func(rel string) {
		if seen[rel] || s.planned[rel] || tracked[rel] {
			return
		}
		seen[rel] = true
		ops = append(ops, op{kind: opUnlink, rel: rel, line: "- " + rel + " (broken)"})
	}
	if s.ledger != nil {
		for _, e := range s.ledger.Entries() {
			p := s.abs(e.Path)
			if !e.Dir && lstatIsLink(p) && !exists(p) && !s.linkedAncestor(e.Path) {
				add(e.Path)
			}
		}
	}
	for _, rel := range s.managedLinks([]string{s.main, s.e.Root.Dir}) {
		if !exists(s.abs(rel)) {
			add(rel)
		}
	}
	for _, rel := range s.stateLinks() {
		if !seen[rel] && !s.planned[rel] && !tracked[rel] {
			seen[rel] = true
			ops = append(ops, op{kind: opUnlink, rel: rel, line: "- " + rel + " (per-checkout state, not shared)"})
		}
	}
	return ops
}

// stateLinks finds links to the main checkout's per-checkout state (the names SkipName matches:
// agent memory, hook logs, runtime files) that an older acp mirrored into worktrees. acp no longer
// links these, so without this they stay shared and git status lists them. Only a link to the very
// same path in the main checkout counts; one pointing anywhere else is the user's.
func (s *session) stateLinks() []string {
	var out []string
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		for _, name := range sortedNames(s.abs(rel)) {
			key := rel + "/" + name
			p := s.abs(key)
			fi, err := os.Lstat(p)
			if err != nil {
				continue
			}
			switch {
			case fi.Mode()&os.ModeSymlink != 0:
				target, ok := LinkTarget(p)
				if ok && SkipName(name) && (target == filepath.Join(s.main, filepath.FromSlash(key)) ||
					target == filepath.Join(realPath(s.main), filepath.FromSlash(key))) {
					out = append(out, key)
				}
			case fi.IsDir() && depth < MirrorDepth && !SkipName(name):
				walk(key, depth+1)
			}
		}
	}
	for _, r := range MirrorRoots {
		if isRealDir(filepath.Join(s.dir, r)) {
			walk(r, 1)
		}
	}
	return out
}

// managedLinks walks the mirror roots (depth 3, never into linked folders or per-checkout state)
// and returns the managed symlinks, relative to the checkout.
func (s *session) managedLinks(anchors []string) []string {
	var out []string
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		for _, name := range sortedNames(s.abs(rel)) {
			if SkipName(name) {
				continue
			}
			key := rel + "/" + name
			p := s.abs(key)
			fi, err := os.Lstat(p)
			if err != nil {
				continue
			}
			switch {
			case fi.Mode()&os.ModeSymlink != 0:
				if IsManaged(p, anchors, s.dir) {
					out = append(out, key)
				}
			case fi.IsDir() && depth < MirrorDepth:
				walk(key, depth+1)
			}
		}
	}
	for _, r := range MirrorRoots {
		if isRealDir(filepath.Join(s.dir, r)) {
			walk(r, 1)
		}
	}
	return out
}
