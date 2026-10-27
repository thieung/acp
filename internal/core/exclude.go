package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// ErrExcludeConflict means acp cannot give this checkout its own exclude file.
var ErrExcludeConflict = errors.New("exclude conflict")

// ExcludePath is the checkout's own exclude file. git reads it through core.excludesFile in
// excludeConfigPath, which the repo config includes only for this git dir
// ([includeIf "gitdir:<git dir>"]). Nothing is copied into a worktree added later, and once git
// deletes a worktree's admin folder the include points at a missing file, which git ignores.
func ExcludePath(gitDir string) string { return filepath.Join(gitDir, "acp-exclude") }

func excludeConfigPath(gitDir string) string { return filepath.Join(gitDir, "acp-config") }

func includeSection(gitDir string) string { return "includeIf.gitdir:" + gitDir }

func expandHome(p string) string {
	if p == "~" {
		return HomeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(HomeDir(), p[2:])
	}
	return p
}

// baseExcludes is the excludes file git would use without acp: the last core.excludesFile across
// the system, global and repo config, includes followed, else $XDG_CONFIG_HOME/git/ignore. Our
// setting replaces it for this checkout, so acp copies it.
func baseExcludes(checkout, ours string) string {
	all, _ := GitRead(checkout, "config", "--includes", "--get-all", "core.excludesFile")
	values := strings.Split(all, "\n")
	for i := len(values) - 1; i >= 0; i-- {
		p := expandHome(strings.TrimSpace(values[i]))
		if p == "" || p == ours || isAcpExclude(checkout, p) {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(checkout, p)
		}
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(HomeDir(), ".config")
	}
	return filepath.Join(base, "git", "ignore")
}

