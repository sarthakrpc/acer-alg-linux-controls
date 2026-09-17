package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"alg/internal/kbd"
	"alg/internal/proto"
)

const kbdUsage = `usage: %s <command>

Keyboard backlight control.

commands:
  status                        show last applied settings
  on | off                      turn the backlight on or off
  brightness PERCENT            set brightness 0-100
  color COLOR [--zone ZONE]     set a colour (zone: left, middle, right, all)
  effect NAME                   run a built-in firmware effect
                                (%s)
  test [-s SECONDS]             cycle colours to check the mapping
  restore                       reapply saved settings

colours: %s, or #rrggbb
`

func printKbdUsage(out *os.File, prog string) {
	fmt.Fprintf(out, kbdUsage, prog,
		strings.Join(kbd.EffectNames(), ", "), strings.Join(kbd.ColorNames(), ", "))
}

func kbdMain(args []string, prog string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printKbdUsage(os.Stdout, prog)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "status":
		return kbdStatus()
	case "on":
		var res struct {
			Percent int `json:"percent"`
		}
		if err := call(proto.Request{Op: proto.OpKbdOn}, &res); err != nil {
			return err
		}
		fmt.Printf("backlight on (%d%%)\n", res.Percent)
		return nil
	case "off":
		if err := call(proto.Request{Op: proto.OpKbdOff}, nil); err != nil {
			return err
		}
		fmt.Println("backlight off")
		return nil
	case "brightness":
		return kbdBrightness(rest, prog)
	case "color", "colour":
		return kbdColor(rest, prog)
	case "effect":
		return kbdEffect(rest, prog)
	case "test":
		return kbdTest(rest, prog)
	case "restore":
		if err := call(proto.Request{Op: proto.OpKbdRestore}, nil); err != nil {
			return err
		}
		fmt.Println("restored the saved backlight settings")
		return nil
	}
	printKbdUsage(os.Stderr, prog)
	fmt.Fprintln(os.Stderr)
	return usagef("unknown kbd command '%s'", cmd)
}

func kbdStatus() error {
	var st proto.KbdState
	if err := call(proto.Request{Op: proto.OpKbdState}, &st); err != nil {
		return err
	}
	fmt.Println("Keyboard backlight (last values applied by alg)")
	if !st.Known {
		fmt.Println("  nothing set by alg yet")
	} else {
		fmt.Printf("  brightness : %d%% (raw %d/255)\n", st.Percent, st.Raw)
		if len(st.Zones) == 0 {
			fmt.Println("  colour     : not set by alg yet")
		}
		for _, name := range kbd.ZoneNames {
			if rgb, ok := st.Zones[name]; ok {
				fmt.Printf("  %-10s : %s\n", name, rgb.Hex())
			}
		}
	}
	fmt.Println("\nThe EC exposes no live backlight state, so this reflects what alg")
	fmt.Println("last set - not a hardware read. Changes made from Windows or with")
	fmt.Println("the Fn key will not show up here.")
	return nil
}

func kbdBrightness(args []string, prog string) error {
	fs := newFlags(prog+" brightness", "PERCENT", "Set the backlight brightness, 0-100.")
	if err := parse(fs, args, 1, 1); err != nil {
		return err
	}
	pct, err := parsePercent(fs.Arg(0))
	if err != nil {
		return err
	}
	var res struct {
		Raw int `json:"raw"`
	}
	if err := call(proto.Request{Op: proto.OpKbdBrightness, Percent: proto.Pct(pct)}, &res); err != nil {
		return err
	}
	fmt.Printf("brightness -> %g%% (raw %d/255)\n", pct, res.Raw)
	return nil
}

func validZone(zone string) bool {
	_, ok := kbd.ZoneID(zone)
	return ok || zone == "all"
}

func kbdColor(args []string, prog string) error {
	fs := newFlags(prog+" color", "COLOR",
		"Set a colour by name or #rrggbb.\n\ncolours: "+strings.Join(kbd.ColorNames(), ", "))
	zone := fs.String("zone", "all", "left, middle, right or all")
	if err := parse(fs, args, 1, 1); err != nil {
		return err
	}
	if !validZone(*zone) {
		return usagef("--zone must be left, middle, right or all")
	}
	rgb, err := kbd.ParseColor(fs.Arg(0))
	if err != nil {
		return usageError{err.Error()}
	}
	var res proto.KbdColorResult
	req := proto.Request{Op: proto.OpKbdColor, R: rgb[0], G: rgb[1], B: rgb[2], Zone: *zone}
	if err := call(req, &res); err != nil {
		return err
	}
	if res.Raised {
		fmt.Println("(the backlight was off - turned it up so you can see the colour)")
	}
	where := *zone + " zone"
	if *zone == "all" {
		where = "all zones"
	}
	fmt.Printf("%s -> %s\n", where, rgb.Hex())
	return nil
}

func kbdEffect(args []string, prog string) error {
	fs := newFlags(prog+" effect", "NAME",
		"Run a built-in firmware effect: "+strings.Join(kbd.EffectNames(), ", "))
	if err := parse(fs, args, 1, 1); err != nil {
		return err
	}
	name := fs.Arg(0)
	code, ok := kbd.Effects[name]
	if !ok {
		return usagef("unknown effect '%s' (choose from %s)", name, strings.Join(kbd.EffectNames(), ", "))
	}
	if err := call(proto.Request{Op: proto.OpKbdEffect, Name: name}, nil); err != nil {
		return err
	}
	fmt.Printf("effect '%s' sent (code 0x%02X)\n", name, code)
	fmt.Println("These are firmware effects and were not individually verified on this model.")
	fmt.Printf("Run `%s color <name>` to go back to a static colour.\n", prog)
	return nil
}

func kbdTest(args []string, prog string) error {
	fs := newFlags(prog+" test", "", "Cycle seven colours to check the channel mapping.")
	var secs float64
	fs.Float64Var(&secs, "s", 3.0, "seconds per colour")
	fs.Float64Var(&secs, "seconds", 3.0, "seconds per colour")
	if err := parse(fs, args, 0, 0); err != nil {
		return err
	}

	c, err := proto.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Call(proto.Request{Op: proto.OpKbdBrightness, Percent: proto.Pct(100)}, callTimeout, nil); err != nil {
		return err
	}
	for _, step := range kbd.TestSequence {
		fmt.Printf("  -> %s\n", step.Name)
		rgb := step.Color
		req := proto.Request{Op: proto.OpKbdColor, R: rgb[0], G: rgb[1], B: rgb[2], Zone: "all"}
		if err := c.Call(req, callTimeout, nil); err != nil {
			return err
		}
		time.Sleep(time.Duration(secs * float64(time.Second)))
	}
	fmt.Println("\nAll seven should have matched their labels. If any did not, say")
	fmt.Println("which one showed what and the mapping can be corrected.")
	return nil
}
