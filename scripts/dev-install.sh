#!/usr/bin/env bash
#
# dev-install.sh — build the CLI from the working tree and put it where a
# bare `naut` will actually find it.
#
# `go install ...@main` is the wrong loop while developing: it goes through
# the module proxy, so it can only ever see code that is committed AND
# pushed, and it writes to $GOBIN — which may sit behind another `naut`
# on your PATH, or behind /usr/bin/nautilus, which on a GNOME desktop is the
# file manager. The failure is silent: the install succeeds, the shell keeps
# running a different binary, and the change appears not to have worked.
#
# So this script resolves the destination from PATH rather than assuming it,
# refuses to write anywhere the answer would be wrong, and verifies what the
# shell resolves afterwards.
#
#   scripts/dev-install.sh              # build HEAD's working tree, install, verify
#   NAUTILUS_DEV_BIN=~/bin scripts/dev-install.sh   # force the destination
#
set -euo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo"

die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
note() { printf '  %s\n' "$*"; }

# ── where should this go? ────────────────────────────────────────────────
# A system bin directory is never a valid target: /usr/bin/nautilus is GNOME
# Files on most desktops, and clobbering it would break the file manager.
is_system() {
  case "$1" in
    /usr/local/bin|/usr/bin|/bin|/usr/sbin|/sbin|/usr/local/sbin) return 0 ;;
    *) return 1 ;;
  esac
}

# Walk PATH left to right, recording two things: the first directory that
# already holds a `naut` (whatever the shell runs today), and the first
# user-writable directory (where we are allowed to write).
shadow_dir="" shadow_idx=-1
user_dir="" user_idx=-1
idx=0
IFS=':' read -ra path_dirs <<< "$PATH"
for d in "${path_dirs[@]}"; do
  [ -n "$d" ] || d="."
  d=${d%/}
  if [ "$shadow_idx" -lt 0 ] && [ -x "$d/nautilus" ]; then
    shadow_dir=$d shadow_idx=$idx
  fi
  if [ "$user_idx" -lt 0 ] && ! is_system "$d" && [ -d "$d" ] && [ -w "$d" ]; then
    user_dir=$d user_idx=$idx
  fi
  idx=$((idx + 1))
done

if [ -n "${NAUTILUS_DEV_BIN:-}" ]; then
  dest_dir=${NAUTILUS_DEV_BIN%/}
  [ -d "$dest_dir" ] || die "NAUTILUS_DEV_BIN=$dest_dir is not a directory"
elif [ "$shadow_idx" -ge 0 ] && ! is_system "$shadow_dir"; then
  # Something already answers to `naut` and we're allowed to replace it.
  # Replacing is the point: deleting it instead would let a later PATH entry
  # (possibly /usr/bin, i.e. the file manager) win the name.
  dest_dir=$shadow_dir
  [ -w "$dest_dir" ] || die "$dest_dir/nautilus is first on PATH but $dest_dir is not writable"
elif [ "$user_idx" -ge 0 ] && { [ "$shadow_idx" -lt 0 ] || [ "$user_idx" -lt "$shadow_idx" ]; }; then
  dest_dir=$user_dir
else
  # The only nautilus on PATH is a system one and every writable directory
  # sits behind it. Nothing we install can win; say so instead of writing a
  # binary the shell will never run.
  die "$(cat <<EOF
the first 'nautilus' on your PATH is $shadow_dir/nautilus (a system path,
probably GNOME Files) and every writable PATH directory comes after it, so
an install here could not take effect.

Put a user directory ahead of $shadow_dir on your PATH, e.g.
    export PATH="\$HOME/.local/bin:\$PATH"
then re-run this script. Or set NAUTILUS_DEV_BIN to override.
EOF
)"
fi

# ── build ────────────────────────────────────────────────────────────────
# Stamp the commit rather than a version: a working-tree build is not any
# release, and `naut version` saying "dev" tells you nothing about which
# dev. -dirty is the common case here and is the useful part.
sha=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
dirty=""
git diff --quiet HEAD -- 2>/dev/null || dirty="-dirty"
version="dev-${sha}${dirty}"

echo "building nautilus $version"
tmp="$dest_dir/.nautilus.$$"
trap 'rm -f "$tmp"' EXIT
go build -ldflags "-X github.com/joyautomation/nautilus/internal/lsp.Version=$version" \
  -o "$tmp" ./cmd/naut
chmod 0755 "$tmp"
# Rename within the destination directory: atomic, and safe while a
# controller is running (the running process keeps its own inode).
mv -f "$tmp" "$dest_dir/nautilus"
trap - EXIT
echo "installed  $dest_dir/nautilus"

# ── verify ───────────────────────────────────────────────────────────────
# The whole point of the script. An install that lands somewhere the shell
# doesn't look is the bug we're preventing, so prove it didn't happen.
hash -r 2>/dev/null || true
resolved=$(command -v nautilus || true)
[ -n "$resolved" ] || die "installed to $dest_dir but nothing named 'nautilus' is on PATH — is $dest_dir on it?"

got=$("$resolved" version 2>/dev/null | awk '{print $2}')
if [ "$resolved" != "$dest_dir/nautilus" ] || [ "$got" != "$version" ]; then
  echo
  note "PATH resolves 'nautilus' to $resolved (version ${got:-unknown})"
  note "but this build went to $dest_dir/nautilus (version $version)"
  die "a different binary still wins the name — check: which -a nautilus"
fi

echo "verified   $resolved -> $version"

# Other copies further down PATH aren't wrong, but they're how you end up
# debugging a months-old binary after a PATH change, so name them — and name
# the system one separately, because it isn't a stale nautilus at all.
stale=() system=()
while read -r o; do
  [ -n "$o" ] && [ "$o" != "$resolved" ] || continue
  if is_system "$(dirname "$o")"; then system+=("$o"); else stale+=("$o"); fi
done < <(type -aP nautilus 2>/dev/null)

if [ ${#stale[@]} -gt 0 ]; then
  echo
  note "also on PATH, shadowed — stale copies of this CLI:"
  for o in "${stale[@]}"; do note "  $o  ($("$o" version 2>/dev/null | awk '{print $2}' || echo '?'))"; done
  note "re-run with NAUTILUS_DEV_BIN=<dir> to refresh one, or leave them."
fi
if [ ${#system[@]} -gt 0 ]; then
  echo
  note "note: ${system[0]} is a different program (GNOME Files)."
  note "it takes the name if you ever delete $resolved — replace, don't remove."
fi

echo
note "a controller started before now still runs the old binary — restart it"
