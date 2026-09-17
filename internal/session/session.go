// Package session holds what the per-user desktop processes (the tray icon
// and the control panel window) need to find each other.
package session

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func runtimeDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}
	return os.TempDir()
}

// WindowSocket is where a running control panel listens for "show this page"
// and "quit" from later launches and from the tray.
func WindowSocket() string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("alg-gui-%d.sock", os.Getuid()))
}

// TellWindow sends one word to the running control panel. It reports false
// when there is no window to tell.
func TellWindow(word string) bool {
	conn, err := net.DialTimeout("unix", WindowSocket(), time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	fmt.Fprintln(conn, word)
	return true
}

// ErrHeld means another process already holds the lock.
var ErrHeld = errors.New("already running")

// LockTray makes this process the one tray icon for the session. The lock
// lives exactly as long as the returned file stays open, so a crashed tray
// can never leave a stale lock behind.
func LockTray() (*os.File, error) {
	path := filepath.Join(runtimeDir(), fmt.Sprintf("alg-tray-%d.lock", os.Getuid()))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, err
	}
	return f, nil
}

// Sibling finds another alg executable: next to this one first, then where
// the package installs it, then on PATH.
func Sibling(name string) (string, error) {
	if self, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(self); err == nil {
			self = real
		}
		if path := filepath.Join(filepath.Dir(self), name); isExecutable(path) {
			return path, nil
		}
	}
	if path := filepath.Join("/usr/bin", name); isExecutable(path) {
		return path, nil
	}
	return exec.LookPath(name)
}

func isExecutable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// Spawn starts a long-lived desktop process detached from this one, so it
// outlives us and is not tied to our terminal.
func Spawn(path string, args ...string) error {
	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap it if it exits before we do
	return nil
}
