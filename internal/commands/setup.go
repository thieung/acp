package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/thieung/acp/internal/core"
)

// installers are AgentKit's official installer URLs, tried in order.
var installers = []string{"https://agentkit.best/install.sh", "https://releases.agentkit.best/install.sh"}

// akJSON runs `ak <args> --json` and returns its data object (empty on any failure).
func akJSON(bin string, args ...string) *core.Object {
	res := core.Exec(context.Background(), core.ExecOpts{Timeout: 2 * time.Minute}, bin, append(args, "--json")...)
	v, err := core.ParseJSON([]byte(res.Stdout))
	if err != nil {
		return core.NewObject()
	}
	o, _ := v.(*core.Object)
	if d := o.Object("data"); d != nil {
		return d
	}
	return core.NewObject()
}

// fleetChannel is the channel most AgentKit projects use (beta when none do).
func fleetChannel(root *core.Root) string {
	fleet, _ := root.Fleet()
	counts := map[string]int{}
	best := ""
	for _, p := range fleet {
		if p.AK() == nil {
			continue
		}
		ch := p.Channel()
		counts[ch]++
		if best == "" || counts[ch] > counts[best] {
			best = ch
		}
	}
	if best == "" {
		return "beta"
	}
	return best
}

func akInstallDir() string {
	if d := os.Getenv("AK_INSTALL_DIR"); d != "" {
		return d
	}
	return filepath.Join(core.HomeDir(), ".local", "bin")
}

func cmdSetup(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	switch a.Str("action") {
	case "install-ak":
		return installAk(e, root, a)
	case "login":
		bin, err := core.AkBin()
		if err != nil {
			return 0, fail("install ak first: `acp setup install-ak --apply`")
		}
		if e.JSON || !e.TTY || !isTTY(os.Stdin) {
			return 0, fail("sign-in is interactive; run `ak onboard` in a terminal")
		}
		cmd := exec.Command(bin, "onboard")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode(), nil
			}
			return 0, err
		}
		return 0, nil
	}
	pins, err := root.ReadPins()
	if err != nil {
		return 0, err
	}
	channels := pins.Object("channels")
	if channels == nil {
		channels = core.NewObject()
	}
	bin, binErr := core.AkBin()
	ak := core.Obj("path", nil, "version", nil)
	e.Set("ak", ak)
	e.Set("login", nil)
	e.Set("healthy", nil)
	e.Set("pins", channels)
	e.Set("channel", fleetChannel(root))
	if binErr != nil {
		e.Printf("ak       %s  ->  acp setup install-ak --apply\n", e.Paint("31", "not installed"))
		return 1, nil
	}
	ak.Set("path", bin)
	res := core.Exec(context.Background(), core.ExecOpts{Timeout: 30 * time.Second}, bin, "--version")
	version := ""
	if f := strings.Fields(res.Stdout); len(f) > 0 {
		version = f[len(f)-1]
		ak.Set("version", version)
	}
	whoami := akJSON(bin, "whoami")
	authenticated := whoami.Bool("authenticated")
	var method any
	if m, ok := whoami.Get("authMethod"); ok {
		method = m
	}
	e.Set("login", core.Obj("authenticated", authenticated, "method", method))
	healthy := akJSON(bin, "doctor").Bool("healthy")
	e.Set("healthy", healthy)
	e.Printf("ak       %s  (%s)\n", version, bin)
	if authenticated {
		e.Printf("login    %s\n", e.Paint("32", fmt.Sprintf("signed in (%v)", method)))
	} else {
		e.Printf("login    %s\n", e.Paint("31", "not signed in -> acp setup login"))
	}
	if healthy {
		e.Printf("doctor   %s\n", e.Paint("32", "healthy"))
	} else {
		e.Printf("doctor   %s\n", e.Paint("33", "needs attention: ak doctor"))
	}
	var pinned []string
	for _, ch := range channels.Keys() {
		kits := channels.Object(ch)
		for _, k := range kits.Keys() {
			pinned = append(pinned, fmt.Sprintf("%s/%s %s", ch, k, kits.String(k)))
		}
	}
	e.Printf("%s\n", e.Paint("2", "pinned   "+strings.Join(pinned, ", ")))
	if authenticated && healthy {
		return 0, nil
	}
	return 1, nil
}

