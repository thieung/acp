package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type ExecOpts struct {
	Dir     string
	Timeout time.Duration // 0: no timeout beyond ctx
	Env     []string      // added to the current environment
	Stdin   io.Reader
	// Tee also copies stdout and stderr here while the command runs.
	Tee io.Writer
}

type ExecResult struct {
	Stdout, Stderr string
	Code           int // -1 when the command did not exit on its own
	TimedOut       bool
	Err            error // start failure or timeout
}

func (r ExecResult) OK() bool { return r.Err == nil && r.Code == 0 }

// Output is stdout and stderr together, trimmed.
func (r ExecResult) Output() string {
	return string(bytes.TrimSpace([]byte(r.Stdout + r.Stderr)))
}

// Exec runs a command in its own process group. On timeout or cancel it sends SIGTERM and then
// SIGKILL to the whole group, and it returns only after the group is gone, so a grandchild that
// keeps stdout open cannot hang acp.
func Exec(ctx context.Context, o ExecOpts, name string, args ...string) ExecResult {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = o.Dir
	cmd.Stdin = o.Stdin
	if o.Env != nil {
		cmd.Env = append(os.Environ(), o.Env...)
	}
	if o.Tee != nil {
		cmd.Stdout = io.MultiWriter(&stdout, o.Tee)
		cmd.Stderr = io.MultiWriter(&stderr, o.Tee)
	} else {
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	res := ExecResult{Code: -1}
	if cmd.Process != nil && ctx.Err() != nil {
		killGroup(cmd.Process.Pid)
	}
	res.Stdout, res.Stderr = stdout.String(), stderr.String()
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		res.Code = cmd.ProcessState.ExitCode()
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) && o.Timeout > 0:
		res.TimedOut = true
		res.Err = fmt.Errorf("%s timed out after %s", name, o.Timeout)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
		res.Err = fmt.Errorf("%s timed out", name)
	case ctx.Err() != nil:
		res.Err = fmt.Errorf("%s: %w", name, ctx.Err())
	case err != nil && cmd.ProcessState == nil:
		res.Err = err
	}
	return res
}

// killGroup sends SIGKILL to the group until no member is left (at most ~2 s).
func killGroup(pgid int) {
	for i := 0; i < 40; i++ {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func envDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// GitTimeout bounds one git call (ACP_GIT_TIMEOUT, default 30s).
func GitTimeout() time.Duration { return envDuration("ACP_GIT_TIMEOUT", 30*time.Second) }

// AKTimeout bounds one ak call (ACP_AK_TIMEOUT, default 15m).
func AKTimeout() time.Duration { return envDuration("ACP_AK_TIMEOUT", 15*time.Minute) }

// AKLoginTimeout and AKWhoamiTimeout bound the two ak calls of `acp setup login`. The web sign-in
// waits longer than both together, so its own deadline never fires while ak is still working.
const (
	AKLoginTimeout  = 60 * time.Second
	AKWhoamiTimeout = 30 * time.Second
)

// Runner runs acp with args (the caller adds --json) and returns its stdout. stdin, when not nil,
// is the child's standard input: the only way a secret such as an API key reaches it.
type Runner func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)

// SelfRunner runs the acp binary at bin from dir as a separate process, so a slow or failing
// command cannot take down the server that asked for it. It adds no timeout of its own: the child
// bounds each git and ak call, and a fixed outer bound would kill a sync of many projects halfway
// through its writes. Only ctx stops it.
func SelfRunner(bin, dir string) Runner {
	return func(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
		o := ExecOpts{Dir: dir}
		if stdin != nil {
			o.Stdin = bytes.NewReader(stdin)
		}
		r := Exec(ctx, o, bin, args...)
		if r.Err != nil {
			return nil, fmt.Errorf("acp %s: %v", args[0], r.Err)
		}
		return []byte(r.Stdout), nil
	}
}
