package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/thieung/acp/internal/core"
)

// installers are AgentKit's official installer URLs, tried in order.
var installers = []string{"https://agentkit.best/install.sh", "https://releases.agentkit.best/install.sh"}

// akJSON runs `ak <args> --json` and returns its data object (empty on any failure).
func akJSON(bin string, args ...string) *core.Object {
	return akJSONCtx(context.Background(), 2*time.Minute, bin, args...)
}

// akJSONCtx is akJSON stopped by ctx or after timeout.
func akJSONCtx(ctx context.Context, timeout time.Duration, bin string, args ...string) *core.Object {
	res := core.Exec(ctx, core.ExecOpts{Timeout: timeout}, bin, append(args, "--json")...)
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
	if a.Bool("api-key-stdin") && a.Str("action") != "login" {
		return 0, usageErr("--api-key-stdin works only with `acp setup login`")
	}
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	switch a.Str("action") {
	case "install-ak":
		return installAk(e, root, a)
	case "link-cli":
		return linkCLI(e, root, a.Bool("apply"))
	case "install-clt":
		return installCLT(e, a.Bool("apply"))
	case "login":
		bin, err := core.AkBin()
		if err != nil {
			return 0, fail("install ak first: `acp setup install-ak --apply`")
		}
		if a.Bool("api-key-stdin") {
			return loginWithKey(e, bin)
		}
		if e.JSON || !e.TTY || !isTTY(os.Stdin) {
			return 0, fail("sign-in is interactive; run `ak onboard` in a terminal, " +
				"or pipe an AgentKit API key to `acp setup login --api-key-stdin`")
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
		reportMachine(e)
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
	reportMachine(e)
	if authenticated && healthy {
		return 0, nil
	}
	return 1, nil
}

// reportMachine adds what a first run needs to know about this machine: whether a terminal's acp
// is this binary, and on macOS whether the Command Line Tools (and so git) are installed.
func reportMachine(e *Env) {
	dest, self := cliPath(), selfBinary()
	var target any
	state := e.Paint("33", "missing  ->  acp setup link-cli --apply")
	if _, err := os.Lstat(dest); err == nil {
		real := linkTarget(dest)
		target = real
		state = e.Paint("33", "another program ("+real+")  ->  acp setup link-cli --apply")
		if real == self {
			state = e.Paint("32", "this acp")
		}
	}
	e.Set("cli", core.Obj("path", dest, "target", target, "current", target == self))
	e.Printf("cli      %s  (%s)\n", state, dest)
	if installed, ok := cltInstalled(); ok {
		e.Set("clt", core.Obj("installed", installed))
		if installed {
			e.Printf("clt      %s\n", e.Paint("32", "Command Line Tools installed"))
		} else {
			e.Printf("clt      %s\n", e.Paint("33", "Command Line Tools missing  ->  acp setup install-clt --apply"))
		}
	}
}

// cliPath is where a terminal finds acp, and what link-cli points at this binary.
func cliPath() string { return filepath.Join(core.HomeDir(), ".local", "bin", "acp") }

// selfBinary is the resolved path of the running acp: inside acp.app, the bundled binary.
func selfBinary() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	return resolve(self)
}

// linkTarget is where path leads, or what a dangling link names.
func linkTarget(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	if old, err := os.Readlink(path); err == nil {
		return old
	}
	return path
}

func hasMarker(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && bytes.Contains(data, []byte(BinaryMarker))
}

// replaceableLink reports whether a link may be pointed elsewhere: it leads to a binary carrying
// BinaryMarker, or names the Python script or the binary of an acp.app that has since moved.
func replaceableLink(link, old string) bool {
	if strings.HasSuffix(old, "/scripts/acp") || strings.HasSuffix(old, "/acp.app/Contents/Helpers/acp") {
		return true
	}
	real, err := filepath.EvalSymlinks(link)
	if err != nil {
		return false
	}
	fi, err := os.Stat(real)
	return err == nil && fi.Mode().IsRegular() && hasMarker(real)
}

// linkCLI points ~/.local/bin/acp at this binary (never ACP_BIN), so a terminal runs the same acp
// as the app. It replaces only another acp: a link to one, or a curl-installed binary, which it
// copies to runtime/backups first. Anything else there is the user's and stays.
func linkCLI(e *Env, root *core.Root, apply bool) (int, error) {
	self := selfBinary()
	if self == "" {
		return 0, fail("cannot tell where this acp binary is")
	}
	if err := lastingBinary(self); err != nil {
		return 0, err
	}
	dest := cliPath()
	dir := filepath.Dir(dest)
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return 0, fail("%s is a symlink; acp does not write through it, so link %s yourself", dir, dest)
	}
	if apply {
		lock, err := lockRoot(e, root)
		if err != nil {
			return 0, err
		}
		defer lock.Release()
	}
	e.Set("path", dest)
	e.Set("target", self)
	e.Set("applied", false)
	e.Set("backup", nil)
	log := core.NewLog("acp in the terminal")
	fi, err := os.Lstat(dest)
	backup := false
	switch {
	case errors.Is(err, fs.ErrNotExist):
		log.Change("+ %s -> %s", dest, self)
	case err != nil:
		return 0, err
	case linkTarget(dest) == self:
		log.Ok("%s is already this acp", dest)
	case fi.Mode()&fs.ModeSymlink != 0:
		old, _ := os.Readlink(dest)
		if !replaceableLink(dest, old) {
			return 0, fail("%s is a link to %s, not acp; move it away and run again", dest, old)
		}
		log.Change("~ %s -> %s (was a link to %s)", dest, self, old)
	case fi.Mode().IsRegular() && hasMarker(dest):
		backup = true
		log.Change("~ %s -> %s (the acp there now goes to a backup in %s)", dest, self, root.Backups())
	default:
		return 0, fail("%s exists and is not acp; move it away and run again", dest)
	}
	e.PrintLog(log, true)
	if len(log.Changes) == 0 {
		return 0, nil
	}
	if !apply {
		e.Printf("%s\n", e.Paint("2", "preview: nothing was changed; pass --apply"))
		return 0, nil
	}
	if backup {
		path, err := backupBinary(root, dest, time.Now())
		if err != nil {
			return 0, fail("could not back up %s, so it was left alone: %v", dest, err)
		}
		e.Set("backup", path)
		e.Printf("backup   %s\n", path)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	// A new link renamed over the old entry, so a terminal never finds acp missing.
	tmp := filepath.Join(dir, fmt.Sprintf(".acp.%d", os.Getpid()))
	os.Remove(tmp)
	if err := os.Symlink(self, tmp); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	e.Set("applied", true)
	return 0, nil
}

// backupBinary copies an acp binary, mode included, to runtime/backups/<time>/acp.
func backupBinary(root *core.Root, path string, now time.Time) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root.Backups(), now.Format("20060102-150405.000000"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "acp")
	return out, os.WriteFile(out, data, fi.Mode().Perm())
}

