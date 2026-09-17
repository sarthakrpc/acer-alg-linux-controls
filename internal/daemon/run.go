package daemon

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"alg/internal/config"
	"alg/internal/ec"
	"alg/internal/fan"
	"alg/internal/paths"
)

func openEC(retries int) (ec.Device, error) {
	var last error
	for attempt := 1; attempt <= retries; attempt++ {
		bus, err := openDevice()
		if err == nil {
			return bus, nil
		}
		last = err
		if attempt < retries {
			time.Sleep(600 * time.Millisecond)
		}
	}
	return nil, last
}

// Run is the daemon's main: restore the saved settings, then serve the
// control socket until told to stop.
func Run() error {
	log.SetFlags(0) // journald stamps the lines itself
	log.SetPrefix("alg: ")

	cfg, err := config.Load()
	if err != nil {
		log.Printf("could not read %s, using defaults: %v", paths.Config, err)
	}
	for _, w := range cfg.Warnings {
		log.Printf("config: %s", w)
	}

	bus, err := openEC(5)
	if err != nil {
		return err
	}
	defer bus.Close()

	ctl := NewController(bus, cfg)
	srv, err := Listen(ctl)
	if err != nil {
		return err
	}
	defer os.Remove(paths.Socket())

	// From here on, however this exits - cleanly or by signal - a running
	// curve is handed back to the firmware. Leaving the fans pinned at its
	// last duty with nothing watching the temperature would be dangerous.
	defer ctl.Shutdown()

	notes, err := ctl.Restore(true, 6)
	if err != nil {
		srv.Close()
		return err
	}
	log.Printf("restored %s", strings.Join(notes, "; "))

	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	served := make(chan error, 1)
	go func() { served <- srv.Serve() }()

	for {
		select {
		case s := <-sig:
			if s == syscall.SIGHUP {
				log.Print("reloading the config")
				if err := ctl.Reload(); err != nil {
					log.Print(err)
				}
				continue
			}
			srv.Close()
			return nil
		case err := <-served:
			return fmt.Errorf("the control socket failed: %w", err)
		}
	}
}

// Cleanup is the service's ExecStopPost safety net, for the case where the
// daemon was killed outright and never ran its own shutdown. Only a curve
// needs undoing; a manual speed is meant to outlive the process.
func Cleanup() error {
	mode, _ := fan.LoadMode()
	if mode.Kind != fan.CurveRun {
		return nil
	}
	bus, err := openDevice()
	if err != nil {
		return err
	}
	defer bus.Close()
	return fan.New(bus).AllAuto()
}
