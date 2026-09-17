# alg — fan and keyboard backlight control for the Acer ALG AL15G-53

Working fan speed and keyboard backlight control for the Acer ALG
(`AL15G-53`, i5-13420H / RTX 3050) under Linux Mint / Ubuntu, with a desktop
control panel and a tray icon. Written in Go.

## Why nothing else works

The obvious answer is `acer-wmi`, and it will never load on this machine.
`acer-wmi` binds to the WMI GUIDs `676AA15E-…`, `6AF4F258-…` and
`67C3371D-…`; this firmware exposes none of them. What it *does* expose is:

```
ABBC0F6D-8EA1-11D1-00A0-C90629100000   method  (WMBB)
Device (DCHU) { _HID = "CLV0001" }
```

Those are **Clevo** identifiers. Under the Acer badge this is a
Clevo/Tongfang ODM board, so Acer-specific tooling (NitroSense clones,
`acer-wmi-battery`, `linuwu-sense`) targets the wrong interface entirely.
That is also why there is no `platform_profile`, no `kbd_backlight` LED and
no fan in `sensors`.

## Install

Double-click **`alg-control_2.0_amd64.deb`** and press *Install Package*. That
is the whole procedure: Mint's package installer asks for your password once
and does the rest. From a terminal the equivalent is:

```bash
sudo apt install ./build/alg-control_2.0_amd64.deb
```

The package is self-contained. It carries the compiled programs, so once you
have the `.deb` the source tree is not needed to install, reinstall or move
to another machine — keep a copy of it somewhere safe.

Installing it:

- starts the `alg` service and enables it at boot;
- adds **ALG Control** to the applications menu;
- puts the **tray icon** in the panel straight away, and again at every
  login (`/etc/xdg/autostart/alg-tray.desktop`);
- **removes the earlier Python version** completely — its program under
  `/usr/local`, its three systemd units, its sleep hook and module settings —
  while keeping your `/etc/alg.conf`, your saved backlight, and whichever fan
  mode you had chosen;
- asks no questions.

To remove it, use the Software Manager, or:

```bash
sudo apt remove alg-control
```

Removing hands the fans back to the firmware first. `apt purge` also deletes
`/etc/alg.conf` and the saved settings in `/var/lib/alg`.

Everything is one command afterwards: **`alg`**. Run it with no arguments to
see what it does.

### Building the package yourself

```bash
make deb
```

This needs Go 1.24+ and, for the control panel only, a C compiler plus the
X11/OpenGL headers (`sudo apt install gcc libgl1-mesa-dev xorg-dev`). The
first build compiles GLFW and takes a couple of minutes; after that it is
seconds. The result is `build/alg-control_<version>_amd64.deb`. Building needs
no root.

## How it fits together

```
 alg fan ... / alg kbd ...  ─┐
 alg tray   (tray icon)     ─┼─►  /run/alg/alg.sock  ─►  alg daemon (root)  ─►  EC
 alg-gui    (control panel) ─┘         JSON lines          alg.service
```

- **`alg daemon`** is the only thing that touches hardware. It owns the EC,
  runs the fan curve, remembers what you chose, and puts it back at boot and
  after sleep. It is a single static binary with no dependencies.
- **Everything else is an unprivileged client** of its Unix socket: the
  command line, the tray icon and the control panel all speak the same
  one-JSON-object-per-line protocol.

### No password, no sudo

The previous version launched a root helper through `pkexec`, so you typed
your password every time the GUI started, and the CLI re-ran itself under
`sudo`. Now nothing does. The daemon checks who is on the other end of the
socket (`SO_PEERCRED`) and allows changes from root and from members of the
groups in `allow_groups` (default `sudo`, `wheel`, `alg`). Reading status is
open to any local user.

Group membership is looked up in the user database when a program connects,
not taken from the caller's process, so adding someone to a group works
immediately without logging out. For an account that is in neither `sudo` nor
`wheel`, the package creates a dedicated `alg` group:
`sudo adduser <name> alg`.

### What it costs to run

Measured on this machine:

| Process | When it runs | Memory | CPU |
|---|---|---|---|
| `alg daemon` | always | ~10 MB | ~0.1% with a curve running (one sensor read every 2 s); asleep otherwise |
| `alg tray` | while you want the icon | ~12 MB | under 0.1% — one status poll every 5 s |
| `alg-gui` | only while the window is open | ~180 MB | a few % (the toolkit redraws at 60 Hz) |
| *Python daemon, for comparison* | | *13 MB* | |

