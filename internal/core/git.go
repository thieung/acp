package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// MirrorRoots are the agent config roots mirrored from a main checkout into its worktrees.
var MirrorRoots = []string{".claude", ".agents", ".codex", ".agentkit"}

// GitRead runs a read-only git command in dir; GIT_OPTIONAL_LOCKS=0 keeps it from refreshing the
// index. It returns trimmed stdout, or an error with git's message.
func GitRead(dir string, args ...string) (string, error) {
	return git(dir, []string{"GIT_OPTIONAL_LOCKS=0"}, args...)
}

// GitWrite runs a git command that may change repository config.
func GitWrite(dir string, args ...string) (string, error) { return git(dir, nil, args...) }

// GitNoMonitor keeps git from running a repo's core.fsmonitor hook or starting its daemon, which
// would write into the git dir and outlive acp.
var GitNoMonitor = []string{"-c", "core.fsmonitor=false"}

func git(dir string, env []string, args ...string) (string, error) {
	res := Exec(context.Background(), ExecOpts{Timeout: GitTimeout(), Env: env}, "git",
		append(append(slices.Clone(GitNoMonitor), "-C", dir), args...)...)
	if res.Err != nil {
		return "", res.Err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(res.Stderr))
	}
	return strings.TrimSpace(res.Stdout), nil
}

// CheckoutOf returns (main checkout, current checkout) for a path; a non-git directory is its own
// main, and a folder nested in a repo belongs to the repo's main checkout.
func CheckoutOf(path string) (main, here string) {
	path = resolvePath(path)
	top, err := GitRead(path, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return path, path
	}
	top = resolvePath(top)
	common, err := GitRead(path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return top, top
	}
	common = resolvePath(common)
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), top
	}
	return top, top
}

// IsToplevel reports whether dir is the top of a git checkout (main or worktree).
func IsToplevel(dir string) bool {
	top, err := GitRead(dir, "rev-parse", "--show-toplevel")
	return err == nil && resolvePath(top) == resolvePath(dir)
}

// GitDir is the checkout's own git dir (.git, or .git/worktrees/<name> for a worktree).
func GitDir(dir string) (string, error) {
	d, err := GitRead(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return resolvePath(d), nil
}

// Worktrees lists the linked worktrees of a main checkout that exist on disk; a folder nested in
// a repo has none.
func Worktrees(main string) []string {
	main = resolvePath(main)
	if m, h := CheckoutOf(main); m != main || h != main {
		return nil
	}
	listing, err := GitRead(main, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	var out []string
	var cur string
	flush := func() {
		if cur != "" && isDir(cur) && resolvePath(cur) != main {
			out = append(out, resolvePath(cur))
		}
		cur = ""
	}
	for _, line := range strings.Split(listing, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = line[len("worktree "):]
		case strings.HasPrefix(line, "prunable"):
			cur = ""
		}
	}
	flush()
	return out
}

// Tracked returns the tracked paths under the mirror roots of a checkout, with every parent
// directory, relative to dir. A folder outside git has none. A session loads its checkouts with
// loadTracked first, so a git checkout whose index cannot be read never reaches here.
func (e *Engine) Tracked(dir string) map[string]bool {
	if t, ok := e.tracked[dir]; ok {
		return t
	}
	if err := e.loadTracked(dir); err != nil {
		return map[string]bool{}
	}
	return e.tracked[dir]
}

// loadTracked lists a checkout's tracked paths. An error is not cached: acp must not treat a
// tracked path as its own because git could not answer.
func (e *Engine) loadTracked(dir string) error {
	if _, ok := e.tracked[dir]; ok {
		return nil
	}
	listing, err := GitRead(dir, append([]string{"ls-files", "-z", "--"}, MirrorRoots...)...)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	for _, item := range strings.Split(listing, "\x00") {
		if item == "" {
			continue
		}
		parts := strings.Split(item, "/")
		for i := range parts {
			set[strings.Join(parts[:i+1], "/")] = true
		}
	}
	e.tracked[dir] = set
	return nil
}

func lstatIsLink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}
