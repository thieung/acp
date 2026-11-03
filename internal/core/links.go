package core

import (
	"os"
	"path/filepath"
)

// LinkTarget is a symlink's target, joined to the link's folder and cleaned (not resolved).
func LinkTarget(link string) (string, bool) {
	dest, err := os.Readlink(link)
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(filepath.Dir(link), dest)
	}
	return filepath.Clean(dest), true
}

// IsManaged reports whether link is a symlink acp may replace or remove: its target is inside one
// of the anchors. A link that points inside self (the checkout being changed) is the checkout's
// own when self sits inside that anchor, as a worktree under main/.claude/worktrees does.
func IsManaged(link string, anchors []string, self string) bool {
	target, ok := LinkTarget(link)
	if !ok {
		return false
	}
	for _, a := range anchors {
		if !Within(target, a) {
			continue
		}
		if self != "" && self != a && Within(self, a) && Within(target, self) {
			continue
		}
		return true
	}
	return false
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