// escapePattern anchors a path and escapes gitignore's special characters.
func escapePattern(rel string) string {
	var b strings.Builder
	b.WriteByte('/')
	for _, r := range rel {
		if strings.ContainsRune(`*?[]\!#`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	s := b.String()
	if strings.HasSuffix(s, " ") {
		s = strings.TrimRight(s, " ") + strings.Repeat(`\ `, len(s)-len(strings.TrimRight(s, " ")))
	}
	return s
}

// legacyWorktreeExcludes is a core.excludesFile in this checkout's config.worktree, where an
// earlier acp build put it; git worktree add copies that file into new worktrees.
func legacyWorktreeExcludes(checkout string) string {
	if v, _ := GitRead(checkout, "config", "--local", "--get", "extensions.worktreeConfig"); v != "true" {
		return ""
	}
	v, _ := GitRead(checkout, "config", "--worktree", "--get", "core.excludesFile")
	return v
}

// excludeAllowed reports why this checkout cannot get its own exclude file, and moves a setting an
// earlier acp build left in config.worktree out of the way.
func excludeAllowed(checkout, gitDir string) error {
	if v, _ := GitRead(checkout, "config", "--local", "--get", "core.worktree"); v != "" {
		return fmt.Errorf("%w: the repo config sets core.worktree", ErrExcludeConflict)
	}
	if v, _ := GitRead(checkout, "config", "--local", "--get", "core.bare"); v == "true" {
		return fmt.Errorf("%w: the repo is bare", ErrExcludeConflict)
	}
	if strings.ContainsAny(gitDir, "*?[\\\"") || strings.ContainsFunc(gitDir, unicode.IsControl) {
		return fmt.Errorf("%w: the git folder path has characters git config cannot match", ErrExcludeConflict)
	}
	if cur := legacyWorktreeExcludes(checkout); cur != "" && !isAcpExclude(checkout, cur) {
		return fmt.Errorf("%w: core.excludesFile is already set for this checkout (%s)", ErrExcludeConflict, cur)
	}
	return dropLegacyExclude(checkout)
}

// dropLegacyExclude removes the worktree-level core.excludesFile an earlier acp set, so git
// worktree add no longer copies it into new worktrees. A user's own setting is left alone.
func dropLegacyExclude(checkout string) error {
	cur := legacyWorktreeExcludes(checkout)
	if cur == "" || !isAcpExclude(checkout, cur) {
		return nil
	}
	_, err := GitWrite(checkout, "config", "--worktree", "--unset-all", "core.excludesFile")
	return err
}

// excludeConfigured reports whether the repo config includes this git dir's acp-config.
func excludeConfigured(checkout, gitDir string) bool {
	cur, _ := GitRead(checkout, "config", "--local", "--get", includeSection(gitDir)+".path")
	return cur == excludeConfigPath(gitDir) && Exists(cur)
}

// writeExclude rewrites the exclude file for the given link paths, or removes it and its setting
// when there are none. The files are written before the include points at them.
func writeExclude(checkout, gitDir string, rels []string) error {
	pruneIncludes(checkout)
	ours := ExcludePath(gitDir)
	if len(rels) == 0 {
		return removeExclude(checkout, gitDir)
	}
	if err := excludeAllowed(checkout, gitDir); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Written by acp for this checkout only; it is rewritten on every acp run.\n")
	base := baseExcludes(checkout, ours)
	if data, err := os.ReadFile(base); err == nil {
		fmt.Fprintf(&b, "# Copied from %s, which this file replaces for this checkout:\n", base)
		b.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	b.WriteString("# Links acp made:\n")
	sorted := append([]string(nil), rels...)
	sort.Strings(sorted)
	for _, rel := range sorted {
		b.WriteString(escapePattern(rel) + "\n")
	}
	if err := WriteFileAtomic(ours, []byte(b.String()), 0o644); err != nil {
		return err
	}
	// excludeAllowed rejected quotes, backslashes and control characters, so plain quoting holds.
	cfg := "# Written by acp; the repo config includes it for this checkout only.\n[core]\n\texcludesFile = \"" +
		ours + "\"\n"
	if err := WriteFileAtomic(excludeConfigPath(gitDir), []byte(cfg), 0o644); err != nil {
		return err
	}
	if excludeConfigured(checkout, gitDir) {
		return nil
	}
	_, err := GitWrite(checkout, "config", "--local", "--replace-all", includeSection(gitDir)+".path", excludeConfigPath(gitDir))
	return err
}

func isAcpExclude(checkout, path string) bool {
	if filepath.Base(path) != "acp-exclude" {
		return false
	}
	common, err := GitRead(checkout, "rev-parse", "--path-format=absolute", "--git-common-dir")
	return err == nil && Within(resolvePath(path), resolvePath(common))
}

func removeExclude(checkout, gitDir string) error {
	if _, err := GitRead(checkout, "config", "--local", "--get", includeSection(gitDir)+".path"); err == nil {
		if _, err := GitWrite(checkout, "config", "--local", "--remove-section", includeSection(gitDir)); err != nil {
			return err
		}
	}
	if cur := legacyWorktreeExcludes(checkout); cur != "" && isAcpExclude(checkout, cur) {
		if _, err := GitWrite(checkout, "config", "--worktree", "--unset-all", "core.excludesFile"); err != nil {
			return err
		}
	}
	for _, p := range []string{excludeConfigPath(gitDir), ExcludePath(gitDir)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// pruneIncludes drops the includes of worktrees git has deleted. Each pointed at an acp-config in
// the removed admin folder, so it did nothing; this only keeps the repo config tidy.
func pruneIncludes(checkout string) {
	out, err := GitRead(checkout, "config", "--local", "-z", "--get-regexp", `^includeif\.gitdir:.*\.path$`)
	if err != nil {
		return
	}
	for _, item := range strings.Split(out, "\x00") {
		key, value, ok := strings.Cut(item, "\n")
		if !ok || filepath.Base(value) != "acp-config" {
			continue
		}
		if _, err := os.Stat(filepath.Dir(value)); err == nil {
			continue
		}
		// --get-regexp prints the section lowercased; --remove-section only finds it as includeIf.
		section := "includeIf." + strings.TrimSuffix(strings.TrimPrefix(key, "includeif."), ".path")
		_, _ = GitWrite(checkout, "config", "--local", "--remove-section", section)
	}
}
