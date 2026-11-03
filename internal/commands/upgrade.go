package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/thieung/acp/internal/core"
)

// lockRoot takes the control-plane lock that guards manifests, the overlay, the pin file, global
// homes and settings files, waiting as long as it takes and saying so once.
func lockRoot(e *Env, root *core.Root) (*core.Lock, error) {
	if l, err := core.AcquireWithin(root.RootLock(), 2*time.Second); err == nil {
		return l, nil
	}
	fmt.Fprintf(e.Errw, "acp: waiting for another acp changing %s\n", root.Dir)
	return core.Acquire(context.Background(), root.RootLock())
}

// resolveLatest asks `ak changelog` for the newest release of a kit on a channel.
func resolveLatest(bin, channel, kit string) (string, error) {
	res := core.Exec(context.Background(), core.ExecOpts{Timeout: 2 * time.Minute}, bin,
		"changelog", "--channel", channel, "--kit", kit, "--json", "--no-interactive")
	if !res.OK() {
		return "", fail("`ak changelog` failed: %s", strings.TrimSpace(res.Stderr+errText(res.Err)))
	}
	var out struct {
		Data struct {
			Releases []struct {
				Version    string `json:"version"`
				Components []struct {
					Target string `json:"target"`
				} `json:"components"`
			} `json:"releases"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return "", fail("`ak changelog` printed something that is not JSON: %v", err)
	}
	for _, r := range out.Data.Releases {
		for _, c := range r.Components {
			if c.Target == "kit-"+kit && r.Version != "" {
				return r.Version, nil
			}
		}
	}
	return "", fail("no %s release in the %s changelog", kit, channel)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func cmdUpgrade(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	channel, dry := a.Str("channel"), a.Bool("dry-run")
	fleet, err := root.Fleet()
	if err != nil {
		return 0, err
	}
	kitSet := map[string]bool{}
	for _, p := range fleet {
		if p.AK() != nil && p.Channel() == channel {
			for _, k := range p.Kits() {
				kitSet[k.Name] = true
			}
		}
	}
	if k := a.Str("kit"); k != "" {
		if !core.ValidName(k) {
			return 0, usageErr("invalid kit name %q", k)
		}
		kitSet = map[string]bool{k: true}
	}
	kits := make([]string, 0, len(kitSet))
	for k := range kitSet {
		kits = append(kits, k)
	}
	sort.Strings(kits)
	if len(kits) == 0 {
		kits = []string{"engineer"}
	}
	version := a.Str("version")
	var bin string
	if version == "" || (!a.Bool("skip-binary") && !dry) {
		if bin, err = core.AkBin(); err != nil {
			return 0, err
		}
	}
	if !a.Bool("skip-binary") && !dry {
		e.Printf("updating the ak binary on %s ...\n", channel)
		res := core.Exec(context.Background(), core.ExecOpts{Timeout: core.AKTimeout()}, bin,
			"self-update", "--channel", channel, "--yes")
		if out := strings.TrimSpace(res.Stdout + res.Stderr); out != "" {
			e.Printf("%s\n", core.Redact(out))
		}
		if !res.OK() {
			e.Printf("%s\n", e.Paint("31", "the ak binary update failed; pins left alone (--skip-binary pins anyway)"))
			return 1, nil
		}
	}
	resolved := core.NewObject()
	for _, kit := range kits {
		v := version
		if v == "" {
			if v, err = resolveLatest(bin, channel, kit); err != nil {
				return 0, err
			}
		}
		resolved.Set(kit, v)
	}
	lock, err := lockRoot(e, root)
	if err != nil {
		return 0, err
	}
	pins, err := root.ReadPins()
	if err != nil {
		lock.Release()
		return 0, err
	}
	channels := pins.Object("channels")
	if channels == nil {
		channels = core.NewObject()
	}
	// Kits the fleet no longer uses on this channel drop out of the pin; --kit changes one kit.
	pinned := core.NewObject()
	if a.Str("kit") != "" {
		if old := channels.Object(channel); old != nil {
			pinned = core.Clone(old).(*core.Object)
		}
	}
	for _, kit := range kits {
		v := resolved.String(kit)
		old := root.PinnedVersion(channel, kit)
		if old == "" {
			old = "none"
		}
		pinned.Set(kit, v)
		e.Printf("pin %s/%s: %s -> %s\n", channel, kit, old, v)
	}
	channels.Set(channel, pinned)
	e.Set("resolved", resolved)
	e.Set("pins", channels)
	if dry {
		lock.Release()
		e.Printf("%s\n", e.Paint("2", "dry run: the binary, the lock and the fleet were left alone"))
		return 0, nil
	}
	err = core.WriteJSON(root.LockFile(), core.Obj("schemaVersion", 2, "channels", channels), 0o644)
	lock.Release()
	if err != nil {
		return 0, err
	}
	if a.Bool("no-sync") {
		return 0, nil
	}
	var todo []*core.Project
	for _, p := range fleet {
		if p.AK() != nil && p.Channel() == channel {
			todo = append(todo, p)
		}
	}
	if syncProjects(e, root, todo, core.SyncOpts{Native: true}) {
		return 1, nil
	}
	return 0, nil
}
