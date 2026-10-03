#!/usr/bin/env bash
# A throwaway Incus container with a virtual display and a VS Code that has
# never seen anything.
#
# Sourced by tools/rig/smoke/run.sh, tools/rig/selftest.sh, and the content
# repo's assets/capture/record-vscode.sh (which finds it through RIG_DIR).
# Provides: rig_up, rig_run, rig_pull, rig_push, rig_push_tree, rig_down.
#
# RIG_NAME is the container. One run per name at a time: every entry point's
# EXIT trap is rig_down, so a second run on the same name deletes the
# container under the first. Give each concurrent run (and each machine that
# shares an incus remote) its own RIG_NAME.
#
# WHY THIS EXISTS. Beat 4a is the extension-recommendation toast, and it fires
# exactly once — on a profile that has never had the extension. mira1's profile
# has had it for weeks, so the beat was marked `live` and shot by hand, for the
# same reason beat 2a was: the machine that develops the thing is the worst
# machine to demonstrate first contact with it. A container that has never seen
# the extension is a first contact you can re-render.
#
# It pays for more than 4a. Beats 4b, 5a and 7 are VS Code takes that need
# mira1's session UNLOCKED, because xdotool goes through XTEST and the lock
# screen's keyboard grab swallows it — so recording them means being at the
# machine and giving up the screen for the duration. In here, nothing is
# swallowed and nothing is taken over.
#
# The look has to match the takes shot on mira1, or the edit shows a seam every
# time it cuts between rigs. That is why this file provisions a display and
# copies lib.sh in, and does NOT reimplement any of vscode_profile(),
# launch_vscode() or rec_start(). One implementation, two displays.

RIG_NAME=${RIG_NAME:-nautilus-vscode-rig}
RIG_IMAGE=${RIG_IMAGE:-nautilus-vscode-rig}
RIG_BASE=${RIG_BASE:-images:ubuntu/noble}
RIG_DISPLAY=${RIG_DISPLAY:-:99}
RIG_USER=${RIG_USER:-dev}
# The Xvfb screen itself, sized with margin over the largest capture frame
# (CAP_WxCAP_H — the series frame is 2560x1440) rather than exactly matching
# it. VS Code's GTK CSD frame margin (_GTK_FRAME_EXTENTS, 4 px top + bottom
# on 1.139) makes the window need CAP_H+8, and a display sized to exactly
# CAP_H leaves ensure_work_area no slack: it falls back to xrandr panning on
# EVERY take instead of only when a beat genuinely needs more room. 40 px is
# comfortably over the 8 px GTK margin with room to spare (found: the first
# beat recorded at the series frame, 2026-09-25 — xrandr wasn't even
# installed in the image at the time).
RIG_XVFB_W=${RIG_XVFB_W:-$(( ${CAP_W:-2560} + 40 ))}
RIG_XVFB_H=${RIG_XVFB_H:-$(( ${CAP_H:-1440} + 40 ))}

_rig() { incus exec "$RIG_NAME" -- "$@"; }
# Everything the capture does runs as an ordinary user: VS Code refuses to run
# as root without --no-sandbox, and a take is not the place to find that out.
rig_run() { incus exec "$RIG_NAME" -- su - "$RIG_USER" -c "$1"; }
rig_pull() { incus file pull "$RIG_NAME$1" "$2"; }
rig_push() { incus file push "$1" "$RIG_NAME$2" >/dev/null; }
# rig_push_tree <host dir> <container dir> — the CONTENTS of <host dir> into
# <container dir> (created if missing), merged over whatever is there. How
# the verbs and an episode's fixtures share ~/fixtures: verbs first, then the
# episode's own files on top, so an episode that carries its own copy of a
# verb file (wk06) still gets its copy. Ownership is the caller's to fix.
rig_push_tree() {
  _rig mkdir -p "$2"
  tar -C "$1" -cf - . | incus exec "$RIG_NAME" -- tar -C "$2" -xf - --no-same-owner
}

rig_down() { incus delete -f "$RIG_NAME" >/dev/null 2>&1 || true; }

# Provision from scratch: ~5 minutes, most of it apt. Published as an image
# afterwards so the next run is seconds — these beats get re-rendered, and a
# five-minute wait per attempt is how a rig stops being used.
_rig_provision() {
  echo "  provisioning (apt, VS Code, fonts) — once, then published as an image"
  # python3-xlib: lib.sh's unmaximize_window (it now falls back to wmctrl
  # without it, but the mira1 path is the tested one). fonts-symbola: the
  # ladder toolbar's copy/paste glyphs (U+29C9, U+2398) are not in DejaVu and
  # rendered as tofu in the ext-stable stills. Symbola, not fonts-noto-core —
  # Noto takes over as the default sans and changes the whole UI's look.
  # x11-xserver-utils: xrandr, for ensure_work_area's panning fallback in
  # lib.sh. Distinct from x11-utils (xprop/xdpyinfo/xwininfo), which does NOT
  # pull xrandr in — an image published before this line has no xrandr, which
  # is why rig_up below checks for it too.
  _rig bash -c '
    set -e
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -yqq --no-install-recommends \
      xvfb x11-utils x11-xserver-utils xdotool wmctrl openbox ffmpeg python3-xlib python3-pil fonts-symbola \
      curl ca-certificates gpg git fontconfig fonts-dejavu-core \
      libnss3 libatk1.0-0t64 libatk-bridge2.0-0t64 libgtk-3-0t64 \
      libasound2t64 libxss1 libsecret-1-0 libgbm1 xdg-utils >/dev/null
    curl -sSL https://packages.microsoft.com/keys/microsoft.asc \
      | gpg --dearmor -o /usr/share/keyrings/microsoft.gpg
    echo "deb [arch=amd64 signed-by=/usr/share/keyrings/microsoft.gpg] https://packages.microsoft.com/repos/code stable main" \
      > /etc/apt/sources.list.d/vscode.list
    apt-get update -qq
    apt-get install -yqq code >/dev/null
    id dev >/dev/null 2>&1 || useradd -m -s /bin/bash dev'

  # These go in the DEFAULT extensions directory, because check_theme() asks
  # `code --list-extensions` and that only looks there. joyauto.vscode-iec
  # deliberately does NOT go in — its absence is the beat.
  #
  # The theme is here so the take matches the ones shot on mira1. Red Hat's
  # YAML extension is here for a different reason: the scaffold recommends TWO
  # extensions, `joyauto.vscode-iec` and `redhat.vscode-yaml`, and VS Code
  # prompts for ONE of them. The first take out of this rig was a technically
  # perfect recording of "Do you want to install the recommended 'YAML'
  # extension from Red Hat" — the toast fired, and it advertised somebody
  # else's extension. Installing YAML up front leaves exactly one outstanding
  # recommendation, so there is nothing for VS Code to choose between.
  echo "  installing the theme and YAML (the missing one is the shot)"
  rig_run "code --install-extension sdras.night-owl 2>&1 | tail -1"
  rig_run "code --install-extension redhat.vscode-yaml 2>&1 | tail -1"
}

