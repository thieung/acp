package core

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// AllSkills is the preset (and the manifest value of agentkit.skills) that selects every skill of
// a kit.
const AllSkills = "all"

// Preset is a named list of AgentKit skills. Kit is the kit that has every skill on the list, or
// "" when the shared core skills alone make it up and every kit has them. Names carry the ak-
// prefix ak expects in `ak kit init <kit> --skills`.
type Preset struct {
	Kit    string
	Skills []string
}

// skillPresets are composable: `minimal,frontend` selects both lists. Each list was checked against
// the engineer (105 skills) and marketing (84 skills) catalogs of AgentKit 3.0.0.
var skillPresets = map[string]Preset{
	// The brainstorm → plan → cook → test → git loop, fix, debug and scout for bugs and search,
	// and vault for secrets.
	"minimal": {"", []string{"ak-brainstorm", "ak-cook", "ak-debug", "ak-fix", "ak-git", "ak-plan", "ak-scout",
		"ak-test", "ak-vault"}},
	// Work that spans sessions: journals, handoffs and status reports.
	"continuity": {"", []string{"ak-handoff", "ak-journal", "ak-sumup", "ak-watzup"}},
	// Code review and shipping through pull requests.
	"pr": {"engineer", []string{"ak-code-review", "ak-review-pr", "ak-ship"}},
	// UI work; frontend-design drives a browser through agent-browser to look at the result.
	"frontend": {"engineer", []string{"ak-agent-browser", "ak-frontend-design", "ak-ui-styling", "ak-web-testing"}},
	// APIs, databases, security review and deploys.
	"backend": {"engineer", []string{"ak-backend-development", "ak-databases", "ak-deploy", "ak-security"}},
	// ak-vibe's issue-to-PR pipeline and the skills it hands work to, beyond minimal.
	"pipeline": {"engineer", []string{"ak-code-review", "ak-docs", "ak-research", "ak-review-pr", "ak-ship",
		"ak-vibe", "ak-worktree"}},
	// Copy, content, search, social and campaign planning.
	"marketing": {"marketing", []string{"ak-campaign", "ak-content-marketing", "ak-copywriting",
		"ak-marketing-planning", "ak-marketing-research", "ak-seo", "ak-social"}},
}

// PresetNames lists every preset: "all" first, then the core presets, then the kit ones.
func PresetNames() []string {
	return []string{AllSkills, "minimal", "continuity", "pr", "frontend", "backend", "pipeline", "marketing"}
}

// SkillPreset returns a copy of a named list preset; ok is false for "all" and unknown names.
func SkillPreset(name string) (Preset, bool) {
	p, ok := skillPresets[name]
	return Preset{p.Kit, slices.Clone(p.Skills)}, ok
}

// ParsePresets splits a comma-separated preset value and checks each name. "all" stands alone.
func ParsePresets(value string) ([]string, error) {
	var names []string
	for _, n := range strings.Split(value, ",") {
		if n = strings.TrimSpace(n); n == "" || slices.Contains(names, n) {
			continue
		}
		if _, ok := skillPresets[n]; !ok && n != AllSkills {
			return nil, fmt.Errorf("unknown preset %q (choose from %s)", n, strings.Join(PresetNames(), ", "))
		}
		names = append(names, n)
	}
	switch {
	case len(names) == 0:
		return nil, fmt.Errorf("no preset given (choose from %s)", strings.Join(PresetNames(), ", "))
	case len(names) > 1 && slices.Contains(names, AllSkills):
		return nil, fmt.Errorf("preset %q selects every skill; it does not combine with others", AllSkills)
	}
	return names, nil
}

// PresetSkills groups the skills of parsed preset names by the kit they go to: a preset's own kit,
// or primary for the core presets. The lists are sorted without duplicates. It returns nil for
// "all".
func PresetSkills(names []string, primary string) map[string][]string {
	if slices.Equal(names, []string{AllSkills}) {
		return nil
	}
	byKit := map[string][]string{}
	for _, n := range names {
		p := skillPresets[n]
		kit := p.Kit
		if kit == "" {
			kit = primary
		}
		for _, s := range p.Skills {
			if !slices.Contains(byKit[kit], s) {
				byKit[kit] = append(byKit[kit], s)
			}
		}
	}
	for _, list := range byKit {
		sort.Strings(list)
	}
	return byKit
}

// ResolvePresets turns a preset value into the selection of one kit (a new project's only kit):
// all is true for "all"; a preset made for another kit is an error.
func ResolvePresets(value, kit string) (all bool, list []string, err error) {
	names, err := ParsePresets(value)
	if err != nil {
		return false, nil, err
	}
	byKit := PresetSkills(names, kit)
	if byKit == nil {
		return true, nil, nil
	}
	for k := range byKit {
		if k != kit {
			return false, nil, fmt.Errorf("preset %s needs the %s kit, not %s", PresetFor(names, k), k, kit)
		}
	}
	return false, byKit[kit], nil
}

// PresetFor names the first of names whose preset is made for kit.
func PresetFor(names []string, kit string) string {
	for _, n := range names {
		if skillPresets[n].Kit == kit {
			return n
		}
	}
	return ""
}
