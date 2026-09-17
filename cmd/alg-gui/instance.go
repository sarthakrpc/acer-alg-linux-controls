package main

import (
	"bufio"
	"errors"
	"net"
	"os"
	"strings"
	"time"

	"alg/internal/session"
)

var errAlreadyRunning = errors.New("alg-gui is already running")

// instance is the listening end of the single-instance socket.
type instance struct {
	ln   net.Listener
	path string
}

// claimInstance makes this process the one control panel. If another is
// already open it is asked to show the requested page instead, and
// errAlreadyRunning is returned.
func claimInstance(page string) (*instance, error) {
	if session.TellWindow(page) {
		return nil, errAlreadyRunning
	}
	path := session.WindowSocket()
	os.Remove(path) // nothing answered, so whatever is there is stale
	ln, err := net.Listen("unix", path)
	if err != nil {
		// Not fatal: the panel still works, it just cannot be raised by
		// launching it a second time.
		return nil, err
	}
	os.Chmod(path, 0o600)
	return &instance{ln: ln, path: path}, nil
}

// serve passes on each word another process sends: a page name to show, or
// "quit" from the tray.
func (i *instance) serve(handle func(word string)) {
	for {
		conn, err := i.ln.Accept()
		if err != nil {
			return
		}
		conn.SetDeadline(time.Now().Add(time.Second))
		line, _ := bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		handle(strings.TrimSpace(line))
	}
}

func (i *instance) Close() {
	i.ln.Close()
	os.Remove(i.path)
}
