#!/usr/bin/env bash
# Build tiad-fake, the agent's core with a scripted host instead of TIA, for
# the contract suite (siemens/tiad/tiadtest) under Mono:
#
#   tools/tiad/build-fake.sh
#   TIAD_FAKE_BIN=$PWD/tools/tiad/out/tiad-fake.exe go test ./siemens/tiad/...
#
# Needs mcs, mono and the .NET 4.8 reference assemblies that -sdk:4.8 compiles
# against (Debian/Ubuntu: mono-devel; mono-mcs + mono-runtime lack them).
#
# The sources are C# 5, because on the TIA machine install-tiad.ps1 compiles
# them with the stock .NET Framework csc, which knows nothing newer. Here
# -langversion:5 makes anything newer fail first.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
out="$here/out"
mkdir -p "$out"

# The stock csc on the TIA machine reads a source without a BOM in the
# system code page, so any non-ASCII character must be a \u escape.
if LC_ALL=C grep -nP '[^\x00-\x7F]' "$here"/core/*.cs "$here"/fake/*.cs; then
  echo "build-fake.sh: non-ASCII characters in C# sources (use \\uXXXX escapes)" >&2
  exit 1
fi

mcs="${MCS:-mcs}"
common=(-langversion:5 -sdk:4.8 -warnaserror+ -nologo -debug -r:System.Web.Extensions.dll)

"$mcs" "${common[@]}" -target:library -out:"$out/tiad-core.dll" "$here"/core/*.cs
"$mcs" "${common[@]}" -target:exe -out:"$out/tiad-fake.exe" -r:"$out/tiad-core.dll" "$here"/fake/*.cs

echo "built $out/tiad-fake.exe"