# _rig_launch <image> — `incus launch`, bounded. On mira1 the client has
# hung with no server-side operation at all (2026-09-25, twice in one night,
# ~11 min each, while other rigs were launching): nothing is created and
# nothing ever returns. The cause: when stdin is not a terminal, `incus
# launch` reads instance config (YAML) from it — an inherited pipe that never
# closes (a harness, a background job) and the client waits forever before
# sending anything. So stdin is /dev/null, always. The timeout and retries
# stay as a backstop: 120 s a try, the half-made container deleted between.
_rig_launch() {
  local i
  for i in 1 2 3; do
    timeout 120 incus launch "$1" "$RIG_NAME" </dev/null >/dev/null && return 0
    echo "  incus launch $1 did not finish (try $i) — retrying" >&2
    rig_down
  done
  echo "incus launch $1 $RIG_NAME failed three times" >&2; exit 1
}

# rig_up [--fresh] — a running container with a display, ready to be driven.
rig_up() {
  local fresh=""
  [[ ${1:-} == --fresh ]] && fresh=1
  rig_down

  if [[ -z $fresh ]] && incus image list --format csv 2>/dev/null | grep -q "^$RIG_IMAGE,"; then
    echo "  launching from image $RIG_IMAGE"
    _rig_launch "$RIG_IMAGE"
  else
    echo "  no image $RIG_IMAGE yet — building it"
    incus launch "$RIG_BASE" "$RIG_NAME" </dev/null >/dev/null
    _rig_wait_net
    _rig_provision
    echo "  publishing $RIG_IMAGE for next time"
    incus stop "$RIG_NAME" >/dev/null
    # --rebuild-image exists to replace an image that is WRONG, so the old one
    # is always in the way at this point. `incus publish --alias` will not take
    # an alias that is already claimed, and it says so after the five minutes of
    # provisioning rather than before.
    incus image delete "$RIG_IMAGE" >/dev/null 2>&1 || true
    incus publish "$RIG_NAME" --alias "$RIG_IMAGE" >/dev/null
    incus start "$RIG_NAME" >/dev/null
  fi
  _rig_wait_net

  # An image built before x11-xserver-utils joined _rig_provision has no
  # xrandr — tolerate it here rather than forcing a rebuild of every such
  # image. Costs ~5-10s of apt on an old image, once (nothing on a current
  # one, and nothing at all once every published image has been rebuilt).
  _rig bash -c '
    command -v xrandr >/dev/null || {
      export DEBIAN_FRONTEND=noninteractive
      apt-get update -qq && apt-get install -yqq x11-xserver-utils >/dev/null
    }'

  # A display, and a window manager. Without a WM, xdotool windowactivate has
  # nothing to talk to and VS Code opens at whatever size it likes — which is
  # not ${RIG_XVFB_W}x${RIG_XVFB_H}, and is not something you notice until you
  # read the frames.
  #
  # The screen is sized with margin OVER the capture frame (CAP_WxCAP_H), not
  # exactly to it — see RIG_XVFB_W/H above for why: VS Code's GTK CSD frame
  # margin needs a few px more than CAP_H, and a display with no slack makes
  # ensure_work_area (lib.sh) fall back to xrandr panning on every take
  # instead of only when a beat genuinely asks for more room.
  rig_run "export DISPLAY=$RIG_DISPLAY
    pgrep -x Xvfb >/dev/null || nohup Xvfb $RIG_DISPLAY -screen 0 ${RIG_XVFB_W}x${RIG_XVFB_H}x24 -nolisten tcp >/tmp/xvfb.log 2>&1 &
    sleep 2
    pgrep -x openbox >/dev/null || nohup openbox >/tmp/openbox.log 2>&1 &
    sleep 1
    xdpyinfo -display $RIG_DISPLAY >/dev/null" \
    || { echo "no display in the container" >&2; return 2; }
  echo "  display $RIG_DISPLAY up at ${RIG_XVFB_W}x${RIG_XVFB_H}"
}

_rig_wait_net() {
  local i
  for i in $(seq 60); do
    _rig getent hosts archive.ubuntu.com >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "container never got a network" >&2
  return 2
}
