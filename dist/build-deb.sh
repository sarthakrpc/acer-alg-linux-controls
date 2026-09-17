#!/bin/bash
# Assemble build/alg-control_<version>_amd64.deb from the release binaries.
# Needs no root: dpkg-deb records root ownership itself.
set -euo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PKG=alg-control

for bin in alg alg-gui; do
    if [ ! -x "$SRC/build/$bin" ]; then
        echo "build/$bin is missing - run 'make' first" >&2
        exit 1
    fi
done

# The package version is whatever the binary says it is.
VERSION="$("$SRC/build/alg" --version | awk '{print $2}')"
STAGE="$SRC/build/deb-stage"
OUT="$SRC/build/${PKG}_${VERSION}_amd64.deb"

rm -rf "$STAGE"
umask 022
put() { # put <mode> <source> <path inside the package>
    install -D -m "$1" "$2" "$STAGE/$3"
}

put 0755 "$SRC/build/alg"              usr/bin/alg
put 0755 "$SRC/build/alg-gui"          usr/bin/alg-gui
# Names from earlier versions, in case a shortcut or script still uses them.
for legacy in algfan algkbd algui; do
    ln -s alg "$STAGE/usr/bin/$legacy"
done

put 0644 "$SRC/dist/alg.service"       usr/lib/systemd/system/alg.service
put 0755 "$SRC/dist/system-sleep-alg"  usr/lib/systemd/system-sleep/alg
put 0644 "$SRC/dist/modules-load.conf" usr/lib/modules-load.d/alg-control.conf
put 0644 "$SRC/dist/modprobe.conf"     usr/lib/modprobe.d/alg-control.conf

put 0644 "$SRC/dist/alg.desktop"       usr/share/applications/alg.desktop
put 0644 "$SRC/assets/alg.svg"         usr/share/icons/hicolor/scalable/apps/alg.svg
# Starts the tray icon at every login, for every user.
put 0644 "$SRC/dist/alg-tray.desktop"  etc/xdg/autostart/alg-tray.desktop

# The default config; the postinst copies it to /etc/alg.conf if there is none.
put 0644 "$SRC/alg.conf"               usr/share/$PKG/alg.conf
put 0644 "$SRC/README.md"              usr/share/doc/$PKG/README.md
# Kept in the same relative place, so the README's links and pictures work
# from the installed copy too.
for doc in "$SRC"/docs/*; do
    put 0644 "$doc"                    "usr/share/doc/$PKG/docs/$(basename "$doc")"
done

mkdir -p "$STAGE/DEBIAN"
for script in preinst postinst prerm postrm; do
    install -m 0755 "$SRC/dist/deb/$script" "$STAGE/DEBIAN/$script"
done
install -m 0644 "$SRC/dist/deb/conffiles" "$STAGE/DEBIAN/conffiles"

SIZE="$(du -sk --exclude=DEBIAN "$STAGE" | cut -f1)"
sed -e "s/@VERSION@/$VERSION/" -e "s/@INSTALLED_SIZE@/$SIZE/" \
    "$SRC/dist/deb/control.in" > "$STAGE/DEBIAN/control"

(cd "$STAGE" && find . -path ./DEBIAN -prune -o -type f -printf '%P\0' | sort -z | xargs -0 md5sum) \
    > "$STAGE/DEBIAN/md5sums"

rm -f "$OUT"
dpkg-deb --root-owner-group -Zxz --build "$STAGE" "$OUT" >/dev/null
rm -rf "$STAGE"

echo "built $OUT"
dpkg-deb --info "$OUT" | sed -n '/^ Package:/,$p'