The `alg-gui` row is why the tray is **not** part of the control panel. GUI
toolkits keep a render loop ticking even with nothing on screen; leaving one
in the tray all day would cost more than everything else here combined. So
the tray is plain D-Bus with no toolkit behind it, and the window is a
separate program that exists only while it is open. Closing the window ends
that process; the icon stays.

While the fan curve is settled the daemon sleeps straight through to the next
sensor sample instead of waking every `step_interval`, so steady state is one
wake-up per `poll_interval`.

## The GUI

Launch **ALG Control** from your applications menu, or:

```bash
alg gui
```

- **Fans** — live RPM, duty and temperatures; mode selector (Automatic /
  Manual / Curve), manual speed slider, profile picker.
- **Keyboard** — brightness slider with presets, colour picker, preset
  swatches, per-zone selection, and the colour test.
- **Tray icon** — the hottest temperature drawn as the icon itself (white,
  amber from 70 °C, red from 85 °C), with quick fan presets, *Show control
  panel* and *Quit* in its menu.

`alg gui --page keyboard` opens straight to the keyboard controls. Launching
it a second time raises the window that is already open.

The tray icon starts by itself when you log in. It is also started whenever
you open the control panel (`alg gui --no-tray` skips that), so choosing
*Quit* from its menu only lasts until then. To stop it appearing at login,
switch off *ALG Control (tray icon)* in Mint's **Startup Applications**.

If it starts before the panel is ready, which is normal at login, it simply
waits and appears the moment the panel's tray does; it likewise comes back by
itself if Cinnamon is restarted.

To pin the control panel itself next to the menu button, right-click **ALG
Control** in the applications menu and choose *Add to panel*.

The old tray showed the temperature as an AppIndicator text label beside the
icon. That label is an Ayatana extension the portable StatusNotifierItem
protocol does not have, so the number is now drawn into the icon, on a dark
plate that stays legible on light and dark panels alike.

The window talks to the daemon on a worker goroutine, so a slow EC read can't
freeze it, and slider changes are debounced (200 ms) because a drag emits
continuously and every emission would otherwise be an EC write. It also
follows changes made elsewhere — set a speed from the CLI and the slider
moves — but never yanks the handle out from under a drag in progress.

### Settings stay put

| Action | Result |
|---|---|
| Close the window | the window process exits; the tray icon stays |
| **Quit** from the tray | closes tray and window — your settings remain applied |
| GUI or tray crashes or is killed | nothing changes; they only ever *ask* the daemon |
| Sleep, lid close, reboot | reapplied automatically |

Settings change when you change them, and not otherwise. The CPU's own
thermal protection does not depend on any of this.

## Command line

```bash
alg status                 # fans, temperatures and backlight at a glance

alg fan status             # duty, rpm, temperatures
alg fan monitor            # live view
alg fan set 70             # pin both fans to 70%
alg fan set 80 --fan 1     # just the CPU fan
alg fan curve              # follow the default temperature curve
alg fan curve quiet        # ...or a named profile
alg fan auto               # hand control back to the firmware
alg fan profiles           # list curve profiles

alg kbd brightness 70      # 0-100
alg kbd off / alg kbd on   # "on" returns to the level you had before "off"
alg kbd color white        # name, or #rrggbb
alg kbd color '#ff8800' --zone left
alg kbd test               # cycle colours to verify the mapping

alg reload                 # re-read /etc/alg.conf
```

None of these need `sudo`. `algfan`, `algkbd` and `algui` still work —
they're symlinks to `alg` and dispatch on `argv[0]`.

One thing changed meaning: `alg fan curve` used to *be* the curve daemon,
running in the foreground. It now tells the daemon to follow a curve, and
returns.

### Automatic fan curve

```bash
alg fan curve balanced
```

The daemon samples the hottest available sensor every two seconds and
interpolates a duty from the curve.

| Profile | Curve |
|---|---|
| `silent` | 50 °C:15% → 65:25 → 75:40 → 85:70 → 92:100 |
| `balanced` *(default)* | 45 °C:25% → 60:40 → 72:55 → 82:75 → 90:100 |
| `performance` | 40 °C:35% → 55:50 → 65:70 → 75:90 → 85:100 |
| `max` | flat 100% |

