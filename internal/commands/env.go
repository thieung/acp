// Package commands implements every acp command and the --json envelope.
package commands

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/thieung/acp/internal/core"
)

// Version is set at build time with -ldflags "-X .../internal/commands.Version=v0.1.0".
var Version = "dev"

// BinaryMarker lets install.sh recognize an acp binary it may replace without running it.
const BinaryMarker = core.BinaryMarker

// Env is what a command sees: where to print, the parsed root, and the structured result that
// --json wraps as data.
type Env struct {
	Cwd   string
	Stdin io.Reader
	Out   io.Writer // human text; captured into data.text with --json
	Errw  io.Writer
	JSON  bool
	TTY   bool
	Data  *core.Object
	Logs  []*core.Log

	root     *core.Root
	rootErr  error
	rootDone bool
}

// Root resolves the control plane once per command.
func (e *Env) Root() (*core.Root, error) {
	if !e.rootDone {
		e.root, e.rootErr = core.ResolveRoot(e.Cwd)
		e.rootDone = true
	}
	return e.root, e.rootErr
}

func (e *Env) Project(id string) (*core.Project, error) {
	root, err := e.Root()
	if err != nil {
		return nil, err
	}
	return root.ByID(id)
}

func (e *Env) Printf(format string, a ...any) { fmt.Fprintf(e.Out, format, a...) }

// Set records a result field for the --json envelope.
func (e *Env) Set(key string, v any) { e.Data.Set(key, v) }

func (e *Env) Paint(code, text string) string {
	if !e.TTY {
		return text
	}
	return "\033[" + code + "m" + text + "\033[0m"
}

// PrintLog prints a log like Python's Log.print and records it for --json.
func (e *Env) PrintLog(l *core.Log, verbose bool) {
	e.Logs = append(e.Logs, l)
	state := e.Paint("32", "in sync")
	if len(l.Issues) > 0 {
		state = e.Paint("31", "needs attention")
	} else if len(l.Changes) > 0 {
		state = e.Paint("33", fmt.Sprintf("%d change(s)", len(l.Changes)))
	}
	e.Printf("%s  %s\n", e.Paint("1", l.Title), state)
	if verbose {
		for _, line := range l.OK {
			e.Printf("  %s %s\n", e.Paint("32", "ok"), line)
		}
	}
	for _, line := range l.Changes {
		head, rest := line, ""
		if len(line) >= 2 {
			head, rest = line[:1], line[2:]
		}
		e.Printf("  %s %s\n", e.Paint("33", head), rest)
	}
	for _, line := range l.Issues {
		e.Printf("  %s %s\n", e.Paint("31", "!"), line)
	}
}

// exitError carries an exit code other than 1, or an exit code without a message.
type exitError struct {
	code int
	err  error
}

func (x *exitError) Error() string {
	if x.err == nil {
		return ""
	}
	return x.err.Error()
}
func (x *exitError) Unwrap() error { return x.err }

func fail(format string, a ...any) error { return fmt.Errorf(format, a...) }

func usageErr(format string, a ...any) error {
	return &exitError{code: 2, err: fmt.Errorf(format, a...)}
}

func codeOf(err error) int {
	var x *exitError
	if errors.As(err, &x) {
		return x.code
	}
	return 1
}

type handler func(*Env, *Args) (int, error)

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// execute runs one parsed command. Without --json it prints text and, on error, "acp: <error>" to
// stderr. With --json it prints {schema_version, kind, data:{logs?, ...data, text}, exit_code,
// error?} on stdout.
func execute(a *Args, run handler, stdin io.Reader, stdout, stderr io.Writer) int {
	cwd, _ := os.Getwd()
	e := &Env{Cwd: cwd, Stdin: stdin, Errw: stderr, Data: core.NewObject()}
	if !a.json || a.cmd.server {
		e.Out, e.TTY = stdout, isTTY(stdout)
		code, err := run(e, a)
		if err != nil {
			if msg := err.Error(); msg != "" {
				fmt.Fprintf(stderr, "acp: %s\n", msg)
			}
			return codeOf(err)
		}
		return code
	}
	var text bytes.Buffer
	e.Out, e.JSON = &text, true
	code, err := run(e, a)
	if err != nil {
		code = codeOf(err)
	}
	writeEnvelope(stdout, a.Kind(), e, text.String(), code, err)
	return code
}

func writeEnvelope(w io.Writer, kind string, e *Env, text string, code int, err error) {
	data := core.NewObject()
	if len(e.Logs) > 0 {
		logs := make([]any, len(e.Logs))
		for i, l := range e.Logs {
			logs[i] = l.ToObject()
		}
		data.Set("logs", logs)
	}
	if e.Data != nil {
		for _, k := range e.Data.Keys() {
			v, _ := e.Data.Get(k)
			data.Set(k, v)
		}
	}
	data.Set("text", text)
	env := core.Obj("schema_version", 1, "kind", kind, "data", data, "exit_code", code)
	if err != nil && err.Error() != "" {
		env.Set("error", strings.TrimPrefix(err.Error(), "acp: "))
	}
	out, mErr := core.MarshalJSON(env)
	if mErr != nil {
		out = []byte(fmt.Sprintf("{\"schema_version\":1,\"kind\":%q,\"exit_code\":1,\"error\":%q}\n", kind, mErr.Error()))
	}
	w.Write(out)
}

// warnNoFix prints the one-line notice for the retired --no-fix flag.
func warnNoFix(w io.Writer, set bool) {
	if set {
		fmt.Fprintln(w, "acp: --no-fix does nothing now; acp no longer starts fix sessions")
	}
}
