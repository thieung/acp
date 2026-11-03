package core

import "context"

// Kits a project no longer uses, and the ak binary lookup the kit syncs share.

// akBin finds the ak binary once per sync and reports a missing one once.
type akBin struct {
	bin  string
	err  error
	done bool
}

func (a *akBin) get(log *Log) string {
	if !a.done {
		a.done = true
		if a.bin, a.err = AkBin(); a.err != nil {
			log.Issue("%v", a.err)
		}
	}
	return a.bin
}

// RemoveKits uninstalls kits that a project's manifest no longer lists (`ak kit uninstall`
// removes every target), then refreshes the primary kit on each target that has it: uninstalling
// a sibling kit can delete files the kits shared (bestagentkits/agentkit#2981), and a refresh at
// the pin writes them back. A kit still in the manifest is never uninstalled.
func (e *Engine) RemoveKits(ctx context.Context, p *Project, main string, kits []string, log *Log, dry bool) {
	ak := &akBin{}
	removed := false
	for _, kit := range kits {
		if p.UsesKit(kit) {
			log.Issue("AgentKit %s is still in the manifest; not uninstalled", kit)
			continue
		}
		installed := false
		for _, target := range TargetNames {
			installed = installed || InstallManifest(main, target, kit).Len() > 0
		}
		if !installed {
			log.Ok("AgentKit %s is not installed", kit)
			continue
		}
		log.Change("- AgentKit %s: ak kit uninstall", kit)
		if dry {
			removed = true
			continue
		}
		bin := ak.get(log)
		if bin == "" {
			return
		}
		args := []string{"kit", "uninstall", kit, "--project-dir", main, "--yes", "--no-interactive"}
		res := Exec(ctx, ExecOpts{Dir: main, Timeout: AKTimeout()}, bin, args...)
		if !res.OK() {
			reportAkFailure(log, args, "for "+kit, res)
			continue
		}
		removed = true
	}
	e.catalog = nil
	if !removed || p.AK() == nil {
		return
	}
	primary := p.Kit()
	pin := e.Root.PinnedVersion(p.Channel(), primary)
	if pin == "" {
		return // SyncNative reports the missing pin
	}
	for _, target := range p.AKTargets() {
		if _, ok := Targets[target]; !ok || InstalledVersion(InstallManifest(main, target, primary)) == "" {
			continue // SyncNative installs a missing primary kit
		}
		log.Change("~ AgentKit %s %s: refresh %s after the uninstall", primary, target, pin)
		if dry {
			continue
		}
		if bin := ak.get(log); bin != "" {
			e.runAk(ctx, bin, []string{"kit", "refresh", primary, "--channel", p.Channel(), "--version", pin,
				"--target", target, "--yes", "--no-interactive"}, main, target, pin, e.StoreLinks(main, Targets[target]), log)
		}
	}
	e.catalog = nil
}
