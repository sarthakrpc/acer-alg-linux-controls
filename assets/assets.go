// Package assets embeds the files the GUI needs at run time, so the binary
// does not depend on anything being installed alongside it.
package assets

import _ "embed"

//go:embed alg.svg
var Icon []byte
