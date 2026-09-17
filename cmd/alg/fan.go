package main

import (
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"alg/internal/fan"
	"alg/internal/proto"
)

const fanUsage = `usage: %s <command>

Fan control.

commands:
  status                 show fan duty, rpm and temperatures
  monitor [-i SECONDS]   live view
  set PERCENT [--fan N]  set a fixed fan speed (default both fans)
  auto                   hand the fans back to the firmware
  curve [PROFILE]        follow a temperature curve (default: the profile
                         named in /etc/alg.conf)
  profiles               list available profiles

Whatever you choose stays in effect across sleep and reboots until you
choose something else.
`

func fanMain(args []string, prog string) error {
	// --config belonged to the old foreground daemon, which read the file
	// itself. Accept and ignore a leading one so existing scripts keep working.
	if len(args) >= 2 && args[0] == "--config" {
		args = args[2:]
	} else if len(args) >= 1 && strings.HasPrefix(args[0], "--config=") {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Printf(fanUsage, prog)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "status":
		return fanStatus()
	case "monitor":
		return fanMonitor(rest, prog)
	case "set":
		return fanSet(rest, prog)
	case "auto":
		return fanAuto(rest, prog)
	case "curve":
		return fanCurve(rest, prog)
	case "profiles":
		return fanProfiles()
	}
	fmt.Fprintf(os.Stderr, fanUsage+"\n", prog)
	return usagef("unknown fan command '%s'", cmd)
}

func fanName(id int) string {
	if id == 1 {
		return "fan1 (CPU)"
	}
	return fmt.Sprintf("fan%d (GPU)", id)
}

func fanStatus() error {
	var st proto.Status
	if err := call(proto.Request{Op: proto.OpStatus}, &st); err != nil {
		return err
	}
	fmt.Printf("mode : %s\n", st.Mode)
	fmt.Println("fans :")
	for _, f := range st.Fans {
		fmt.Printf("  %s  %3d%%  (duty %3d/255)   %5d rpm\n", fanName(f.ID), f.Percent, f.Duty, f.RPM)
	}
	fmt.Println("temps:")
	keys := make([]string, 0, len(st.Temps))
	for k := range st.Temps {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if st.Temps[keys[i]] != st.Temps[keys[j]] {
			return st.Temps[keys[i]] > st.Temps[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		fmt.Printf("  %-6s %5.1f C\n", k, st.Temps[k])
	}
	return nil
}

func fanMonitor(args []string, prog string) error {
	fs := newFlags(prog+" monitor", "", "Live view of temperatures and fan speeds. Ctrl-C to stop.")
	var interval float64
	fs.Float64Var(&interval, "i", 1.0, "seconds between updates")
	fs.Float64Var(&interval, "interval", 1.0, "seconds between updates")
	if err := parse(fs, args, 0, 0); err != nil {
		return err
	}
	if interval < 0.1 {
		interval = 0.1
	}

	c, err := proto.Dial()
	if err != nil {
		return err
	}
	defer c.Close()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	tick := time.NewTicker(time.Duration(interval * float64(time.Second)))
	defer tick.Stop()
	for {
		var st proto.Status
		if err := c.Call(proto.Request{Op: proto.OpStatus}, callTimeout, &st); err != nil {
			fmt.Println()
			return err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "\r%s  hot=%5.1fC(%-5s)  ", time.Now().Format("15:04:05"), st.Hottest, st.HottestSource)
		for _, f := range st.Fans {
			fmt.Fprintf(&b, "fan%d=%3d%%/%5drpm  ", f.ID, f.Percent, f.RPM)
		}
		fmt.Print(b.String(), "   ")
		select {
		case <-stop:
			fmt.Println()
			return nil
		case <-tick.C:
		}
	}
}

func fanSet(args []string, prog string) error {
	fs := newFlags(prog+" set", "PERCENT", "Pin the fans to a fixed speed.")
	which := fs.Int("fan", 0, "only this fan, 1 or 2 (default both)")
	if err := parse(fs, args, 1, 1); err != nil {
		return err
	}
	pct, err := parsePercent(fs.Arg(0))
	if err != nil {
		return err
	}
	if *which != 0 && !fan.ValidFan(*which) {
		return usagef("--fan must be 1 or 2")
	}

	var res proto.FanSetResult
	req := proto.Request{Op: proto.OpFanSet, Percent: proto.Pct(pct), Fan: *which, Verify: true}
	if err := call(req, &res); err != nil {
		return err
	}
	unconfirmed := map[int]bool{}
	for _, f := range res.Unconfirmed {
		unconfirmed[f] = true
	}
	for _, f := range []int{1, 2} {
		if *which != 0 && f != *which {
			continue
		}
		warn := ""
		if unconfirmed[f] {
			warn = "   [WARNING: the EC did not confirm the new duty]"
		}
		fmt.Printf("fan%d -> %g%% (duty %d)%s\n", f, pct, res.Raw, warn)
	}
	if pct < 20 {
		fmt.Println("note: below ~20% the fans may stall; watch your temperatures.")
	}
	return nil
}

func fanAuto(args []string, prog string) error {
	fs := newFlags(prog+" auto", "", "Hand the fans back to the firmware's built-in curve.")
	// Accepted for compatibility with the old service unit; the daemon now
	// handles its own shutdown, so there is nothing transient to record.
	fs.Bool("transient", false, "ignored")
	if err := parse(fs, args, 0, 0); err != nil {
		return err
	}
	if err := call(proto.Request{Op: proto.OpFanAuto}, nil); err != nil {
		return err
	}
	fmt.Println("fans handed back to the firmware's built-in curve")
	return nil
}

func fanCurve(args []string, prog string) error {
	fs := newFlags(prog+" curve", "[PROFILE]",
		"Follow a temperature curve. Without PROFILE, uses the one named in /etc/alg.conf.")
	profile := fs.String("profile", "", "same as giving PROFILE")
	if err := parse(fs, args, 0, 1); err != nil {
		return err
	}
	if fs.NArg() == 1 {
		*profile = fs.Arg(0)
	}
	var res struct {
		Profile string `json:"profile"`
	}
	if err := call(proto.Request{Op: proto.OpFanCurve, Profile: *profile}, &res); err != nil {
		return err
	}
	fmt.Printf("fans now follow the '%s' curve\n", res.Profile)
	return nil
}

func fanProfiles() error {
	var p proto.Profiles
	if err := call(proto.Request{Op: proto.OpProfiles}, &p); err != nil {
		return err
	}
	section := func(title string, builtin bool) {
		first := true
		for _, info := range p.Profiles {
			if info.Builtin != builtin {
				continue
			}
			if first {
				fmt.Println(title)
				first = false
			}
			fmt.Printf("  %-12s %s\n", info.Name, info.Points)
		}
	}
	section("built-in profiles:", true)
	section("from config:", false)
	fmt.Printf("\ncurrently selected: %s\n", p.Current)
	return nil
}
