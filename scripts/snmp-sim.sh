#!/usr/bin/env bash
#
# snmp-sim.sh — run the foreign SNMP agent the snmp driver's foreign test
# talks to: snmpsim (LeXtudio's maintained fork, on pysnmp) replaying the
# committed .snmprec files in snmp/testdata/sim/, which are our fixture walks
# plus a couple of counters that move with the wall clock.
#
#   scripts/snmp-sim.sh                 # 127.0.0.1:1161
#   scripts/snmp-sim.sh --port 1162
#
# Then, in another terminal (the script prints the exact line):
#
#   NAUTILUS_SNMP_SIM=127.0.0.1:1161 go test ./snmp/ -run TestForeign -v
#
# One agent serves both halves of the suite:
#   v2c — the community picks the data file: switch, ups, pdu
#   v3  — user "nautilus", authPriv SHA-256 / AES-128 (the FSOS syntax is
#         `snmp-server user X grp v3 priv aes128 auth sha256`), plus one user
#         per other privacy flavour the manifest accepts: "nautilus-aes256"
#         (SHA-1 + Blumenthal key extension, pysnmp AES256BLMT),
#         "nautilus-aes256c" (SHA-1 + Reeder/"Cisco", pysnmp AES256) and
#         "nautilus-legacy" (MD5/DES). SHA-1, because under SHA-256 the
#         localized key is already 32 bytes, no extension runs, and the two
#         AES-256s cannot be told apart.
#         The context name picks the data file. The keys are test keys, not
#         secrets; the test hands them to the driver through the environment.
#
# snmpsim goes in a venv OUTSIDE the repo (under $TMPDIR, or /tmp), and its
# index cache beside it — nothing to gitignore. The pins are deliberate:
#   snmpsim 1.2.2 imports pysmi at run time but declares it only as a dev
#   extra, so a clean `pip install snmpsim` dies with
#   "ModuleNotFoundError: No module named 'pysmi'"; and pysnmp needs
#   `cryptography` for AES/DES privacy but does not declare it either —
#   without it the sim answers v3 authPriv with nothing useful.
#
set -euo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
data="$repo/snmp/testdata/sim"
pins=('snmpsim==1.2.2' 'pysnmp==7.1.29' 'pysmi==2.0.0' 'cryptography==50.0.1')
root="${NAUTILUS_SNMP_SIM_VENV:-${TMPDIR:-/tmp}/nautilus-snmp-sim}"
venv="$root/.venv-snmp-sim"

if [ ! -x "$venv/bin/python" ]; then
  echo "creating venv $venv" >&2
  python3 -m venv "$venv"
fi
if ! "$venv/bin/python" -c 'import snmpsim, pysmi, cryptography' 2>/dev/null; then
  echo "installing ${pins[*]}" >&2
  "$venv/bin/pip" install --quiet "${pins[@]}"
fi

host=127.0.0.1 port=1161
while [ $# -gt 0 ]; do
  case "$1" in
    --host) host=$2; shift 2 ;;
    --port) port=$2; shift 2 ;;
    *) echo "unknown flag $1 (want --host, --port)" >&2; exit 2 ;;
  esac
done
echo "NAUTILUS_SNMP_SIM=$host:$port go test ./snmp/ -run TestForeign -v" >&2

mkdir -p "$root/cache"
exec "$venv/bin/snmpsim-command-responder" \
  --cache-dir="$root/cache" \
  --v3-engine-id=auto \
  --v3-user=nautilus \
  --v3-auth-key=nautilus-auth-key --v3-auth-proto=SHA256 \
  --v3-priv-key=nautilus-priv-key --v3-priv-proto=AES \
  --v3-user=nautilus-aes256 \
  --v3-auth-key=nautilus-auth-key --v3-auth-proto=SHA \
  --v3-priv-key=nautilus-priv-key --v3-priv-proto=AES256BLMT \
  --v3-user=nautilus-aes256c \
  --v3-auth-key=nautilus-auth-key --v3-auth-proto=SHA \
  --v3-priv-key=nautilus-priv-key --v3-priv-proto=AES256 \
  --v3-user=nautilus-legacy \
  --v3-auth-key=nautilus-auth-key --v3-auth-proto=MD5 \
  --v3-priv-key=nautilus-priv-key --v3-priv-proto=DES \
  --data-dir="$data" \
  --agent-udpv4-endpoint="$host:$port"
