package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"

	"alg/internal/kbd"
	"alg/internal/paths"
	"alg/internal/proto"
)

const (
	maxClients = 32
	maxLine    = 64 * 1024
)

type Server struct {
	ctl   *Controller
	ln    *net.UnixListener
	slots chan struct{}
}

// Listen creates the control socket. The socket itself is open to every
// local user; what a connection may do is decided per request from the
// peer's credentials, so read-only status works for anyone.
func Listen(ctl *Controller) (*Server, error) {
	sock := paths.Socket()
	if err := os.MkdirAll(paths.RunDir, 0o755); err != nil {
		return nil, err
	}
	// A socket file left behind by a killed daemon would make bind fail.
	// Only remove it when nothing is answering on it.
	if conn, err := net.Dial("unix", sock); err == nil {
		conn.Close()
		return nil, fmt.Errorf("another alg daemon is already listening on %s", sock)
	}
	os.Remove(sock)

	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sock, 0o666); err != nil {
		ln.Close()
		return nil, err
	}
	return &Server{ctl: ctl, ln: ln, slots: make(chan struct{}, maxClients)}, nil
}

func (s *Server) Close() error { return s.ln.Close() }

// Serve accepts clients until the listener is closed.
func (s *Server) Serve() error {
	for {
		conn, err := s.ln.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		select {
		case s.slots <- struct{}{}:
			go func() {
				defer func() { <-s.slots }()
				s.handle(conn)
			}()
		default:
			conn.Close() // too many clients; this one can retry
		}
	}
}

func peerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var (
		cred    *syscall.Ucred
		credErr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return cred.Uid, nil
}

// authorised reports whether uid may change settings: root, the user the
// daemon itself runs as, or a member of one of the configured groups.
//
// Membership comes from the user database rather than the peer's process
// credentials, so adding someone to a group works without them logging out.
func authorised(uid uint32, groups []string) bool {
	if uid == 0 || int(uid) == os.Geteuid() {
		return true
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return false
	}
	gids, err := u.GroupIds()
	if err != nil {
		return false
	}
	for _, gid := range gids {
		g, err := user.LookupGroupId(gid)
		if err != nil {
			continue
		}
		for _, allowed := range groups {
			if g.Name == allowed {
				return true
			}
		}
	}
	return false
}

func (s *Server) handle(conn *net.UnixConn) {
	defer conn.Close()

	groups := s.ctl.Settings().AllowGroups
	uid, err := peerUID(conn)
	mayWrite := err == nil && authorised(uid, groups)

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 4096), maxLine)
	enc := json.NewEncoder(conn)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req proto.Request
		resp := proto.Response{}
		if err := json.Unmarshal(line, &req); err != nil {
			resp.Error = "bad json: " + err.Error()
		} else {
			resp.ID = req.ID
			var data any
			switch {
			case !proto.ReadOnly[req.Op] && !mayWrite:
				err = fmt.Errorf("not authorised: changing settings needs root or membership of one of "+
					"these groups: %s", strings.Join(groups, ", "))
			default:
				data, err = s.dispatch(&req)
			}
			if err != nil {
				resp.Error = err.Error()
			} else {
				resp.OK = true
				if data != nil {
					resp.Data, _ = json.Marshal(data)
				}
			}
		}
		if enc.Encode(&resp) != nil {
			return
		}
	}
}

func (s *Server) dispatch(req *proto.Request) (any, error) {
	c := s.ctl
	switch req.Op {
	case proto.OpPing:
		return proto.Pong{PID: os.Getpid(), Version: proto.Version}, nil
	case proto.OpStatus:
		return c.Status()
	case proto.OpProfiles:
		return c.Profiles(), nil
	case proto.OpKbdState:
		return c.KbdState(), nil

	case proto.OpFanSet:
		if req.Percent == nil {
			return nil, errors.New("fan_set needs a percent")
		}
		res, err := c.SetManual(*req.Percent, req.Fan, req.Verify)
		if err == nil {
			log.Printf("fans -> %s", res.Mode)
		}
		return res, err
	case proto.OpFanAuto:
		err := c.SetAuto()
		if err == nil {
			log.Print("fans -> firmware curve")
		}
		return nil, err
	case proto.OpFanCurve:
		profile, err := c.SetCurve(req.Profile)
		return map[string]string{"profile": profile}, err
	case proto.OpReload:
		return nil, c.Reload()
	case proto.OpResume:
		notes, err := c.Restore(false, req.Retries)
		if err == nil {
			log.Printf("restored %s", strings.Join(notes, "; "))
		}
		return proto.ResumeResult{Notes: notes}, err

	case proto.OpKbdBrightness:
		if req.Percent == nil {
			return nil, errors.New("kbd_brightness needs a percent")
		}
		raw, err := c.KbdBrightness(*req.Percent)
		return map[string]int{"raw": raw}, err
	case proto.OpKbdOn:
		raw, err := c.KbdOn()
		return map[string]int{"raw": raw, "percent": kbd.RawToPct(raw)}, err
	case proto.OpKbdOff:
		return nil, c.KbdOff()
	case proto.OpKbdColor:
		return c.KbdColor(kbd.RGB{req.R, req.G, req.B}, req.Zone)
	case proto.OpKbdEffect:
		return nil, c.KbdEffect(req.Name)
	case proto.OpKbdRestore:
		note, err := c.RestoreBacklight()
		return map[string]string{"note": note}, err
	}
	return nil, fmt.Errorf("unknown op '%s'", req.Op)
}