// xcodeSelect is Apple's tool that reports and installs the Command Line Tools (ACP_XCODE_SELECT
// for tests).
func xcodeSelect() string {
	if p := os.Getenv("ACP_XCODE_SELECT"); p != "" {
		return p
	}
	return "/usr/bin/xcode-select"
}

// cltInstalled reports whether the Command Line Tools are installed; ok is false off macOS, where
// git comes from the system's packages. It asks xcode-select, never git: without the tools, macOS
// answers every git call with an installer dialog.
func cltInstalled() (installed, ok bool) {
	if runtime.GOOS != "darwin" {
		return false, false
	}
	return core.Exec(context.Background(), core.ExecOpts{Timeout: 10 * time.Second}, xcodeSelect(), "-p").OK(), true
}

// installCLT opens Apple's installer for the Command Line Tools, which acp needs for git. The
// installer is Apple's dialog; acp only asks for it.
func installCLT(e *Env, apply bool) (int, error) {
	installed, ok := cltInstalled()
	if !ok {
		return 0, fail("the Command Line Tools are for macOS; install git with your system's packages")
	}
	e.Set("clt", core.Obj("installed", installed))
	e.Set("applied", false)
	log := core.NewLog("Command Line Tools")
	if installed {
		log.Ok("the Command Line Tools are installed")
		e.PrintLog(log, true)
		return 0, nil
	}
	log.Change("+ the Command Line Tools (git and the compilers), from Apple's installer dialog")
	e.PrintLog(log, true)
	if !apply {
		e.Printf("%s\n", e.Paint("2", "plan: nothing was opened; pass --apply"))
		return 0, nil
	}
	res := core.Exec(context.Background(), core.ExecOpts{Timeout: 30 * time.Second}, xcodeSelect(), "--install")
	if !res.OK() {
		return 0, fail("xcode-select --install failed (exit %d): %s", res.Code, strings.TrimSpace(res.Stderr))
	}
	e.Set("applied", true)
	e.Printf("Apple's installer is open; finish it there, then check with `acp setup`\n")
	return 0, nil
}

// maxKey bounds an API key read from stdin; real keys are far shorter.
const maxKey = 4096

// loginWithKey signs in with an API key read from stdin and hands it to `ak login` on stdin only:
// never in argv or the environment, and ak's own output is not printed because it may echo the key.
// ak runs in its own process group, so a SIGTERM or SIGINT sent to acp (the web sign-in giving up)
// stops ak before acp exits instead of leaving it to finish the sign-in on its own.
func loginWithKey(e *Env, bin string) (int, error) {
	if isTTY(os.Stdin) {
		return 0, usageErr("pipe the key to --api-key-stdin; typed in a terminal it would echo")
	}
	raw, err := io.ReadAll(io.LimitReader(e.Stdin, maxKey+1))
	if err != nil {
		return 0, err
	}
	key := bytes.TrimSpace(raw)
	switch {
	case len(raw) > maxKey:
		return 0, usageErr("the API key on stdin is longer than %d bytes", maxKey)
	case len(key) == 0:
		return 0, usageErr("no API key on stdin")
	case bytes.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r > '~' }):
		return 0, usageErr("the API key on stdin has spaces or characters a key cannot have")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res := core.Exec(ctx, core.ExecOpts{Timeout: core.AKLoginTimeout, Stdin: bytes.NewReader(key)},
		bin, "login", "--api-key-stdin", "--json", "--no-interactive")
	switch {
	case ctx.Err() != nil:
		return 0, fail("sign-in stopped before ak finished")
	case res.TimedOut:
		return 0, fail("sign-in failed: ak did not answer in %s", core.AKLoginTimeout)
	case !res.OK():
		return 0, fail("sign-in failed (ak exit %d); check the key on agentkit.best", res.Code)
	}
	whoami := akJSONCtx(ctx, core.AKWhoamiTimeout, bin, "whoami")
	var method any
	if m, ok := whoami.Get("authMethod"); ok {
		method = m
	}
	e.Set("login", core.Obj("authenticated", whoami.Bool("authenticated"), "method", method))
	if !whoami.Bool("authenticated") {
		return 0, fail("sign-in did not take; ak whoami says signed out")
	}
	e.Printf("login    %s\n", e.Paint("32", fmt.Sprintf("signed in (%v)", method)))
	return 0, nil
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