Define your own in `/etc/alg.conf`:

```ini
[curve.mine]
points = 45:20, 60:35, 70:50, 78:70, 85:90, 90:100
```

Then `alg fan curve mine`. After editing a curve that is already running,
`alg reload` applies it. `profile =` under `[general]` is only the default
for a bare `alg fan curve`; the profile you actually picked is remembered
separately, so the daemon never rewrites your config file.

### Ramping

The curve gives a *target*; the daemon walks the duty toward it at a bounded
rate rather than jumping. Without this the fans slam to 100% the instant a
core spikes and drop back just as abruptly — noisy, and hard on the bearings.

| Setting | Default | Meaning |
|---|---|---|
| `ramp_up` | 10 %/s | 0→100% takes ~10 s |
| `ramp_down` | 3 %/s | 100→0% takes ~33 s |
| `step_interval` | 0.5 s | how often the duty is nudged |
| `temp_smoothing` | 6 s | EMA time constant on the temperature |

Rising is deliberately quicker than falling: a real thermal event should
still be answered promptly, while easing off can take its time. Set either
rate to `0` to jump straight to the target.

`temp_smoothing` matters as much as the ramp itself. Core temperatures are
spiky, and on raw readings a momentary blip drags the target up and the fans
then sawtooth their way back down. Smoothing feeds the *curve* a settled
value — while the emergency check below deliberately reads the raw sensor,
so nothing sits in front of the safety response.

Measured on this machine, same 12-thread load before and after:

| | Without ramping | With ramping |
|---|---|---|
| Rising | instant 100% | 74→89→94→98→99% over ~20 s |
| Falling | 88, 84, **92**, 82, 87, 84, 80, **90** | 99→89→77→72→68→64→62→61 |
| Peak CPU | 96 °C | 86 °C |

## Sleep, resume and reboot

The EC drops **both** the keyboard backlight and any manual fan setting
across suspend — including a lid close, since that suspends too — and
forgets everything at power-off.

- **Boot:** the daemon restores your fan mode and backlight as it starts.
- **Wake:** a systemd sleep hook at `/usr/lib/systemd/system-sleep/alg` runs
  `alg resume`, which asks the daemon to reapply both. A running curve is
  restarted so it re-reads the duty the EC fell back to and ramps from
  there, rather than assuming its last write survived.

A sleep hook is used rather than a unit ordered `After=suspend.target`
because only the hook is guaranteed to run *after* waking. The EC can be
briefly unresponsive at that moment, so the restore retries before giving up,
and does the fans before the backlight: if the EC is only half awake, cooling
matters more than the keyboard lighting up.

### How the mode is remembered

`/var/lib/alg/fan-mode` holds what **you chose** — `auto (EC default)`,
`manual 70%`, `manual fan1=50% fan2=70%` or `curve:quiet` — and is written
only by explicit actions. Backlight settings live next to it in `kbd.json`.
Both are written atomically (temp file + rename), so a power cut mid-write
cannot leave a truncated file.

The Python version needed two copies of this (what the fans are doing *now*
versus what you *chose*) plus a systemd unit that was enabled or disabled to
encode "follow the curve at boot", and a real bug came from the curve daemon
stamping its own state over a saved manual speed. With one long-lived daemon
the "now" lives in memory, transient states such as the emergency override
are never written to disk, and there is a single source of truth. The file
format is unchanged, so the saved mode carries straight over.

## How it works

The DSDT's `ECMD` method drives a six-register mailbox in the EC's standard
address space:

| Register | EC offset |
|---|---|
| `FCMD` | `0xF8` |
| `FDAT` | `0xF9` |
| `FBUF` | `0xFA` |
| `FBF1`–`FBF3` | `0xFB`–`0xFD` |

Payload registers are written first; writing `FCMD` last is what triggers
the EC. Decoding the firmware's own `SCMD` cases gives the protocol:

```
FCMD=0xC1, FDAT=<fan 1..4>, FBUF=<duty 0..255>   manual duty      (SCMD 0x68)
FCMD=0xC1, FDAT=0xFF,       FBUF=<fan 1..4>      back to EC auto  (SCMD 0x69)
FCMD=0xC4, FDAT=0x02, FBUF=<0..255>              backlight brightness
FCMD=0xCA, FDAT=<3|4|5>, FBUF/FBF1/FBF2          per-zone colour
```

