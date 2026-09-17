# How the EC protocol was worked out

This directory is the evidence behind the "How it works" section of the main
README. The conclusions live there and in `internal/ec`, `internal/fan` and
`internal/kbd`; this file records how each one was reached, so it does not
have to be rediscovered.

The experiments below were run with six small throwaway scripts in August
2026. They had done their job and were deleted; everything they established
is written down here.

## What is in this directory

| Path | What |
|---|---|
| `acpi/*.dat` | the DSDT and SSDTs, copied from `/sys/firmware/acpi/tables` |
| `acpi/dsdt.dsl` | the DSDT disassembled with `iasl -d`; this is where the mailbox, the `SCMD` cases and the field maps below come from |
| `ec_baseline.bin` | the 256-byte EC register space, captured before any command had ever been sent |
| `00-collect.sh` | re-collects all of the above; read-only, run with `sudo` |

## Two views of the EC

The DSDT declares two `SystemMemory` regions inside the EC device:

| Region | Address | What it turned out to be |
|---|---|---|
| `RAM` | `0xFE0B0100`, 0x100 bytes | a mirror of the classic EC `0x00`–`0xFF` space: the same bytes `/sys/kernel/debug/ec/ec0/io` exposes. **Live.** |
| `RAM3` | `0xFE0B0300`, 0x100 bytes | an OEM block holding keyboard colours and fan-curve tables. **Not live** — a boot-time configuration buffer. |

`RAM3` looked like exactly what was needed, and cost the most time. Its field
map, straight from the DSDT (offsets are within `RAM3`):

| Field | Offset | Size | |
|---|---|---|---|
| `KLCR` `KLCG` `KLCB` | `0x80`–`0x82` | 1 each | left zone red, green, blue |
| `KMCR` `KMCG` `KMCB` | `0x83`–`0x85` | 1 each | middle zone |
| `KRCR` `KRCG` `KRCB` | `0x86`–`0x88` | 1 each | right zone |
| `KBBH` | `0x89` | 1 | backlight brightness |
| `FANQ` | `0x8A` | 1 | |
| `KBTP` | `0x8B` | 1 | |
| `F1T1`–`F1T4` | `0x8C`–`0x8F` | 1 each | fan 1 table |
| `F1D1`–`F1D4` | `0x90`–`0x93` | 1 each | |
| `F1R1`–`F1R3` | `0x94` `0x96` `0x98` | 2 each | |
| `F2T1`–`F2T4` | `0x9A`–`0x9D` | 1 each | fan 2 table |
| `F2D1`–`F2D4` | `0x9E`–`0xA1` | 1 each | |
| `F2R1`–`F2R3` | `0xA2` `0xA4` `0xA6` | 2 each | |
| `F3T1`–`F3T4` | `0xA8`–`0xAB` | 1 each | fan 3 table |
| `F3D1`–`F3D4` | `0xAC`–`0xAF` | 1 each | |
| `F3R1`–`F3R3` | `0xB0` `0xB2` `0xB4` | 2 each | |

By their names the `T`/`D` fields are temperature thresholds and duties. They
and the `R` fields were never decoded further, because the whole window
turned out not to drive anything.

## The experiments, in order

**1. Dump both windows** (`/dev/mem` mapped at `0xFE0B0000`, read-only).
`RAM` is the mirror of the classic EC space that the DSDT says it is. `RAM3`
held BIOS defaults — blue, brightness 0 — while the keyboard was visibly lit
white. First sign it was not live state.

**2. Is `RAM3` live?** Dumped the whole 4 KB page twice, three seconds apart,
to see which bytes the EC updates on its own, then wrote pure green to all
nine zone-colour bytes and read them back. Nothing happened on the keyboard.
Only the `0xFE0B0100` window is live.

**3. Brightness through `RAM3`.** Wrote a staircase (1, 2, 3, 4, 10, 50, 100,
200, 255) to `KBBH`, reading each back after 1.5 s. No effect on the
backlight. The `/dev/mem` route was abandoned here; everything after this goes
through the EC command mailbox, via `ec_sys`.

**4. The fan protocol.** `FCMD=0xC1, FDAT=<fan>, FBUF=<duty>` on both fans:
255 for eight seconds, then 77 for eight seconds, sampling duty (`0xCE`,
`0xCF`) and the tachometers (`0xD0`, `0xD2`, big-endian period) every two
seconds, then `FCMD=0xC1, FDAT=0xFF, FBUF=<fan>` to hand back to the firmware
— in a `finally`, so a crash could not leave the fans pinned. Duty and rpm
tracked the commands. This is the "Measured behaviour" table in the README:
6286 / 5859 rpm at 100%, 2539 / 2395 rpm at 30%, and it confirmed the standard
Clevo constant, `rpm = 2156220 / period`.

**5. Backlight through the mailbox.** `FCMD=0xC4` with `FDAT=0x0D` (on) and
`0x0E` (off), blinked three times; then `FDAT=0x02, FBUF=<n>` swept 255 → 180
→ 100 → 30. The on/off commands do nothing on this model. Brightness works.

**6. Off, and the colour byte order.** Brightness 0 went fully dark, so off is
brightness 0. Then `FCMD=0xCA, FDAT=<3|4|5>` with `FBUF=0xFF, FBF1=0x00,
FBF2=0x00` to all three zones, to see which colour came up: **blue**. The
script's own legend read that as "the order is B,G,R", which was wrong — the
order is **B,R,G** (`FBUF`=blue, `FBF1`=red, `FBF2`=green). One primary
cannot tell those two orders apart, and a red/green swap went unnoticed at
first as a result. That is why `alg kbd test` cycles yellow, cyan and magenta
as well as the primaries: each secondary mixes two channels, so any swap
shows.

The probes waited 10–20 ms after writing `FCMD`. The daemon uses 5 ms and
re-asserts a running curve every 15 s.

## Doing any of this again

Looking needs no special tooling:

```bash
sudo xxd /sys/kernel/debug/ec/ec0/io
```

```bash
sudo watch -d -n1 xxd /sys/kernel/debug/ec/ec0/io
```

The second highlights every byte as it changes, which is how you find a live
register. To compare against the pristine capture, from the repository root:

```bash
sudo xxd /sys/kernel/debug/ec/ec0/io | diff - <(xxd recon/ec_baseline.bin)
```

Anything that *writes* to the EC now goes through `alg` (`alg fan set`,
`alg kbd brightness`, `alg kbd test`, ...), where the daemon serialises it. If
a raw mailbox experiment is ever needed again, stop the daemon first with
`sudo systemctl stop alg`: two writers on the mailbox can interleave their
payload bytes, and the EC would then run one command with the other's
arguments.
