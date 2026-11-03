package commands

import (
	"os"

	"github.com/thieung/acp/internal/core"
)

func cmdGlobal(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	check := a.Bool("check")
	if !check {
		lock, err := lockRoot(e, root)
		if err != nil {
			return 0, err
		}
		defer lock.Release()
	}
	log, err := core.GlobalSync(root, check)
	if err != nil {
		return 0, err
	}
	e.PrintLog(log, a.Bool("verbose"))
	if len(log.Issues) > 0 || (check && len(log.Changes) > 0) {
		return 1, nil
	}
	return 0, nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
