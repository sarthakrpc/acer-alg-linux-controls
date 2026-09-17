// Package kbd controls the keyboard backlight of the Acer ALG AL15G-53.
//
// Derived from DSDT method SCMD case 0x67, which packs a 32-bit word and then
// issues plain EC commands. We issue those commands directly:
//
//	FCMD=0xC4, FDAT=0x02, FBUF=<0..255>          brightness
//	FCMD=0xCA, FDAT=<3|4|5>, FBUF/FBF1/FBF2      per-zone colour
//	FCMD=0xC4, FDAT=<0x07..0x0B>                 built-in effects
//
// Two things were established by experiment rather than from the DSDT:
//
//   - The colour bytes are ordered BLUE, RED, GREEN on this panel:
//     FBUF=blue, FBF1=red, FBF2=green. The DSDT packs its 32-bit word as if
//     the bytes were R,G,B, so the panel's LED wiring does not follow the ACPI
//     convention. Do not "fix" this to RGB without retesting - the primaries
//     alone cannot catch a two-channel swap, which is why `alg kbd test`
//     cycles the secondaries (yellow, cyan, magenta) as well.
//   - The 0x0D/0x0E on/off commands do nothing here. Off is brightness 0.
//
// The firmware's RGB block at 0xFE0B0380 is a boot-time config buffer, not
// live state, so backlight settings cannot be read back from hardware - hence
// the state file.
package kbd

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"alg/internal/ec"
	"alg/internal/fan"
	"alg/internal/paths"
)

const (
	subBrightness = 0x02
	MaxBrightness = 255
)

type RGB [3]int

// ZoneNames is in left-to-right order; ZoneID maps a name to its EC selector.
var ZoneNames = []string{"left", "middle", "right"}

func ZoneID(name string) (byte, bool) {
	for i, n := range ZoneNames {
		if n == name {
			return byte(3 + i), true
		}
	}
	return 0, false
}

var Effects = map[string]byte{
	"breathe": 0x07, "wave": 0x08, "random": 0x09, "flash": 0x0A, "cycle": 0x0B,
}

func EffectNames() []string {
	names := make([]string, 0, len(Effects))
	for n := range Effects {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return Effects[names[i]] < Effects[names[j]] })
	return names
}

var Colors = map[string]RGB{
	"off": {0, 0, 0}, "black": {0, 0, 0},
	"white": {255, 255, 255}, "red": {255, 0, 0}, "green": {0, 255, 0},
	"blue": {0, 0, 255}, "yellow": {255, 255, 0}, "cyan": {0, 255, 255},
	"magenta": {255, 0, 255}, "purple": {160, 32, 240},
	"orange": {255, 90, 0}, "pink": {255, 60, 120},
}

func ColorNames() []string {
	names := make([]string, 0, len(Colors))
	for n := range Colors {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// TestSequence checks the channel mapping. Primaries alone cannot catch every
// mis-mapping - two channels could be swapped and each primary would still
// look right on its own. The secondaries mix two channels each, so any swap
// is immediately visible.
var TestSequence = []struct {
	Name  string
	Color RGB
}{
	{"RED", RGB{255, 0, 0}}, {"GREEN", RGB{0, 255, 0}}, {"BLUE", RGB{0, 0, 255}},
	{"YELLOW  (red+green)", RGB{255, 255, 0}},
	{"CYAN    (green+blue)", RGB{0, 255, 255}},
	{"MAGENTA (red+blue)", RGB{255, 0, 255}},
	{"WHITE", RGB{255, 255, 255}},
}

type Backlight struct {
	bus ec.Bus
}

func New(bus ec.Bus) *Backlight { return &Backlight{bus: bus} }

func (b *Backlight) SetBrightness(raw int) error {
	return b.bus.Command(ec.CmdKbdCtl, subBrightness, byte(max(0, min(255, raw))))
}

func (b *Backlight) SetZone(zone byte, c RGB) error {
	// Panel channel order is B, R, G - see the package comment.
	return b.bus.Command(ec.CmdKbdColor, zone, byte(c[2]), byte(c[0]), byte(c[1]))
}

func (b *Backlight) SetAll(c RGB) error {
	for _, name := range ZoneNames {
		id, _ := ZoneID(name)
		if err := b.SetZone(id, c); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backlight) Effect(code byte) error {
	return b.bus.Command(ec.CmdKbdCtl, code)
}

// ParseColor accepts a colour name, #rrggbb or #rgb.
func ParseColor(text string) (RGB, error) {
	key := strings.ToLower(strings.TrimSpace(text))
	if c, ok := Colors[key]; ok {
		return c, nil
	}
	h := strings.TrimLeft(key, "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) == 6 {
		if v, err := strconv.ParseUint(h, 16, 32); err == nil {
			return RGB{int(v >> 16), int(v >> 8 & 0xFF), int(v & 0xFF)}, nil
		}
	}
	return RGB{}, fmt.Errorf("cannot read colour '%s'. Use a name (%s) or #rrggbb",
		text, strings.Join(ColorNames(), ", "))
}

func (c RGB) Hex() string { return fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2]) }

func (c RGB) Valid() bool {
	for _, v := range c {
		if v < 0 || v > 255 {
			return false
		}
	}
	return true
}

// State is the last backlight configuration alg applied.
type State struct {
	Brightness int `json:"brightness"`
	// LastOn remembers the level to come back to after `alg kbd off`.
	LastOn int            `json:"last_on,omitempty"`
	Zones  map[string]RGB `json:"zones"`
}

// LoadState reads the saved backlight state; ok is false when there is none.
func LoadState() (State, bool) {
	st := State{Brightness: MaxBrightness, Zones: map[string]RGB{}}
	b, err := os.ReadFile(paths.KbdState())
	if err != nil {
		return st, false
	}
	if json.Unmarshal(b, &st) != nil {
		return State{Brightness: MaxBrightness, Zones: map[string]RGB{}}, false
	}
	if st.Zones == nil {
		st.Zones = map[string]RGB{}
	}
	st.Brightness = max(0, min(MaxBrightness, st.Brightness))
	return st, true
}

func SaveState(st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return fan.WriteFileAtomic(paths.KbdState(), append(b, '\n'))
}

func PctToRaw(p float64) int {
	return int(math.RoundToEven(math.Max(0, math.Min(100, p)) * MaxBrightness / 100))
}

func RawToPct(v int) int {
	return int(math.Round(float64(v) * 100 / MaxBrightness))
}
