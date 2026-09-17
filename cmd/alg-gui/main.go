// alg-gui - desktop control panel for the Acer ALG AL15G-53.
//
// Runs as your normal user and needs no password: it is a client of the alg
// daemon's socket, exactly like the command line.
//
// This process is only the window. The tray icon is `alg tray`, a separate
// and much lighter program that this one starts if it is not already there;
// closing the window ends this process and leaves the tray running. Settings
// stay put whether either of them is running or not.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"alg/assets"
	"alg/internal/proto"
	"alg/internal/session"
)

const (
	appID    = "org.alg.Control"
	appTitle = "ALG Control"

	pollEvery = 2 * time.Second
	debounce  = 200 * time.Millisecond
)

type ui struct {
	app    fyne.App
	win    fyne.Window
	link   *link
	tabs   *container.AppTabs
	status *widget.Label

	offline    bool // the last status poll failed
	warnedDown bool // ...and the user has been told why

	// syncing is set while widgets are being updated to match the daemon, so
	// their change handlers do not echo the update straight back as a command.
	syncing bool

	fans fansTab
	kbd  kbdTab
}

func main() {
	page := flag.String("page", "fans", "which page to open on: fans or keyboard")
	noTray := flag.Bool("no-tray", false, "do not start the system tray icon")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: alg gui [--page fans|keyboard] [--no-tray]\n\n%s control panel.\n\noptions:\n", appTitle)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *page != "fans" && *page != "keyboard" {
		flag.Usage()
		os.Exit(2)
	}

	// Launching it again should raise the window that is already there.
	instance, err := claimInstance(*page)
	if errors.Is(err, errAlreadyRunning) {
		return
	}

	if !*noTray {
		// It exits by itself if an icon is already up, or if this desktop
		// has no tray to put one in.
		if alg, err := session.Sibling("alg"); err == nil {
			session.Spawn(alg, "tray")
		}
	}

	app.SetMetadata(fyne.AppMetadata{ID: appID, Name: appTitle, Version: proto.Version})
	a := app.NewWithID(appID)
	u := newUI(a)
	u.selectPage(*page)
	u.win.SetMaster() // closing the window ends the process; the tray lives on

	if instance != nil {
		defer instance.Close()
		go instance.serve(func(word string) {
			runOnUI(func() {
				if word == "quit" {
					a.Quit()
					return
				}
				u.selectPage(word)
				u.win.Show()
				u.win.RequestFocus()
			})
		})
	}

	u.start()
	u.win.ShowAndRun()
}

// newUI builds the window. It does not talk to the daemon until start.
func newUI(a fyne.App) *ui {
	icon := fyne.NewStaticResource("alg.svg", assets.Icon)
	a.SetIcon(icon)

	u := &ui{app: a, link: newLink()}
	u.win = a.NewWindow(appTitle)
	u.win.SetIcon(icon)

	u.status = widget.NewLabel("connecting to the alg daemon...")
	u.status.SizeName = theme.SizeNameCaptionText
	u.status.Truncation = fyne.TextTruncateEllipsis

	u.tabs = container.NewAppTabs(
		container.NewTabItem("Fans", u.buildFansTab()),
		container.NewTabItem("Keyboard", u.buildKbdTab()),
	)
	u.win.SetContent(container.NewBorder(nil, u.status, nil, nil, u.tabs))
	u.win.Resize(fyne.NewSize(540, 570))
	return u
}

func (u *ui) start() {
	u.loadProfiles()
	u.loadKbdState()
	u.refresh()
	go u.poll()
}

func (u *ui) selectPage(page string) {
	if page == "keyboard" {
		u.tabs.SelectIndex(1)
	} else {
		u.tabs.SelectIndex(0)
	}
}

func (u *ui) poll() {
	for {
		time.Sleep(pollEvery)
		u.refresh()
	}
}

// refresh may be called from any goroutine.
func (u *ui) refresh() {
	u.link.status(u.onStatus)
}

func (u *ui) setStatus(text string) {
	u.status.Importance = widget.MediumImportance
	u.status.SetText(text)
}

// fail reports an error from the daemon and returns true if there was one.
func (u *ui) fail(err error) bool {
	if err == nil {
		return false
	}
	u.status.Importance = widget.DangerImportance
	u.status.SetText("Error: " + err.Error())
	if errors.Is(err, proto.ErrNoDaemon) && !u.warnedDown {
		u.warnedDown = true
		dialog.ShowInformation(appTitle,
			"The alg daemon is not running, so nothing can be changed yet.\n\n"+
				"Start it with:  sudo systemctl start alg\n\n"+
				"This window will connect by itself once it is up.", u.win)
	}
	return true
}

// ack is the completion handler for commands: report a failure, otherwise
// pull fresh status so the window reflects what just happened.
func (u *ui) ack(err error) {
	if !u.fail(err) {
		u.refresh()
	}
}

func (u *ui) onStatus(st *proto.Status, err error) {
	if u.fail(err) {
		u.offline = true
		return
	}
	if u.offline {
		// The daemon is back; the lists it could not give us earlier are
		// worth asking for again.
		u.offline, u.warnedDown = false, false
		u.loadProfiles()
		u.loadKbdState()
	}
	u.applyFanStatus(st)
	u.setStatus(fmt.Sprintf("%s - hottest %.0f°C (%s)", st.Mode, st.Hottest, st.HottestSource))
}