// installerURLs is ACP_INSTALLER_URL (https or file only) or the official list.
func installerURLs() ([]string, error) {
	custom := os.Getenv("ACP_INSTALLER_URL")
	if custom == "" {
		return installers, nil
	}
	u, err := url.Parse(custom)
	if err != nil || (u.Scheme != "https" && u.Scheme != "file") {
		return nil, fail("ACP_INSTALLER_URL must be an https:// or file:// URL, not %q", custom)
	}
	return []string{custom}, nil
}

// httpsOnlyRedirect keeps an installer download on https when the server redirects it.
func httpsOnlyRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing a redirect to %s: installers download over https only", req.URL.Redacted())
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

func download(rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "file" {
		return os.ReadFile(u.Path)
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "acp/"+Version+" ("+BinaryMarker+")")
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: httpsOnlyRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", rawURL, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// installAk runs AgentKit's official installer, which verifies the release before installing.
func installAk(e *Env, root *core.Root, a *Args) (int, error) {
	channel := a.Str("channel")
	if channel == "" {
		channel = fleetChannel(root)
	}
	env := core.Obj("AK_CHANNEL", channel)
	if v := a.Str("version"); v != "" {
		env.Set("AK_VERSION", v)
	}
	urls, err := installerURLs()
	if err != nil {
		return 0, err
	}
	target := filepath.Join(akInstallDir(), "ak")
	expected := strings.ToLower(strings.TrimSpace(os.Getenv("ACP_INSTALLER_SHA256")))
	e.Set("installer", urls)
	e.Set("env", env)
	e.Set("binary", target)
	e.Set("applied", false)
	more := ""
	if len(urls) > 1 {
		more = " (fallback " + strings.Join(urls[1:], ", ") + ")"
	}
	var pairs []string
	for _, k := range env.Keys() {
		pairs = append(pairs, k+"="+env.String(k))
	}
	e.Printf("download %s%s\n", urls[0], more)
	if expected != "" {
		e.Printf("check    sha256 %s\n", expected)
	}
	e.Printf("run      %s sh install.sh  ->  %s\n", strings.Join(pairs, " "), target)
	e.Printf("verify   ak --version; ak doctor --exit-on-fail\n")
	if !a.Bool("apply") {
		e.Printf("%s\n", e.Paint("2", "preview: nothing was downloaded; pass --apply"))
		return 0, nil
	}
	var script []byte
	source := ""
	var errs []string
	for _, u := range urls {
		if script, err = download(u); err == nil {
			source = u
			break
		}
		errs = append(errs, err.Error())
	}
	if source == "" {
		return 0, fail("could not download the AgentKit installer: %s", strings.Join(errs, "; "))
	}
	sum := sha256.Sum256(script)
	digest := hex.EncodeToString(sum[:])
	e.Set("source", source)
	e.Set("sha256", digest)
	e.Printf("installer sha256 %s\n", digest)
	if expected != "" && digest != expected {
		return 0, fail("installer sha256 %s does not match ACP_INSTALLER_SHA256 %s; nothing was run", digest, expected)
	}
	folder := filepath.Join(root.Runtime(), "installers", time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return 0, err
	}
	path := filepath.Join(folder, "install.sh")
	if err := os.WriteFile(path, script, 0o600); err != nil {
		return 0, err
	}
	e.Set("applied", true)
	res := core.Exec(context.Background(), core.ExecOpts{Timeout: core.AKTimeout(), Env: append(pairs, "AK_INSTALL_DIR="+akInstallDir())},
		"sh", path)
	if out := res.Output(); out != "" {
		e.Printf("%s\n", core.Redact(out))
	}
	if !res.OK() {
		e.Printf("%s\n", e.Paint("31", fmt.Sprintf("the installer refused or failed (exit %d); nothing else was tried", res.Code)))
		return 1, nil
	}
	version := core.Exec(context.Background(), core.ExecOpts{Timeout: 30 * time.Second}, target, "--version")
	doctor := core.Exec(context.Background(), core.ExecOpts{Timeout: 2 * time.Minute}, target, "doctor", "--exit-on-fail")
	e.Set("version", strings.TrimSpace(version.Stdout))
	e.Set("doctor_ok", doctor.OK())
	state := "failed"
	if doctor.OK() {
		state = "ok"
	}
	e.Printf("installed %s; doctor %s\n", strings.TrimSpace(version.Stdout), state)
	if _, err := exec.LookPath("ak"); err != nil {
		e.Printf("%s\n", e.Paint("33", filepath.Dir(target)+" is not on PATH; open a new terminal or add it"))
	}
	if !doctor.OK() {
		return 1, nil
	}
	return 0, nil
}
