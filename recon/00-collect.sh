#!/bin/bash
# Read-only recon: dump ACPI tables + EC register space.
# Nothing here writes to hardware.
set -u

# Collect next to this script, wherever the repository lives.
OUT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p "$OUT/acpi"

echo "== copying ACPI tables =="
cp /sys/firmware/acpi/tables/DSDT "$OUT/acpi/dsdt.dat" 2>/dev/null && echo "  DSDT ok"
for t in /sys/firmware/acpi/tables/SSDT*; do
    [ -e "$t" ] || continue
    cp "$t" "$OUT/acpi/$(basename "$t").dat" && echo "  $(basename "$t") ok"
done

echo
echo "== EC io ports =="
grep -Ei 'EC (data|cmd)' /proc/ioports

echo
echo "== loading ec_sys with write support =="
modprobe ec_sys write_support=1 2>&1 || echo "  modprobe failed"
mountpoint -q /sys/kernel/debug || mount -t debugfs none /sys/kernel/debug
ls -la /sys/kernel/debug/ec/ 2>&1

echo
echo "== EC register dump =="
if [ -r /sys/kernel/debug/ec/ec0/io ]; then
    dd if=/sys/kernel/debug/ec/ec0/io of="$OUT/ec_baseline.bin" bs=256 count=1 2>/dev/null
    echo "  wrote $OUT/ec_baseline.bin ($(stat -c%s "$OUT/ec_baseline.bin") bytes)"
    xxd "$OUT/ec_baseline.bin"
else
    echo "  /sys/kernel/debug/ec/ec0/io not readable"
fi

# Run through sudo, so hand the results back to whoever ran it.
chown -R "${SUDO_USER:-$USER}:" "$OUT"
echo
echo "== done =="
