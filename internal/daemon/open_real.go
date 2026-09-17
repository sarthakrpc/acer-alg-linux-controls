//go:build !sim

package daemon

import "alg/internal/ec"

func openDevice() (ec.Device, error) { return ec.Open() }
