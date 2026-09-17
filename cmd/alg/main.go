// alg - fan and keyboard backlight control for the Acer ALG AL15G-53.
//
// The board is a Clevo/Tongfang design wearing an Acer badge, which is why
// acer-wmi never binds to it. See the README for the full derivation.
//
// One command with subcommands:
//
//	alg status          overview of fans, temperatures and backlight
//	alg gui             the graphical control panel
//	alg tray            the system tray icon
//	alg fan ...         fan control
//	alg kbd ...         keyboard backlight control
//	alg resume          reapply saved settings (run by the sleep hook)
//	alg daemon          the root service everything else talks to
//
// Only the daemon touches hardware. Every other command is an unprivileged
// client of its socket, so nothing here needs sudo.
//
// The legacy names algfan / algkbd / algui are installed as symlinks to this
// same binary and dispatch on argv[0], so anything you learned before still
// works.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"alg/internal/daemon"
	"alg/internal/proto"
	"alg/internal/session"
)

const usage = `alg - fan and keyboard backlight control for the Acer ALG AL15G-53

usage:
  alg status                 fans, temperatures and backlight at a glance
  alg gui [--page PAGE]      graphical control panel
  alg tray                   system tray icon with the live temperature
  alg fan <command>          fan control      (try: alg fan --help)
  alg kbd <command>          keyboard backlight (try: alg kbd --help)

common:
  alg fan status             duty, rpm and temperatures
  alg fan monitor            live view
  alg fan set 70             pin both fans to 70%
  alg fan auto               hand the fans back to the firmware
  alg fan curve quiet        follow a temperature curve
  alg fan profiles           list curve profiles
  alg kbd brightness 70      backlight brightness, 0-100
  alg kbd color white        colour by name or #rrggbb
  alg kbd off / alg kbd on   toggle the backlight

service:
  alg reload                 re-read /etc/alg.conf
  alg resume                 reapply saved settings after waking
  alg daemon                 the root service itself (run by systemd)

Settings are applied by the alg daemon, so none of this needs sudo.
`

// usageError is a mistake in how a command was invoked; it exits 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error {
	return usageError{fmt.Sprintf(format, a...)}
}

func main() {
	err := run(filepath.Base(os.Args[0]), os.Args[1:])
	if err == nil {
		return
	}
	var ue usageError
	if errors.As(err, &ue) {
		if ue.msg != "" { // otherwise the flag package has already explained
			fmt.Fprintf(os.Stderr, "alg: %v\n", err)
		}
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "alg: %v\n", err)
	os.Exit(1)
}

func run(prog string, args []string) error {
	// Legacy entry points, kept working via symlinks on argv[0].
	switch prog {
	case "algfan":
		return fanMain(args, "algfan")
	case "algkbd":
		return kbdMain(args, "algkbd")
	case "algui":
		return guiMain(args)
	}

	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	case "-V", "--version", "version":
		fmt.Println("alg", proto.Version)
		return nil
	case "status":
		return overview()
	case "gui":
		return guiMain(rest)
	case "tray":
		return trayMain(rest)
	case "fan":
		return fanMain(rest, "alg fan")
	case "kbd":
		return kbdMain(rest, "alg kbd")
	case "resume":
		return resumeMain(rest)
	case "reload":
		return call(proto.Request{Op: proto.OpReload}, nil)
	case "daemon":
		return daemonMain(rest)
	}
	fmt.Fprint(os.Stderr, usage, "\n")
	return usagef("unknown command '%s'", cmd)
}

func overview() error {
	if err := fanStatus(); err != nil {
		return err
	}
	fmt.Println()
	return kbdStatus()
}

func daemonMain(args []string) error {
	fs := newFlags("alg daemon", "", "The root service. Normally run by systemd as alg.service.")
	cleanup := fs.Bool("cleanup", false, "hand an orphaned fan curve back to the firmware, then exit")
	if err := parse(fs, args, 0, 0); err != nil {
		return err
	}
	if *cleanup {
		return daemon.Cleanup()
	}
	return daemon.Run()
}

// guiMain hands over to the separate GUI binary. It is kept out of this one
// so that the daemon, which runs as root, stays a small static executable
// with no graphics stack linked into it.
func guiMain(args []string) error {
	path, err := session.Sibling("alg-gui")
	if err != nil {
		return errors.New("alg-gui is not installed next to alg")
	}
	return syscall.Exec(path, append([]string{"alg-gui"}, args...), os.Environ())
}
