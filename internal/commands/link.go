package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/thieung/acp/internal/core"
)

const (
	linkLockWait = 30 * time.Second
	hookLockWait = 5 * time.Second
)

func hookTimeout() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("ACP_HOOK_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return 8 * time.Second
}

func cmdLink(e *Env, a *Args) (int, error) {
	if a.Bool("hook") {
		return hookLink(e, a), nil
	}
	path := a.Str("path")
	if path == "" {
		path = e.Cwd
	}
	if !isDir(path) {
		return 0, fail("%s does not exist", path)
	}
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	p, main, here, err := root.ProjectFor(path)
	if err != nil {
		return 0, err
	}
	if p == nil {
		e.Printf("acp: not a fleet project; nothing to link\n")
		return 0, nil
	}
	check := a.Bool("check")
	if !check {
		lock, err := lockRepo(e, root, main, linkLockWait)
		if err != nil {
			return 0, fail("%s is busy syncing; run acp link %s later", main, path)
		}
		defer lock.Release()
	}
	log := core.NewLog(p.ID + "  " + here)
	core.NewEngine(root).SyncCheckout(p, main, here, log, core.LinkOpts{Dry: check, Adopt: a.Bool("adopt")})
	e.PrintLog(log, true)
	if check && (len(log.Changes) > 0 || len(log.Issues) > 0) {
		return 1, nil
	}
	return 0, nil
}

// hookLink is the session-start hook: it reads {"cwd": ...} from stdin, links that checkout and
// always exits 0 without printing to stdout. A busy repo or a slow run is skipped with one stderr
// line, so a session never waits long.
func hookLink(e *Env, a *Args) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(e.Errw, "acp link: %v\n", r)
		}
		code = 0
	}()
	say := func(format string, args ...any) { fmt.Fprintf(e.Errw, "acp link: "+format+"\n", args...) }
	path := a.Str("path")
	if path == "" {
		path = e.Cwd
	}
	if f, ok := e.Stdin.(*os.File); !ok || !isTTY(f) {
		data, _ := io.ReadAll(io.LimitReader(e.Stdin, 1<<20+1))
		if len(data) <= 1<<20 {
			var payload map[string]any
			if json.Unmarshal(data, &payload) == nil {
				if cwd, ok := payload["cwd"].(string); ok && cwd != "" {
					path = cwd
				}
			}
		}
	}
	if !isDir(path) {
		return 0
	}
	root, err := e.Root()
	if err != nil {
		say("%v", err)
		return 0
	}
	p, main, here, err := root.ProjectFor(path)
	if err != nil {
		say("%v", err)
		return 0
	}
	if p == nil {
		return 0
	}
	lock, err := core.AcquireWithin(root.RepoLock(main), hookLockWait)
	if err != nil {
		say("%s is busy; skipped linking %s (run `acp link %s` later)", main, here, here)
		return 0
	}
	defer lock.Release()
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout())
	defer cancel()
	eng := core.NewEngine(root)
	eng.Fault = func(string) error { return ctx.Err() }
	log := core.NewLog(p.ID + "  " + here)
	if err := eng.SyncCheckout(p, main, here, log, core.LinkOpts{}); err != nil && ctx.Err() != nil {
		say("stopped after %s; run `acp link %s` to finish", hookTimeout(), here)
	}
	return 0
}

func cmdUnlink(e *Env, a *Args) (int, error) {
	path := a.Str("path")
	if path == "" {
		path = e.Cwd
	}
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	main, here := core.CheckoutOf(path)
	dry := a.Bool("dry-run")
	if !dry {
		lock, err := lockRepo(e, root, main, linkLockWait)
		if err != nil {
			return 0, fail("%s is busy syncing; run acp unlink %s later", main, path)
		}
		defer lock.Release()
	}
	removed, err := core.NewEngine(root).UnlinkAll(main, here, dry)
	for _, rel := range removed {
		e.Printf("- %s\n", rel)
	}
	return 0, err
}
