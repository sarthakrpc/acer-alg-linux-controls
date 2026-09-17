package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fyne.io/systray"
	"github.com/godbus/dbus/v5"

	"alg/internal/proto"
	"alg/internal/session"
	"alg/internal/trayicon"
)

const (
	trayTitle = "ALG Control"
	trayPoll  = 5 * time.Second
)

// trayMain runs the system tray icon.
//
// It lives in this binary rather than in the control panel on purpose. A GUI
// toolkit keeps a render loop ticking even with no window on screen, which is
// a poor trade for something that sits in the panel all day. This is plain
// D-Bus: it sleeps between polls and costs effectively nothing. The window is
// a separate program that exists only while it is open.
func trayMain(args []string) error {
	fs := newFlags("alg tray", "",
		"System tray icon: shows the hottest temperature, with a menu for the\n"+
			"common fan settings and for opening the control panel.")
	if err := parse(fs, args, 0, 0); err != nil {
		return err
	}

	lock, err := session.LockTray()
	if errors.Is(err, session.ErrHeld) {
		return nil // one icon is enough
	}
	if err == nil {
		defer lock.Close()
	}
	// No tray host yet is normal, not an error: started from the login
	// autostart entry, this usually comes up before the panel does. The icon
	// registers itself the moment a host appears (and again if the panel is
	// restarted), so just say so and carry on.
	if !trayHostPresent() {
		fmt.Fprintln(os.Stderr, "alg tray: no system tray is up yet; the icon will appear when one is")
	}

	t := &trayApp{}
	systray.Run(t.ready, func() {})
	return nil
}

// trayHostPresent reports whether anything on the session bus is currently
// displaying tray icons.
func trayHostPresent() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	var owned bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&owned)
	return err == nil && owned
}

type trayApp struct {
	daemon  proto.Session
	lastKey string
	lastTip string
}

func (t *trayApp) ready() {
	systray.SetTitle(trayTitle)
	t.show("offline", trayicon.Offline, "connecting to the alg daemon...")

	open := systray.AddMenuItem("Show control panel", "")
	systray.AddSeparator()
	type action struct {
		item *systray.MenuItem
		req  proto.Request
	}
	actions := []action{
		{systray.AddMenuItem("Fans: automatic", ""), proto.Request{Op: proto.OpFanAuto}},
		{systray.AddMenuItem("Fans: curve", ""), proto.Request{Op: proto.OpFanCurve}},
		{systray.AddMenuItem("Fans: 50%", ""), proto.Request{Op: proto.OpFanSet, Percent: proto.Pct(50)}},
		{systray.AddMenuItem("Fans: 75%", ""), proto.Request{Op: proto.OpFanSet, Percent: proto.Pct(75)}},
		{systray.AddMenuItem("Fans: 100%", ""), proto.Request{Op: proto.OpFanSet, Percent: proto.Pct(100)}},
	}
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "")

	// Left-click opens the panel on desktops that pass the click through.
	systray.SetOnTapped(openWindow)

	// Everything that talks to the daemon happens on this one goroutine, so
	// the session needs no locking.
	requests := make(chan proto.Request, 8)
	for _, a := range actions {
		go func() {
			for range a.item.ClickedCh {
				requests <- a.req
			}
		}()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		tick := time.NewTicker(trayPoll)
		defer tick.Stop()
		t.poll()
		for {
			select {
			case <-tick.C:
				t.poll()
			case req := <-requests:
				if err := t.daemon.Call(req, callTimeout, nil); err != nil {
					t.show("error", trayicon.Offline, "Error: "+err.Error())
					continue
				}
				t.poll()
			case <-open.ClickedCh:
				openWindow()
			case <-quit.ClickedCh:
				// Quit means the whole app, as it always has: take the
				// window down too if it is open.
				session.TellWindow("quit")
				systray.Quit()
				return
			case <-stop:
				systray.Quit()
				return
			}
		}
	}()
}

func openWindow() {
	path, err := session.Sibling("alg-gui")
	if err == nil {
		// If the panel is already open the new process just raises it.
		err = session.Spawn(path)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "alg tray: cannot open the control panel:", err)
	}
}

func (t *trayApp) poll() {
	var st proto.Status
	if err := t.daemon.Call(proto.Request{Op: proto.OpStatus}, callTimeout, &st); err != nil {
		tip := "Error: " + err.Error()
		if errors.Is(err, proto.ErrNoDaemon) {
			tip = "the alg daemon is not running"
		}
		t.show("offline", trayicon.Offline, tip)
		return
	}
	temp := int(st.Hottest + 0.5)
	rpm := 0
	for _, f := range st.Fans {
		rpm = max(rpm, f.RPM)
	}
	t.show(fmt.Sprint(temp), func() ([]byte, error) { return trayicon.Temperature(temp) },
		fmt.Sprintf("%d°C - fans %d rpm - %s", temp, rpm, st.Mode))
}

// show updates the icon and tooltip, but only when they would actually
// differ: every change is a round of D-Bus traffic and a repaint in the panel.
func (t *trayApp) show(key string, draw func() ([]byte, error), tip string) {
	if key != t.lastKey {
		if img, err := draw(); err == nil {
			systray.SetIcon(img)
			t.lastKey = key
		}
	}
	if tip != t.lastTip {
		systray.SetTooltip(trayTitle + ": " + tip)
		t.lastTip = tip
	}
}
