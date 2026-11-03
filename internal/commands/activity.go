package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/thieung/acp/internal/core"
)

func cmdActivity(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	days, min := a.Int("days"), a.Int("min")
	all, idle, err := core.Activity(root, days)
	if err != nil {
		return 0, err
	}
	rows := []any{}
	e.Printf("%s\n", e.Paint("1", fmt.Sprintf("%6s %5s  LAST   FLEET ID              REPO", "CLAUDE", "CODEX")))
	for _, r := range all {
		if r.Claude+r.Codex < min {
			continue
		}
		var project any
		member := e.Paint("2", fmt.Sprintf("%-20s", "-"))
		if r.Project != "" {
			project = r.Project
			member = e.Paint("32", fmt.Sprintf("%-20s", r.Project))
		}
		rows = append(rows, core.Obj("repo", r.Repo, "claude", r.Claude, "codex", r.Codex,
			"last", r.Last.UTC().Format(time.RFC3339), "project", project))
		e.Printf("%6d %5d  %s  %s  %s\n", r.Claude, r.Codex, r.Last.Format("01-02"), member, r.Repo)
	}
	if len(idle) > 0 {
		e.Printf("%s\n", e.Paint("33", fmt.Sprintf("\nfleet projects with no sessions in %d days: %s", days, strings.Join(idle, ", "))))
	}
	e.Set("repos", rows)
	e.Set("idle", idle)
	e.Set("days", days)
	return 0, nil
}
