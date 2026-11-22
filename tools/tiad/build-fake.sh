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
# A plain bracket of the bytes 0x80-0xff, in the C locale: no grep -P.
set +e
LC_ALL=C grep -n "$(printf '[\200-\377]')" "$here"/core/*.cs "$here"/fake/*.cs "$here"/host/*.cs "$here"/openness/*.cs "$here"/test/*.cs
rc=$?
set -e
if [ "$rc" -eq 0 ]; then
  echo "build-fake.sh: non-ASCII characters in C# sources (use \\uXXXX escapes)" >&2
  exit 1
elif [ "$rc" -gt 1 ]; then
  echo "build-fake.sh: the non-ASCII check failed (grep exit $rc)" >&2
  exit 1
fi

mcs="${MCS:-mcs}"
common=(-langversion:5 -sdk:4.8 -warnaserror+ -nologo -debug -r:System.Web.Extensions.dll -r:System.Xml.dll)

"$mcs" "${common[@]}" -target:library -out:"$out/tiad-core.dll" "$here"/core/*.cs
# tiad-fake's own core has the test seams (core/TestSeams.cs); the release
# core above, and every core install-tiad.ps1 builds, never does.
"$mcs" "${common[@]}" -define:TIAD_TEST_SEAMS -target:library -out:"$out/tiad-core-seams.dll" "$here"/core/*.cs
"$mcs" "${common[@]}" -target:exe -out:"$out/tiad-fake.exe" -r:"$out/tiad-core-seams.dll" "$here"/fake/*.cs
# The release core must not hold the seams.
if LC_ALL=C grep -aq TestSeams "$out/tiad-core.dll"; then
  echo "build-fake.sh: the release core holds the test seams (TIAD_TEST_SEAMS leaked)" >&2
  exit 1
fi
if ! LC_ALL=C grep -aq TestSeams "$out/tiad-core-seams.dll"; then
  echo "build-fake.sh: tiad-fake's core lacks the test seams" >&2
  exit 1
fi
# tiad.exe, the real agent, only to hold its sources to C# 5 and
# -warnaserror here too: it runs on Windows only (install-tiad.ps1 builds
# it there with the stock csc).
"$mcs" "${common[@]}" -target:exe -out:"$out/tiad.exe" -r:"$out/tiad-core.dll" "$here"/host/*.cs

# tiad-test.exe: the unit tests of what tiad.Openness decides
# (core/TiaRules.cs). tiad.Openness itself references Siemens.Engineering,
# which only the TIA machine has; install-tiad.ps1 builds it there.
"$mcs" "${common[@]}" -target:exe -out:"$out/tiad-test.exe" -r:"$out/tiad-core.dll" "$here"/test/*.cs
"${MONO:-mono}" "$out/tiad-test.exe"

echo "built $out/tiad-fake.exe (and $out/tiad.exe, Windows only)"
