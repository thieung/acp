package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LedgerEntry is one thing acp made in a checkout: a symlink (with its target) or a folder it
// created to hold links.
type LedgerEntry struct {
	Path   string // relative to the checkout, slash-separated
	Target string
	Dir    bool
}

// Ledger lives at <git-dir>/acp-links.json and lists what acp made in that checkout, so unlink can
// remove all of it even after a crash or when a link points outside every anchor.
type Ledger struct {
	file     string
	checkout string
	entries  map[string]LedgerEntry
}

func LedgerPath(gitDir string) string { return filepath.Join(gitDir, "acp-links.json") }

// cleanRel validates a ledger path: relative, cleaned and inside the checkout.
func cleanRel(rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("ledger path %q is not relative", rel)
	}
	c := filepath.ToSlash(filepath.Clean(rel))
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("ledger path %q leaves the checkout", rel)
	}
	return c, nil
}

func LoadLedger(gitDir, checkout string) (*Ledger, error) {
	l := &Ledger{file: LedgerPath(gitDir), checkout: checkout, entries: map[string]LedgerEntry{}}
	o, err := ReadObject(l.file)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	for _, it := range o.Array("links") {
		m, ok := it.(*Object)
		if !ok {
			continue
		}
		rel, err := cleanRel(m.String("path"))
		if err != nil {
			continue
		}
		l.entries[rel] = LedgerEntry{Path: rel, Target: m.String("target"), Dir: m.Bool("dir")}
	}
	return l, nil
}

func (l *Ledger) Abs(rel string) string { return filepath.Join(l.checkout, filepath.FromSlash(rel)) }

func (l *Ledger) Get(rel string) (LedgerEntry, bool) { e, ok := l.entries[rel]; return e, ok }

func (l *Ledger) Add(e LedgerEntry) error {
	rel, err := cleanRel(e.Path)
	if err != nil {
		return err
	}
	e.Path = rel
	l.entries[rel] = e
	return nil
}

func (l *Ledger) Remove(rel string) { delete(l.entries, rel) }

func (l *Ledger) Len() int { return len(l.entries) }

// Entries are sorted by path; folders after the links inside them when reversed.
func (l *Ledger) Entries() []LedgerEntry {
	out := make([]LedgerEntry, 0, len(l.entries))
	for _, e := range l.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Save writes the ledger, or removes the file when it is empty.
func (l *Ledger) Save() error {
	if len(l.entries) == 0 {
		if err := os.Remove(l.file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	links := []any{}
	for _, e := range l.Entries() {
		o := Obj("path", e.Path)
		if e.Dir {
			o.Set("dir", true)
		} else {
			o.Set("target", e.Target)
		}
		links = append(links, o)
	}
	return WriteJSON(l.file, Obj("version", 1, "links", links), 0o644)
}

// Reconcile drops entries whose path is gone or is no longer what acp made. A link that now points
// somewhere else is reported and forgotten, never removed.
func (l *Ledger) Reconcile(log *Log) {
	for _, e := range l.Entries() {
		p := l.Abs(e.Path)
		fi, err := os.Lstat(p)
		switch {
		case err != nil:
			l.Remove(e.Path)
		case e.Dir:
			if !fi.IsDir() {
				l.Remove(e.Path)
			}
		case fi.Mode()&os.ModeSymlink == 0:
			l.Remove(e.Path)
		default:
			if dest, _ := os.Readlink(p); dest != e.Target {
				if log != nil {
					log.Issue("%s was changed after acp linked it; acp no longer manages it", e.Path)
				}
				l.Remove(e.Path)
			}
		}
	}
}
