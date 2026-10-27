package core

import (
	"strings"
	"testing"

	"github.com/thieung/acp/internal/testkit"
)

// fixture wraps testkit.Fixture with the core helpers the link engine tests use.
type fixture struct {
	*testkit.Fixture
	t                        *testing.T
	base, cp, repo, wt, home string
	root                     *Root
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	k := testkit.New(t)
	return &fixture{Fixture: k, t: t, base: k.Base, cp: k.CP, repo: k.Repo, wt: k.WT, home: k.Home,
		root: &Root{Dir: k.CP}}
}

func (f *fixture) write(path, content string)            { f.Write(path, content) }
func (f *fixture) manifest(json string)                  { f.Manifest("demo", json) }
func (f *fixture) symlink(target, link string)           { f.Symlink(target, link) }
func (f *fixture) git(dir string, args ...string) string { return f.Git(dir, args...) }

// status is `git status --porcelain -uall`.
func (f *fixture) status(dir string) string { return f.Status(dir) }

// native stands in for `ak kit init` in the main checkout.
func (f *fixture) native() { f.Native() }

func (f *fixture) project() *Project {
	f.t.Helper()
	p, err := f.root.ByID("demo")
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

// link runs one link pass like `acp link <dir>` and returns its log.
func (f *fixture) link(dir string, o LinkOpts) *Log {
	f.t.Helper()
	return f.linkWith(NewEngine(f.root), dir, o)
}

func (f *fixture) linkWith(e *Engine, dir string, o LinkOpts) *Log {
	f.t.Helper()
	log := NewLog(dir)
	e.SyncCheckout(f.project(), f.repo, dir, log, o)
	return log
}

func (f *fixture) unlink(dir string) []string {
	f.t.Helper()
	removed, err := NewEngine(f.root).UnlinkAll(f.repo, dir, false)
	if err != nil {
		f.t.Fatal(err)
	}
	return removed
}

func (f *fixture) realpath(p string) string { return f.RealPath(p) }

func (f *fixture) isLink(p string) bool { return f.IsLink(p) }

func clean(l *Log) bool { return len(l.Changes) == 0 && len(l.Issues) == 0 }

func lines(l *Log) string {
	return "changes: " + strings.Join(l.Changes, "; ") + " | issues: " + strings.Join(l.Issues, "; ")
}
