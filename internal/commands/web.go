package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/thieung/acp/internal/core"
	"github.com/thieung/acp/internal/web"
)

// cmdWeb serves the dashboard on 127.0.0.1 until SIGINT or SIGTERM, or with --exit-with-parent until
// stdin closes: an app that runs acp web holds the other end, so acp stops when the app quits or
// crashes. The sign-in URL carries a one-time code; it is printed only to a terminal or with
// --print-url. On the way out it waits (up to ACP_AK_TIMEOUT) for a change already running.
func cmdWeb(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	bin, err := os.Executable()
	if err != nil {
		return 0, fail("cannot find the acp binary: %v", err)
	}
	port := a.Int("port")
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return 0, fail("port %d is already in use; stop the acp web that holds it or pick another --port", port)
		}
		return 0, fail("listen: %v", err)
	}
	srv := web.New(root, core.SelfRunner(bin, e.Cwd))
	addr := ln.Addr().String()
	srv.SetAddr(addr)
	signIn := fmt.Sprintf("http://%s/#code=%s", addr, srv.Code())
	e.Printf("acp web: http://%s (Ctrl-C to stop)\n", addr)
	if e.TTY || a.Bool("print-url") {
		e.Printf("sign in: %s\n", signIn)
	}
	if !a.Bool("no-open") {
		openBrowser(signIn)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if a.Bool("exit-with-parent") {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		go func() {
			io.Copy(io.Discard, e.Stdin)
			cancel()
		}()
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- hs.Serve(ln) }()
	select {
	case err := <-served:
		return 0, fail("acp web: %v", err)
	case <-ctx.Done():
	}
	stop() // a second Ctrl-C stops acp at once
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hs.Shutdown(shutdown)
	if srv.Running() {
		fmt.Fprintf(e.Errw, "acp web: waiting for a running change to finish\n")
	}
	wait, cancelWait := context.WithTimeout(context.Background(), core.AKTimeout())
	defer cancelWait()
	if err := srv.Wait(wait); err != nil {
		return 0, fail("acp web: stopped while a change was still running")
	}
	return 0, nil
}

func openBrowser(url string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	if path, err := exec.LookPath(name); err == nil {
		cmd := exec.Command(path, url)
		if cmd.Start() == nil {
			go cmd.Wait()
		}
	}
}
