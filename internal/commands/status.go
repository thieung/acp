package commands

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/thieung/acp/internal/core"
)

var ansi = regexp.MustCompile("\033\\[[0-9;]*m")

func visibleLen(s string) int { return len([]rune(ansi.ReplaceAllString(s, ""))) }

func (e *Env) statusRow(info *core.Object) []string {
	id := info.String("id")
	if info.String("repo") == "" || !isDir(info.String("repo")) {
		return []string{id, "-", "-", "-"}
	}
	var versions []string
	for _, it := range info.Object("agentkit").Array("targets") {
		t := it.(*core.Object)
		name := t.String("target")
		if name == "claude-code" {
			name = "cc"
		}
		installed := t.String("installed")
		if installed == "" {
			installed = "none"
		}
		color := "33"
		if t.Bool("ok") {
			color = "32"
		}
		versions = append(versions, e.Paint(color, name+" "+installed))
	}
	ak := strings.Join(versions, " ")
	if ak == "" {
		ak = "-"
	}
	personal := info.Object("personal")
	trees := info.Object("worktrees")
	wt := "-"
	if n, _ := trees.Get("total"); n.(int) > 0 {
		wt = fmt.Sprintf("%v/%v", must(trees.Get("linked")), n)
	}
	return []string{id, ak, fmt.Sprintf("%v/%v", must(personal.Get("linked")), must(personal.Get("total"))), wt}
}

func must(v any, _ bool) any { return v }

func cmdStatus(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	fleet, err := root.Fleet()
	if err != nil {
		return 0, err
	}
	eng := core.NewEngine(root)
	infos := []any{}
	var rows [][]string
	notes := map[string][]string{}
	var order []string
	ids := map[string]bool{}
	for _, p := range fleet {
		info := eng.ProjectStatus(p)
		infos = append(infos, info)
		rows = append(rows, e.statusRow(info))
		ids[p.ID] = true
		if n := info.Strings("notes"); len(n) > 0 {
			notes[p.ID] = n
			order = append(order, p.ID)
		}
	}
	repos, err := root.OverlayRepos()
	if err != nil {
		return 0, err
	}
	orphans := []string{}
	for id := range repos {
		if !ids[id] {
			orphans = append(orphans, id)
		}
	}
	sort.Strings(orphans)
	for _, id := range orphans {
		notes[id] = []string{"overlay entry without a manifest; `acp remove " + id + " --yes`"}
		order = append(order, id)
	}
	pins, err := root.ReadPins()
	if err != nil {
		return 0, err
	}
	channels := pins.Object("channels")
	if channels == nil {
		channels = core.NewObject()
	}
	e.Set("projects", infos)
	e.Set("orphans", orphans)
	e.Set("pins", channels)
	e.Set("healthy", len(notes) == 0)

	header := []string{"PROJECT", "AGENTKIT", "PERSONAL", "WORKTREES"}
	widths := make([]int, len(header))
	for i := range header {
		widths[i] = len(header[i])
		for _, r := range rows {
			if l := visibleLen(r[i]); l > widths[i] {
				widths[i] = l
			}
		}
	}
	var head []string
	for i, h := range header {
		head = append(head, e.Paint("1", h+strings.Repeat(" ", widths[i]-len(h))))
	}
	e.Printf("%s\n", strings.Join(head, "  "))
	for _, r := range rows {
		mark := e.Paint("32", "✓")
		if _, bad := notes[r[0]]; bad {
			mark = e.Paint("31", "!")
		}
		var cells []string
		for i, c := range r {
			cells = append(cells, c+strings.Repeat(" ", widths[i]-visibleLen(c)))
		}
		e.Printf("%s  %s\n", strings.Join(cells, "  "), mark)
	}
	var pinned []string
	for _, ch := range channels.Keys() {
		kits := channels.Object(ch)
		for _, k := range kits.Keys() {
			pinned = append(pinned, fmt.Sprintf("%s/%s %s", ch, k, kits.String(k)))
		}
	}
	e.Printf("%s\n", e.Paint("2", "pinned: "+strings.Join(pinned, ", ")))
	if len(notes) > 0 {
		e.Printf("\n")
		for _, id := range order {
			e.Printf("%s\n", e.Paint("1", id))
			list := notes[id]
			shown := list
			if !a.Bool("verbose") && len(list) > 5 {
				shown = list[:5]
			}
			for _, note := range shown {
				sign, text := "~", note
				if len(note) >= 2 && strings.Contains("+-!~", note[:1]) && note[1] == ' ' {
					sign, text = note[:1], note[2:]
				}
				color := "33"
				if sign == "!" {
					color = "31"
				}
				e.Printf("  %s %s\n", e.Paint(color, sign), text)
			}
			if len(list) > 5 && !a.Bool("verbose") {
				e.Printf("  %s\n", e.Paint("2", fmt.Sprintf("... %d more (acp status -v)", len(list)-5)))
			}
		}
		e.Printf("%s\n", e.Paint("2", "\nfix: acp sync --all"))
	}
	if a.cmd.name == "doctor" && len(notes) > 0 {
		return 1, nil
	}
	return 0, nil
}
