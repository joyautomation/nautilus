#!/usr/bin/env bash
#
# prom-sim.sh — run the foreign Prometheus stack the prom-sim CI job runs,
# on a laptop, so `go test ./prom/ -run TestForeign` has something to talk
# to: a REAL node_exporter release binary, not a fixture.
#
#   scripts/prom-sim.sh                      # 127.0.0.1:9100
#   scripts/prom-sim.sh --port 9101          # a second instance
#
# Then, in another terminal (the script prints the exact line):
#
#   NAUTILUS_PROM_SIM=127.0.0.1:9100 go test ./prom/... -run TestForeign -v
#
# The release is downloaded once into a cache OUTSIDE the repo (under
# $TMPDIR, or /tmp) and its sha256 verified against the pin below before it
# is ever executed — nothing to gitignore, nothing for `go vet ./...` to
# trip over, and a corrupted or substituted download refuses to run rather
# than silently executing something else.
#
# VERSION is the release this was last checked against (2026-09-26: the
# latest tag on github.com/prometheus/node_exporter/releases). Re-pin both
# the version and the sha256 together — never one without the other.
set -euo pipefail

VERSION=1.12.1
SHA256_LINUX_AMD64=b51d8a76aa2a9156a55d501aca6276fae09e262259a5e4e831d2c2222f084e63
SHA256_LINUX_ARM64=""   # fill in if/when this is run on arm64 CI
SHA256_DARWIN_AMD64=3506a6d788b768a3c2cd1f58dc97b9ff3fecc5ac9196623d582982db2aa9d0f6
SHA256_DARWIN_ARM64=""  # node_exporter does not publish a darwin/arm64 build as of 1.12.1

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "prom-sim.sh: unsupported architecture $arch" >&2; exit 1 ;;
esac

case "${os}_${arch}" in
  linux_amd64)  pin=$SHA256_LINUX_AMD64 ;;
  linux_arm64)  pin=$SHA256_LINUX_ARM64 ;;
  darwin_amd64) pin=$SHA256_DARWIN_AMD64 ;;
  darwin_arm64) pin=$SHA256_DARWIN_ARM64 ;;
  *) echo "prom-sim.sh: unsupported platform ${os}/${arch}" >&2; exit 1 ;;
esac
if [ -z "$pin" ]; then
  echo "prom-sim.sh: no pinned sha256 for ${os}/${arch} — add one from" \
       "github.com/prometheus/node_exporter/releases/download/v${VERSION}/sha256sums.txt" >&2
  exit 1
fi

archive="node_exporter-${VERSION}.${os}-${arch}.tar.gz"
url="https://github.com/prometheus/node_exporter/releases/download/v${VERSION}/${archive}"
cache="${NAUTILUS_PROM_SIM_CACHE:-${TMPDIR:-/tmp}/nautilus-prom-sim}"
bin="$cache/node_exporter-${VERSION}.${os}-${arch}/node_exporter"

mkdir -p "$cache"
if [ ! -x "$bin" ]; then
  echo "downloading $url" >&2
  curl -sL --fail -o "$cache/$archive" "$url"
  got=$(sha256sum "$cache/$archive" | awk '{print $1}')
  if [ "$got" != "$pin" ]; then
    echo "prom-sim.sh: checksum mismatch for $archive" >&2
    echo "  got:  $got" >&2
    echo "  want: $pin" >&2
    rm -f "$cache/$archive"
    exit 1
  fi
  tar -xzf "$cache/$archive" -C "$cache"
  rm -f "$cache/$archive"
fi
if [ ! -x "$bin" ]; then
  echo "prom-sim.sh: $bin missing after extraction" >&2
  exit 1
fi

host=127.0.0.1
port=9100
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
  case "${args[i]}" in
    --port) port=${args[i + 1]:-$port} ;;
    --host) host=${args[i + 1]:-$host} ;;
  esac
done
echo "NAUTILUS_PROM_SIM=$host:$port go test ./prom/... -run TestForeign -v" >&2
exec "$bin" "--web.listen-address=$host:$port"