Live status is mirrored into the classic EC register space:

| What | EC offset | Encoding |
|---|---|---|
| fan 1 / 2 duty | `0xCE` / `0xCF` | 0–255 |
| fan 1 / 2 tachometer | `0xD0` / `0xD2` | **big-endian** period, `rpm = 2156220 / value` |
| board temperature | `0x07` | °C |

The tachometer is a *period*, not a speed — a smaller number means a faster
fan. That constant (`2156220`) is the standard Clevo one and produces sane
values here: 6286 and 5859 rpm at 100% duty.

All access goes through the kernel's own EC driver via
`/sys/kernel/debug/ec/ec0/io` (`ec_sys` with `write_support=1`), so reads and
writes are serialised by the kernel instead of banging ports `0x62/0x66`.

### Measured behaviour

| Command | Duty | Fan 1 | Fan 2 | CPU |
|---|---|---|---|---|
| firmware curve (stock) | 65% | 4667 rpm | 4382 rpm | 74 °C |
| `alg fan set 100` | 255 | 6286 rpm | 5859 rpm | **64 °C** |
| `alg fan set 30` | 77 | 2539 rpm | 2395 rpm | 66 °C |

The stock curve is conservative — pushing the fans to 100% is worth about
10 °C on this machine.

### Things that were found the hard way

- **The RGB block at `0xFE0B0380` is a decoy.** It looks like live state
  (`KLCR/KLCG/KLCB`, brightness `KBBH`) but is a boot-time config buffer: it
  reads back BIOS defaults (blue, brightness 0) even while the keyboard is
  lit white, and writing to it does nothing. Only the `0xFE0B0100` window is
  live.
- **Colour bytes are ordered blue, red, green** — `FBUF`=blue, `FBF1`=red,
  `FBF2`=green. The DSDT packs its 32-bit word as though the bytes were
  R,G,B, so the panel's wiring does not follow the ACPI convention. Don't
  "correct" this to RGB without retesting.
- **`alg kbd test` cycles the secondaries** (yellow, cyan, magenta) as well
  as the primaries, because primaries alone cannot catch a two-channel swap
  — each still looks right in isolation. Exactly that blind spot hid a
  red/green swap initially.
- **The `0x0D`/`0x0E` backlight on/off commands do nothing here.** Off is
  brightness 0.

## Safety

Handing the fans to software is the risky part, so this is built to fail
back to the firmware rather than fail quiet:

- **A running curve is always handed back to the firmware when the daemon
  exits** — normal stop, `SIGTERM`, `SIGINT`. The unit also runs
  `alg daemon --cleanup` as `ExecStopPost` in case it was killed outright,
  and restarts it on failure. (A *manual* speed is deliberately left alone:
  that is a setting, not a supervised loop. Neither path touches your saved
  choice, so the curve resumes when the daemon comes back.)
- **Emergency override.** If any sensor hits `emergency_temp` (default
  95 °C) the fans go straight to 100% with no ramp, until things drop 8 °C
  below it. TjMax on this CPU is 100 °C, so 95 leaves margin without firing
  during ordinary heavy load — all-core work sits in the low 90s here quite
  normally. This previously handed control back to the firmware instead,
  which was exactly wrong: the firmware curve is *less* aggressive than
  these profiles up there, so the safety net made the machine hotter. It
  cost about 10 °C of peak temperature under load. The check reads the raw
  sensor; smoothing never sits in front of it.
- **Fails hot, not silent.** If every temperature source disappears, the
  daemon assumes 100 °C rather than assuming everything is fine.
- **A request that forgets its percentage is rejected**, not read as 0%. A
  stalled fan is not a safe default for a malformed message.
- **`min_duty`** (default 10%) stops the curve commanding a duty so low the
  fans stall.
- **Write verification.** `alg fan set` reads the duty back and re-sends it
  once if the EC never reflected it, so a write colliding with firmware EC
  traffic doesn't silently go missing. The curve re-asserts every 15 s, and
  a failed EC write is retried on the next pass instead of killing the loop.
- **No fighting.** There is one daemon and it serialises mode changes, so a
  manual speed and the curve cannot overwrite each other. Mailbox commands
  are issued under a lock, so two clients can never interleave payloads.
