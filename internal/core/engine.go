package core

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LinkOpts control one link pass.
type LinkOpts struct {
	Dry   bool
	Adopt bool // back up old copies in a worktree and link them to main
}

// Engine links one control plane's skills into checkouts. It caches git facts for one run.
type Engine struct {
	Root   *Root
	Now    func() time.Time
	Rename func(oldpath, newpath string) error
	// Fault, when set, runs before every filesystem step; an error stops the run there (tests use
	// it to simulate a crash).
	Fault   func(step string) error
	tracked map[string]map[string]bool
	catalog map[string][]string
}

func NewEngine(root *Root) *Engine {
	return &Engine{Root: root, Now: time.Now, Rename: os.Rename, tracked: map[string]map[string]bool{}}
}

type opKind int

const (
	opLink opKind = iota
	opUnlink
	opAdopt
	opMkdir
)

type op struct {
	kind    opKind
	rel     string
	target  string // link target
	replace bool   // remove an existing symlink first
	line    string // change line for the log ("" for folders)
}

// session is one checkout during one run.
type session struct {
	e         *Engine
	main, dir string
	gitDir    string
	ledger    *Ledger // nil for folders that are not a git toplevel
	dry       bool
	log       *Log
	planned   map[string]bool
	backfill  int
	stopped   error
}

func (e *Engine) newSession(main, dir string, log *Log, dry bool) *session {
	s := &session{e: e, main: resolvePath(main), dir: resolvePath(dir), dry: dry, log: log, planned: map[string]bool{}}
	if !IsToplevel(s.dir) {
		if v, _ := GitRead(s.dir, "rev-parse", "--is-inside-work-tree"); v == "true" {
			if err := e.loadTracked(s.dir); err != nil {
				log.Issue("cannot list the files git tracks in %s (%v); acp leaves this checkout alone", s.dir, err)
				s.stopped = err
			}
		}
		return s
	}
	gitDir, err := GitDir(s.dir)
	if err != nil {
		log.Issue("cannot read the git dir: %v", err)
		return s
	}
	ledger, err := LoadLedger(gitDir, s.dir)
	if err != nil {
		log.Issue("the link ledger is unreadable (%v); acp leaves this checkout alone", err)
		s.stopped = err
		return s
	}
	for _, dir := range []string{s.dir, s.main} {
		if IsToplevel(dir) {
			if err := e.loadTracked(dir); err != nil {
				log.Issue("cannot list the files git tracks in %s (%v); acp leaves this checkout alone", dir, err)
				s.stopped = err
				return s
			}
		}
	}
	s.gitDir, s.ledger = gitDir, ledger
	ledger.Reconcile(log)
	return s
}

func (s *session) abs(rel string) string { return filepath.Join(s.dir, filepath.FromSlash(rel)) }

// linkedAncestor reports whether a folder between the checkout and rel is a symlink; writing
// there would land in another checkout.
func (s *session) linkedAncestor(rel string) bool {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if lstatIsLink(s.abs(strings.Join(parts[:i], "/"))) {
			return true
		}
	}
	return false
}

// record adopts an existing correct link into the ledger (links made by the Python acp, or by an
// earlier run whose ledger was lost).
func (s *session) record(rel string) {
	if s.ledger == nil || s.linkedAncestor(rel) {
		return
	}
	if _, ok := s.ledger.Get(rel); ok {
		return
	}
	raw, err := os.Readlink(s.abs(rel))
	if err != nil {
		return
	}
	s.backfill++
	if !s.dry {
		s.ledger.Add(LedgerEntry{Path: rel, Target: raw})
	}
}

func (s *session) step(name string) error {
	if s.e.Fault != nil {
		return s.e.Fault(name)
	}
	return nil
}

// withParents puts a folder op before each link whose parent folders do not exist yet, so the
// ledger knows every folder acp creates.
func (s *session) withParents(ops []op) []op {
	out := []op{}
	made := map[string]bool{}
	for _, o := range ops {
		if o.kind == opLink || o.kind == opAdopt || o.kind == opMkdir {
			parts := strings.Split(o.rel, "/")
			for i := 1; i < len(parts); i++ {
				p := strings.Join(parts[:i], "/")
				if !made[p] && !Exists(s.abs(p)) {
					made[p] = true
					out = append(out, op{kind: opMkdir, rel: p})
				}
			}
		}
		if o.kind == opMkdir {
			if made[o.rel] {
				continue
			}
			made[o.rel] = true
		}
		out = append(out, o)
	}
	return out
}

// apply logs a stage's changes and, unless dry, writes the ledger first, then the exclude file,
// then the links, then reconciles, so a crash at any point leaves only recorded links behind.
func (s *session) apply(ops []op) {
	if s.stopped != nil {
		return
	}
	for _, o := range ops {
		if o.line != "" {
			s.log.Change("%s", o.line)
		}
		s.planned[o.rel] = true
	}
	if s.dry || len(ops) == 0 {
		return
	}
	ops = s.withParents(ops)
	if s.ledger != nil {
		pending := []string{}
		for _, o := range ops {
			switch o.kind {
			case opLink, opAdopt:
				s.ledger.Add(LedgerEntry{Path: o.rel, Target: o.target})
				pending = append(pending, o.rel)
			case opMkdir:
				s.ledger.Add(LedgerEntry{Path: o.rel, Dir: true})
			}
		}
		if err := s.ledger.Save(); err != nil {
			s.log.Issue("cannot write the link ledger: %v", err)
			s.stopped = err
			return
		}
		s.syncExclude(pending)
	}
	for _, o := range ops {
		if err := s.step("op"); err != nil {
			s.stopped = err
			return
		}
		s.run(o)
	}
	if s.ledger != nil {
		s.ledger.Reconcile(nil)
		if err := s.ledger.Save(); err != nil {
			s.log.Issue("cannot write the link ledger: %v", err)
		}
		s.syncExclude(nil)
	}
}

