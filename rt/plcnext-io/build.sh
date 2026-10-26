#!/usr/bin/env bash
# Builds nautilus_io for the PLCnext AXC F 2152 (ARMv7 hard float, glibc
# 2.35) with zig, and with -p packages it as the release tarball:
#
#   rt/plcnext-io/build.sh [-p] [out dir]     (default out dir: dist/plcnext-io)
#
# The same script builds the bench's binary, CI's compile check and the
# release asset, so the three cannot drift. ZIG names the zig binary
# (default: zig on PATH); VERSION is stamped into the binary and its log's
# first line (default: dev).
set -euo pipefail

pack=0
if [[ ${1:-} == -p ]]; then pack=1; shift; fi
here=$(cd "$(dirname "$0")" && pwd)
out=${1:-dist/plcnext-io}
zig=${ZIG:-zig}
version=${VERSION:-dev}

mkdir -p "$out"
"$zig" cc -target arm-linux-gnueabihf.2.35 -mcpu=cortex_a9 -O2 -std=c11 \
	-Wall -Wextra -Werror -DNAUTILUS_IO_VERSION="\"$version\"" \
	-o "$out/nautilus_io" "$here/nautilus_io.c" -ldl -lpthread
echo "built $out/nautilus_io ($version)"

if ((pack)); then
	# Everything the README's deploy steps copy, under one directory.
	name=nautilus-plcnext-io_${version}_linux_armv7
	stage=$(mktemp -d)
	mkdir "$stage/$name"
	cp "$out/nautilus_io" "$here/NautilusIo.acf.settings" "$here/NautilusIo.acf.config" \
		"$here/README.md" "$stage/$name/"
	tar -C "$stage" -czf "$out/$name.tar.gz" "$name"
	(cd "$out" && sha256sum "$name.tar.gz" >"$name.tar.gz.sha256")
	rm -rf "$stage"
	echo "packed $out/$name.tar.gz"
fi
