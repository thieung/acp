package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ErrLockBusy means another acp process holds the lock.
var ErrLockBusy = errors.New("another acp process is working here")

// Lock is an flock-based lock on one file. Locks are re-entrant inside a process: a nested
// Acquire of a held path returns at once and the file is unlocked after the last Release.
type Lock struct{ path string }

type heldLock struct {
	f *os.File
	n int
}

var (
	locksMu sync.Mutex
	locks   = map[string]*heldLock{}
)

// Acquire waits for the lock until ctx is done.
func Acquire(ctx context.Context, path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// Resolve the folder, which exists, so the key is the same before and after the file exists.
	path = filepath.Join(resolvePath(filepath.Dir(path)), filepath.Base(path))
	for {
		if l, ok := reenter(path); ok {
			return l, nil
		}
		l, err := tryLock(path)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, ErrLockBusy) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (lock %s)", ErrLockBusy, path)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// AcquireWithin gives up after d.
func AcquireWithin(path string, d time.Duration) (*Lock, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return Acquire(ctx, path)
}

func reenter(path string) (*Lock, bool) {
	locksMu.Lock()
	defer locksMu.Unlock()
	if h, ok := locks[path]; ok {
		h.n++
		return &Lock{path: path}, true
	}
	return nil, false
}

func tryLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrLockBusy
		}
		return nil, err
	}
	locksMu.Lock()
	locks[path] = &heldLock{f: f, n: 1}
	locksMu.Unlock()
	return &Lock{path: path}, nil
}

// Release drops one hold; the file is unlocked when none remain.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	locksMu.Lock()
	defer locksMu.Unlock()
	h, ok := locks[l.path]
	if !ok {
		return
	}
	h.n--
	if h.n > 0 {
		return
	}
	syscall.Flock(int(h.f.Fd()), syscall.LOCK_UN)
	h.f.Close()
	delete(locks, l.path)
}
