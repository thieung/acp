package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/thieung/acp/internal/core"
)

// selected is --all, the given ids, or the project the current directory belongs to.
func selected(e *Env, root *core.Root, all bool, ids []string) ([]*core.Project, error) {
	if all {
		return root.Fleet()
	}
	if len(ids) > 0 {
		var out []*core.Project
		for _, id := range ids {
			p, err := root.ByID(id)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return out, nil
	}
	p, _, _, err := root.ProjectFor(e.Cwd)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fail("this directory is not in the fleet; use `acp add`, an ID, or --all")
	}
	return []*core.Project{p}, nil
}

// lockRepo takes a main checkout's repo lock. wait 0 waits as long as it takes, saying so once.
func lockRepo(e *Env, root *core.Root, main string, wait time.Duration) (*core.Lock, error) {
	path := root.RepoLock(main)
	if wait > 0 {
		return core.AcquireWithin(path, wait)
	}
	if l, err := core.AcquireWithin(path, 2*time.Second); err == nil {
		return l, nil
	}
	fmt.Fprintf(e.Errw, "acp: waiting for another acp working on %s\n", main)
	return core.Acquire(context.Background(), path)
}

func repoOf(p *core.Project) string {
	if p.Repo == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p.Repo); err == nil {
		return real
	}
	return p.Repo
}

// printLogs prints project heads always and worktrees only when they changed.
func printLogs(e *Env, logs []*core.Log) {
	for _, l := range logs {
		if len(l.Changes) > 0 || len(l.Issues) > 0 || (l.Title != "" && l.Title[0] != ' ') {
			e.PrintLog(l, false)
		}
	}
}

func hasIssues(logs []*core.Log) bool {
	for _, l := range logs {
		if len(l.Issues) > 0 {
			return true
		}
	}
	return false
}

// syncProjects syncs each project under its repo lock; one failure does not stop the rest.
func syncProjects(e *Env, root *core.Root, projects []*core.Project, o core.SyncOpts) bool {
	eng := core.NewEngine(root)
	failed := false
	for _, p := range projects {
		var lock *core.Lock
		if repo := repoOf(p); repo != "" && !o.Dry && isDir(repo) {
			l, err := lockRepo(e, root, repo, 0)
			if err != nil {
				log := core.NewLog(p.ID)
				log.Issue("%v", err)
				printLogs(e, []*core.Log{log})
				failed = true
				continue
			}
			lock = l
		}
		logs := eng.SyncProject(context.Background(), p, o)
		lock.Release()
		printLogs(e, logs)
		failed = failed || hasIssues(logs)
	}
	return failed
}

func cmdSync(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	projects, err := selected(e, root, a.Bool("all"), a.List("ids"))
	if err != nil {
		return 0, err
	}
	o := core.SyncOpts{Dry: a.Bool("dry-run"), Native: !a.Bool("offline"), Adopt: a.Bool("adopt")}
	failed := syncProjects(e, root, projects, o)
	if o.Dry {
		e.Printf("%s\n", e.Paint("2", "dry run: nothing was written"))
	}
	if failed {
		return 1, nil
	}
	return 0, nil
}

func isDir(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }
