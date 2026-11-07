package core

import (
	"slices"
	"strings"
	"testing"
)

// Every list preset is named in PresetNames and holds sorted, unique ak- skill names, which is the
// form ak takes in --skills and the form the manifest keeps.
func TestPresetsAreWellFormed(t *testing.T) {
	names := PresetNames()
	if names[0] != AllSkills || len(names) != len(skillPresets)+1 {
		t.Fatalf("PresetNames %v does not match the %d presets", names, len(skillPresets))
	}
	for name, p := range skillPresets {
		if !slices.Contains(names, name) {
			t.Errorf("%s is missing from PresetNames", name)
		}
		if len(p.Skills) == 0 || !slices.IsSorted(p.Skills) || len(slices.Compact(slices.Clone(p.Skills))) != len(p.Skills) {
			t.Errorf("%s: skills must be sorted and unique: %v", name, p.Skills)
		}
		for _, s := range p.Skills {
			if !strings.HasPrefix(s, "ak-") || !ValidName(s) {
				t.Errorf("%s: %q is not an AgentKit skill name", name, s)
			}
		}
	}
	if p, _ := SkillPreset("minimal"); !slices.Contains(p.Skills, "ak-vault") || p.Kit != "" {
		t.Errorf("minimal: %+v", p)
	}
}

func TestParsePresets(t *testing.T) {
	for value, want := range map[string][]string{
		"minimal":               {"minimal"},
		" minimal , pr,minimal": {"minimal", "pr"},
		"all":                   {"all"},
	} {
		if got, err := ParsePresets(value); err != nil || !slices.Equal(got, want) {
			t.Errorf("%q: %v %v", value, got, err)
		}
	}
	for _, value := range []string{"", " , ", "tiny", "minimal,tiny", "all,minimal", "minimal-pr"} {
		if _, err := ParsePresets(value); err == nil {
			t.Errorf("%q accepted", value)
		}
	}
}

func TestPresetSkillsGroupsByKit(t *testing.T) {
	if PresetSkills([]string{AllSkills}, "engineer") != nil {
		t.Fatal("all is not a list")
	}
	got := PresetSkills([]string{"minimal", "pr", "pipeline", "marketing"}, "engineer")
	eng, mkt := got["engineer"], got["marketing"]
	if len(got) != 2 || !slices.IsSorted(eng) || !slices.Contains(eng, "ak-vault") || !slices.Contains(eng, "ak-vibe") {
		t.Fatalf("by kit: %v", got)
	}
	if n := len(eng); n != len(slices.Compact(slices.Clone(eng))) {
		t.Fatalf("pr and pipeline overlap, the union repeats: %v", eng)
	}
	if !slices.Contains(mkt, "ak-seo") || slices.Contains(mkt, "ak-plan") {
		t.Fatalf("marketing gets only its preset: %v", mkt)
	}
	// The core presets follow the primary kit.
	if m := PresetSkills([]string{"minimal", "continuity"}, "marketing"); len(m) != 1 || len(m["marketing"]) != 13 {
		t.Fatalf("core presets on marketing: %v", m)
	}
}

func TestResolvePresetsForOneKit(t *testing.T) {
	if all, _, err := ResolvePresets("all", "engineer"); !all || err != nil {
		t.Fatalf("all: %v %v", all, err)
	}
	if _, list, err := ResolvePresets("minimal,marketing", "marketing"); err != nil || !slices.Contains(list, "ak-seo") {
		t.Fatalf("marketing kit: %v %v", list, err)
	}
	if _, _, err := ResolvePresets("minimal,frontend", "marketing"); err == nil ||
		!strings.Contains(err.Error(), "frontend needs the engineer kit") {
		t.Fatalf("engineer preset on marketing: %v", err)
	}
}
