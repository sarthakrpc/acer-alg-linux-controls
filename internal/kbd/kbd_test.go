package kbd_test

import (
	"os"
	"reflect"
	"testing"

	"alg/internal/ec"
	"alg/internal/ec/sim"
	"alg/internal/kbd"
	"alg/internal/paths"
)

func TestParseColorMatchesPython(t *testing.T) {
	cases := map[string]kbd.RGB{
		"white": {255, 255, 255}, "#FF8000": {255, 128, 0}, "f80": {255, 136, 0},
		"#0a0B0c": {10, 11, 12}, "Orange": {255, 90, 0}, " red ": {255, 0, 0},
	}
	for text, want := range cases {
		got, err := kbd.ParseColor(text)
		if err != nil || got != want {
			t.Errorf("ParseColor(%q) = %v, %v; want %v", text, got, err, want)
		}
	}
	for _, bad := range []string{"", "nope", "#12", "#12345", "#gggggg", "#1234567"} {
		if _, err := kbd.ParseColor(bad); err == nil {
			t.Errorf("ParseColor(%q) should fail", bad)
		}
	}
}

// The panel wants BLUE, RED, GREEN. Getting this wrong still shows correct
// primaries for some swaps, so pin the byte order down exactly.
func TestZoneChannelOrder(t *testing.T) {
	bus := sim.New()
	bl := kbd.New(bus)
	if err := bl.SetAll(kbd.RGB{0x11, 0x22, 0x33}); err != nil {
		t.Fatal(err)
	}
	if err := bl.SetBrightness(300); err != nil {
		t.Fatal(err)
	}
	if err := bl.Effect(kbd.Effects["wave"]); err != nil {
		t.Fatal(err)
	}
	want := []sim.Command{
		{Cmd: ec.CmdKbdColor, Payload: []byte{3, 0x33, 0x11, 0x22}},
		{Cmd: ec.CmdKbdColor, Payload: []byte{4, 0x33, 0x11, 0x22}},
		{Cmd: ec.CmdKbdColor, Payload: []byte{5, 0x33, 0x11, 0x22}},
		{Cmd: ec.CmdKbdCtl, Payload: []byte{0x02, 255}},
		{Cmd: ec.CmdKbdCtl, Payload: []byte{0x08}},
	}
	if got := bus.Commands(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n got %v\nwant %v", got, want)
	}
}

func TestStateReadsThePythonFile(t *testing.T) {
	paths.StateDir = t.TempDir()
	if _, ok := kbd.LoadState(); ok {
		t.Error("no state file, but LoadState reported one")
	}
	// Exactly what the Python version wrote.
	legacy := `{
  "brightness": 26,
  "zones": {
    "left": [255, 255, 255],
    "middle": [0, 128, 255],
    "right": [255, 255, 255]
  }
}`
	if err := os.WriteFile(paths.KbdState(), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	st, ok := kbd.LoadState()
	if !ok || st.Brightness != 26 || st.Zones["middle"] != (kbd.RGB{0, 128, 255}) {
		t.Fatalf("LoadState = %+v, %v", st, ok)
	}
	st.Brightness = 200
	if err := kbd.SaveState(st); err != nil {
		t.Fatal(err)
	}
	again, _ := kbd.LoadState()
	if !reflect.DeepEqual(st, again) {
		t.Errorf("round trip: %+v != %+v", st, again)
	}
}