func (s *session) run(o op) {
	p := s.abs(o.rel)
	switch o.kind {
	case opMkdir:
		if err := os.Mkdir(p, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			s.log.Issue("cannot create %s: %v", o.rel, err)
		}
	case opUnlink:
		if lstatIsLink(p) {
			if err := os.Remove(p); err != nil {
				s.log.Issue("cannot remove %s: %v", o.rel, err)
				return
			}
		}
		if s.ledger != nil {
			s.ledger.Remove(o.rel)
		}
	case opLink:
		if o.replace && lstatIsLink(p) {
			os.Remove(p)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			err = os.Symlink(o.target, p)
			if err == nil {
				return
			}
			s.log.Issue("cannot link %s: %v", o.rel, err)
		} else {
			s.log.Issue("cannot create the folder for %s: %v", o.rel, err)
		}
	case opAdopt:
		s.adopt(o)
	}
}

// hidesLinks: worktrees get their own exclude file. The main checkout does not, like the Python
// acp: its links are the project's own agent setup.
func (s *session) hidesLinks() bool { return s.ledger != nil && s.dir != s.main }

// syncExclude points git at this checkout's exclude file listing the ledger's current links plus
// the ones about to be made.
func (s *session) syncExclude(pending []string) {
	if !s.hidesLinks() {
		if s.ledger != nil && !s.dry {
			if err := dropLegacyExclude(s.dir); err != nil {
				s.log.Issue("cannot remove the old worktree-level exclude setting: %v", err)
			}
		}
		return
	}
	set := map[string]bool{}
	for _, rel := range pending {
		set[rel] = true
	}
	for _, e := range s.ledger.Entries() {
		if !e.Dir && lstatIsLink(s.abs(e.Path)) {
			set[e.Path] = true
		}
	}
	rels := make([]string, 0, len(set))
	for rel := range set {
		rels = append(rels, rel)
	}
	if err := writeExclude(s.dir, s.gitDir, rels); err != nil {
		if errors.Is(err, ErrExcludeConflict) {
			s.log.Ok("git status shows acp's links here: %v", err)
			s.log.Fact("exclude_conflict", err.Error())
		} else {
			s.log.Issue("cannot hide acp's links from git status: %v", err)
		}
	}
}

// finish records backfilled links and refreshes the exclude file.
func (s *session) finish() {
	if s.ledger == nil || s.stopped != nil {
		return
	}
	if s.dry {
		if !s.hidesLinks() {
			return
		}
		configured := excludeConfigured(s.dir, s.gitDir)
		links := 0
		for _, e := range s.ledger.Entries() {
			if !e.Dir {
				links++
			}
		}
		if s.backfill > 0 || (links > 0 && !configured) {
			s.log.Change("~ hide acp's links from git status (%d existing link(s) to record)", s.backfill+links)
		}
		return
	}
	s.ledger.Reconcile(nil)
	if err := s.ledger.Save(); err != nil {
		s.log.Issue("cannot write the link ledger: %v", err)
		return
	}
	s.syncExclude(nil)
}

// SyncCheckout links personal skills into a checkout and, for a worktree, mirrors the main
// checkout's untracked agent files and drops broken links.
func (e *Engine) SyncCheckout(p *Project, main, dir string, log *Log, o LinkOpts) error {
	s := e.newSession(main, dir, log, o.Dry)
	s.apply(s.planPersonal(p))
	if s.dir != s.main {
		s.apply(s.planMirror(o.Adopt))
		s.apply(s.planSweep())
	}
	s.finish()
	return s.stopped
}

// UnlinkAll removes every link acp made in a checkout: the ledger's links that still point where
// acp pointed them, then managed links found by walking. Tracked paths are never touched. It
// returns the removed paths.
func (e *Engine) UnlinkAll(main, dir string, dry bool) ([]string, error) {
	log := NewLog("")
	s := e.newSession(main, dir, log, dry)
	if s.stopped != nil {
		return nil, s.stopped
	}
	tracked := e.Tracked(s.dir)
	removed := []string{}
	seen := map[string]bool{}
	remove := func(rel string) error {
		if seen[rel] || tracked[rel] {
			return nil
		}
		seen[rel] = true
		if !dry {
			if err := s.step("unlink"); err != nil {
				return err
			}
			if err := os.Remove(s.abs(rel)); err != nil {
				return err
			}
		}
		removed = append(removed, rel)
		return nil
	}
	var dirs []string
	if s.ledger != nil {
		for _, en := range s.ledger.Entries() {
			if en.Dir {
				dirs = append(dirs, en.Path)
				continue
			}
			if raw, err := os.Readlink(s.abs(en.Path)); err == nil && raw == en.Target && !s.linkedAncestor(en.Path) {
				if err := remove(en.Path); err != nil {
					return removed, err
				}
			}
		}
	}
	anchors := []string{s.main, e.Root.Dir}
	if s.dir == s.main {
		anchors = []string{e.Root.Skills(), e.Root.Runtime()}
	}
	for _, rel := range s.managedLinks(anchors) {
		if err := remove(rel); err != nil {
			return removed, err
		}
	}
	if dry || s.ledger == nil {
		return removed, nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, rel := range dirs {
		os.Remove(s.abs(rel)) // only succeeds when empty; anything inside stays
	}
	for _, en := range s.ledger.Entries() {
		s.ledger.Remove(en.Path)
	}
	if err := s.ledger.Save(); err != nil {
		return removed, err
	}
	if !s.hidesLinks() {
		return removed, nil
	}
	return removed, removeExclude(s.dir, s.gitDir)
}