- **An unknown profile is refused** without disturbing the curve that is
  running, and a saved profile that has since been deleted from the config
  lands on the firmware curve at boot.

The CPU keeps its own hardware protection (PROCHOT and thermal shutdown)
regardless — software cannot disable it.

## Known limits

- **Backlight state cannot be read back.** The EC exposes no live status, so
  `alg kbd status` and the GUI report what alg last set. Changes made from
  Windows or with the Fn key won't show up.
- **The Fn backlight key still does nothing in Linux.** It arrives as a WMI
  event (`ABBC0F6B-…`, notify `0xD0`) with no driver listening. Bind `alg kbd`
  to a shortcut instead.
- **The effects** (`breathe`, `wave`, …) come from the DSDT but their visual
  mapping was never individually verified.

## Layout

Everything below except the last three rows belongs to the `alg-control`
package (`dpkg -L alg-control` lists it).

| Path | What |
|---|---|
| `/usr/bin/alg` | daemon, CLI and tray in one static binary (`algfan`/`algkbd`/`algui` are symlinks) |
| `/usr/bin/alg-gui` | the control panel window |
| `/usr/lib/systemd/system/alg.service` | the daemon |
| `/usr/lib/systemd/system-sleep/alg` | restore after wake |
| `/usr/lib/modules-load.d/alg-control.conf` | loads `ec_sys` at boot |
| `/usr/lib/modprobe.d/alg-control.conf` | ...with `write_support=1` |
| `/etc/xdg/autostart/alg-tray.desktop` | tray icon at login |
| `/usr/share/applications/alg.desktop` | menu entry |
| `/usr/share/icons/hicolor/scalable/apps/alg.svg` | icon |
| `/usr/share/alg-control/alg.conf` | the default config, copied to `/etc` if you have none |
| `/etc/alg.conf` | default profile, tuning, custom curves, `allow_groups` |
| `/run/alg/alg.sock` | control socket |
| `/var/lib/alg/fan-mode`, `kbd.json` | the fan mode **you chose** and the saved backlight |

`/etc/alg.conf` is created by the package but deliberately not tracked as a
dpkg conffile: you almost certainly have one already, and a conffile would
turn that into a question in the middle of a double-click install. It is
written once and never overwritten.

### Source

| Path | What |
|---|---|
| `cmd/alg` | CLI, `alg daemon`, `alg tray` — pure Go, built with `CGO_ENABLED=0` |
| `cmd/alg-gui` | the control panel (Fyne) |
| `internal/ec` | EC mailbox access; `internal/ec/sim` is a simulated controller |
| `internal/fan`, `internal/kbd` | fan and backlight protocol, curves, saved state |
| `internal/daemon` | mode transitions, the curve loop, the socket server |
| `internal/proto` | wire protocol and client |
| `internal/trayicon` | draws the temperature icon |
| `dist/` | what goes into the package: systemd unit, sleep hook, desktop entries, and the package scripts in `dist/deb/` |
| `recon/` | the ACPI dumps and EC baseline all of this was derived from, and `NOTES.md`: how each finding was reached |

### Developing without the hardware

```bash
make test
```

runs everything under the race detector, including an end-to-end test that
drives the real window logic through the real socket into a simulated EC.
Several tests pin behaviour to values recorded from the original Python
implementation, which this replaced.

`make sim` builds `build/alg-sim`, which can stand in a simulated EC so the
daemon, CLI, tray and window can all be run as an ordinary user, on any
machine, touching no hardware. Release builds do not contain the simulator.

```bash
cd build && export ALG_EC_IO=sim ALG_RUN_DIR=run ALG_STATE_DIR=state ALG_CONFIG=../alg.conf
./alg-sim daemon &
./alg-sim fan set 70 && ./alg-sim status
```

(The relative `ALG_RUN_DIR` is deliberate: a Unix socket path is limited to
about 100 characters.)

## Credit where due

The command numbering (`0x63/0x64/0x6E` fan info, `0x67` keyboard LEDs,
`0x68` fan duty, `0x69` fan auto) and the `2156220` tachometer constant are
the long-established Clevo interface, the same one
[tuxedo-drivers](https://gitlab.com/tuxedocomputers/development/packages/tuxedo-drivers)
implements. If you want a broader, distro-packaged option with a GUI, that
project plus TUXEDO Control Center is worth a look — this is a small,
dependency-free alternative aimed at this specific machine.
