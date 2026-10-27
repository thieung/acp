package core

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,80}$`)

// ValidName accepts project ids and skill names; the CLI, web and MCP all use it.
func ValidName(s string) bool { return namePattern.MatchString(s) && s != "." && s != ".." }

// SafeJoin joins name under dir and refuses a result outside dir.
func SafeJoin(dir, name string) (string, error) {
	p := filepath.Join(dir, name)
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(name) {
		return "", fmt.Errorf("%q leaves %s", name, dir)
	}
	return p, nil
}

// Within reports whether path is dir or inside it (both cleaned, absolute).
func Within(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}
