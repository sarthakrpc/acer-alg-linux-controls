//go:build sim

package daemon

import (
	"log"
	"os"

	"alg/internal/ec"
	"alg/internal/ec/sim"
)

// openDevice in a `-tags sim` build can stand in a simulated controller, so
// the daemon, CLI and GUI can all be exercised as an ordinary user on any
// machine. Release builds do not contain this file.
func openDevice() (ec.Device, error) {
	if os.Getenv("ALG_EC_IO") == "sim" {
		log.Print("using a SIMULATED embedded controller - no hardware is touched")
		return sim.New(), nil
	}
	return ec.Open()
}
