// Package paths holds every filesystem location alg touches.
//
// The ALG_* environment overrides exist so the daemon can be run against a
// fake EC as an ordinary user while developing. The installed service never
// sets them.
package paths

import (
	"os"
	"path/filepath"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var (
	ECIO     = env("ALG_EC_IO", "/sys/kernel/debug/ec/ec0/io")
	Config   = env("ALG_CONFIG", "/etc/alg.conf")
	RunDir   = env("ALG_RUN_DIR", "/run/alg")
	StateDir = env("ALG_STATE_DIR", "/var/lib/alg")
)

// ECIsDefault reports whether the real kernel EC interface is in use, as
// opposed to a development stand-in.
func ECIsDefault() bool { return os.Getenv("ALG_EC_IO") == "" }

func Socket() string   { return filepath.Join(RunDir, "alg.sock") }
func FanMode() string  { return filepath.Join(StateDir, "fan-mode") }
func KbdState() string { return filepath.Join(StateDir, "kbd.json") }
