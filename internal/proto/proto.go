// Package proto is the wire protocol between the root daemon and its
// clients (the CLI and the GUI).
//
// One JSON object per line, both directions, over a Unix stream socket:
//
//	-> {"id": 1, "op": "status"}
//	<- {"id": 1, "ok": true, "data": {...}}
//	<- {"id": 1, "ok": false, "error": "..."}
package proto

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"alg/internal/kbd"
	"alg/internal/paths"
)

const Version = "2.0"

const (
	OpPing          = "ping"
	OpStatus        = "status"
	OpProfiles      = "profiles"
	OpKbdState      = "kbd_state"
	OpFanSet        = "fan_set"
	OpFanAuto       = "fan_auto"
	OpFanCurve      = "fan_curve"
	OpKbdBrightness = "kbd_brightness"
	OpKbdOn         = "kbd_on"
	OpKbdOff        = "kbd_off"
	OpKbdColor      = "kbd_color"
	OpKbdEffect     = "kbd_effect"
	OpKbdRestore    = "kbd_restore"
	OpResume        = "resume"
	OpReload        = "reload"
)

// ReadOnly ops are open to every local user; the rest need authorisation.
var ReadOnly = map[string]bool{
	OpPing: true, OpStatus: true, OpProfiles: true, OpKbdState: true,
}

type Request struct {
	ID int    `json:"id"`
	Op string `json:"op"`
	// Percent is a pointer so that a request which forgot it is rejected
	// rather than read as 0% - a stalled fan is not a safe default.
	Percent *float64 `json:"percent,omitempty"`
	Fan     int      `json:"fan,omitempty"` // 0 means every fan
	Verify  bool     `json:"verify,omitempty"`
	Profile string   `json:"profile,omitempty"`
	Zone    string   `json:"zone,omitempty"`
	R       int      `json:"r,omitempty"`
	G       int      `json:"g,omitempty"`
	B       int      `json:"b,omitempty"`
	Name    string   `json:"name,omitempty"`
	Retries int      `json:"retries,omitempty"`
}

// Pct wraps a percentage for Request.Percent.
func Pct(v float64) *float64 { return &v }

type Response struct {
	ID    int             `json:"id"`
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

type Pong struct {
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

type FanStatus struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Duty    int    `json:"duty"`
	Percent int    `json:"percent"`
	RPM     int    `json:"rpm"`
}

type Status struct {
	Fans          []FanStatus        `json:"fans"`
	Temps         map[string]float64 `json:"temps"`
	Hottest       float64            `json:"hottest"`
	HottestSource string             `json:"hottest_source"`
	// Mode is the human-readable line; Kind, Profile and Manual break the
	// same thing down for programs.
	Mode    string          `json:"mode"`
	Kind    string          `json:"kind"`
	Profile string          `json:"profile,omitempty"`
	Manual  map[int]float64 `json:"manual,omitempty"`
}

type FanSetResult struct {
	Percent float64 `json:"percent"`
	Raw     int     `json:"raw"`
	// Unconfirmed lists fans whose duty the EC never reflected. Only filled
	// in when the request asked for verification.
	Unconfirmed []int  `json:"unconfirmed,omitempty"`
	Mode        string `json:"mode"`
}

type ProfileInfo struct {
	Name    string `json:"name"`
	Points  string `json:"points"`
	Builtin bool   `json:"builtin"`
}

type Profiles struct {
	Profiles []ProfileInfo `json:"profiles"`
	// Current is the profile in use, or the configured default when the
	// curve is not running.
	Current string `json:"current"`
	Default string `json:"default"`
}

type KbdState struct {
	Percent int                `json:"brightness"`
	Raw     int                `json:"raw"`
	Zones   map[string]kbd.RGB `json:"zones"`
	// Known is false when alg has never set the backlight on this machine.
	Known bool `json:"known"`
}

type KbdColorResult struct {
	Color kbd.RGB `json:"rgb"`
	Zone  string  `json:"zone"`
	// Raised is set when the backlight had to be turned up from zero so the
	// colour would be visible at all.
	Raised bool `json:"raised,omitempty"`
}

type ResumeResult struct {
	Notes []string `json:"notes"`
}

// RemoteError is a request the daemon received and turned down, as opposed
// to a failure to reach it at all.
type RemoteError struct{ Msg string }

func (e *RemoteError) Error() string { return e.Msg }

// ErrNoDaemon means nothing is listening on the socket.
var ErrNoDaemon = errors.New("the alg daemon is not running. Start it with:\n" +
	"    sudo systemctl start alg")

// Client is a connection to the daemon. It is not safe for concurrent use.
type Client struct {
	conn   net.Conn
	r      *bufio.Reader
	nextID int
}

func Dial() (*Client, error) {
	conn, err := net.DialTimeout("unix", paths.Socket(), 3*time.Second)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, ErrNoDaemon
		}
		return nil, err
	}
	return &Client{conn: conn, r: bufio.NewReader(conn), nextID: 1}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Call sends one request and decodes the reply's data into out (which may be
// nil). An error reply from the daemon comes back as an error.
func (c *Client) Call(req Request, timeout time.Duration, out any) error {
	req.ID = c.nextID
	c.nextID++
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	c.conn.SetDeadline(time.Now().Add(timeout))
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("lost the connection to the daemon: %w", err)
	}
	reply, err := c.r.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("lost the connection to the daemon: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(reply, &resp); err != nil {
		return fmt.Errorf("bad reply from the daemon: %w", err)
	}
	if !resp.OK {
		return &RemoteError{resp.Error}
	}
	if out != nil && len(resp.Data) > 0 {
		return json.Unmarshal(resp.Data, out)
	}
	return nil
}

// Session is a Client for long-lived programs: it connects on first use and
// reconnects after the connection breaks, which is what lets the tray and
// the window ride out a daemon restart. Not safe for concurrent use.
type Session struct {
	client *Client
}

func (s *Session) Call(req Request, timeout time.Duration, out any) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if s.client == nil {
			if s.client, err = Dial(); err != nil {
				s.client = nil
				return err
			}
		}
		err = s.client.Call(req, timeout, out)
		var remote *RemoteError
		if err == nil || errors.As(err, &remote) {
			return err
		}
		// The connection itself broke, most likely because the daemon was
		// restarted. Every request is idempotent, so one retry on a fresh
		// connection is safe.
		s.Close()
	}
	return err
}

func (s *Session) Close() {
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
}
