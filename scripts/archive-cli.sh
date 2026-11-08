#!/bin/sh
# Packs a built CLI into the archive published beside it in a release, for
# networks that block bare executables but let archives through. The archive
# holds one file, marasi (marasi.exe on Windows), and is named after the
# build: marasi-darwin-arm64.zip, marasi-windows-amd64.zip,
# marasi-linux-amd64.tar.gz. It prints the archive's path.
#
#   scripts/archive-cli.sh dist/marasi-darwin-arm64
set -eu

if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
	echo "usage: $0 dist/marasi-OS-ARCH[.exe]" >&2
	exit 2
fi

binary=$1
dir=$(cd "$(dirname "$binary")" && pwd)
build=$(basename "$binary" .exe)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

case "$build" in
marasi-windows-*)
	cp "$binary" "$stage/marasi.exe"
	archive="$dir/$build.zip"
	rm -f "$archive"
	(cd "$stage" && zip -q -X "$archive" marasi.exe)
	;;
marasi-darwin-*)
	cp -p "$binary" "$stage/marasi"
	archive="$dir/$build.zip"
	rm -f "$archive"
	# notarytool takes a ditto zip. The code signature lives inside the
	# binary, so extended attributes are left out: they would add a
	# ._marasi entry beside it.
	ditto -c -k --norsrc --noextattr --noqtn "$stage/marasi" "$archive"
	;;
marasi-linux-*)
	cp -p "$binary" "$stage/marasi"
	chmod 755 "$stage/marasi"
	archive="$dir/$build.tar.gz"
	tar -C "$stage" -czf "$archive" marasi
	;;
*)
	echo "$0: $binary is not named marasi-OS-ARCH" >&2
	exit 2
	;;
esac

echo "$archive"
