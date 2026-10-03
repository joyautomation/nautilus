#!/usr/bin/env bash
# 04 — editor-title buttons, per diagram language (.fbd, .ld, .sfc):
#
#   text editor     "Open as Diagram Editor" is there, and REPLACES the tab
#                   (reopenActiveEditorWith) carrying an unsaved edit over
#   diagram editor  "Diff … (vs git HEAD)" and "Show Source" are there, and
#                   the "…" menu has "between git revisions…" and
#                   "vs Controller"
#
# Button and menu identity is read off hover tooltips / the open menu (the
# PNGs); what each one DOES is asserted from the file, the title and the
# number of editors.
set -euo pipefail
CHECK=04-title-buttons
source "$HOME/smoke/lib.sh"
rm -rf "$PROFILE"

# The title-bar buttons of a single full-width editor at 1920x1200, side bar
# hidden, file CLEAN against git (right to left: …, split, then ours). Once
# the file is modified on disk the git extension's "Open Changes" joins them
# and ours move a slot left — so every button here is clicked before the
# save, while the edit is only in the buffer.
B1=1798; B2=1830; MORE=1893; BY=56

# back_to_diagram — close whatever opened beside (group 2+), focus group 1.
back_to_diagram() {
  xdotool key --clearmodifiers ctrl+1; sleep 0.5
  vs_cmd "View: Close Editors in Other Groups" 1.5
}

# slots <file> — B1/B2 for the file's git state: modified on disk adds the
# git extension's "Open Changes" and moves ours one slot (32 px) left.
slots() {
  if [[ -n $(git -C "$PROJ" status --porcelain -- "$1") ]]; then B1=1766; B2=1798; else B1=1798; B2=1830; fi
}
# as_diagram <file> — quick-open the text, then Open as Diagram Editor.
as_diagram() { open_file "$1" 3; vs_cmd "nautilus: Open as Diagram Editor" 6; }

one() { # <lang label> <project> <file> <old literal> <new literal>
  local lang=$1 file=$3 old=$4 new=$5
  local f=$PROJ/$file png
  smoke_open "$PROJ" "$file"
  key Escape; hide_sidebar
  hover $B1 $BY
  png=$(shot "$lang-text-buttons")
  info "$lang text editor: title buttons (hover: 'Open as Diagram Editor')" "$png"

  text_replace "$f" "$old" "$new"
  [[ $(title) == "● $file"* ]] || { fail "$lang: the unsaved text edit did not register ($(title))"; return; }
  click_at $B1 $BY 6
  png=$(shot "$lang-as-diagram")
  [[ $(title) == "● $file"* ]] && pass "$lang: Open as Diagram Editor → diagram, unsaved edit carried over (still dirty)" "$png" \
    || fail "$lang: after Open as Diagram Editor the title is '$(title)'" "$png"

  hover $B1 $BY; png=$(shot "$lang-diagram-btn1")
  info "$lang diagram editor: button 1 (hover: 'Diff … (vs git HEAD)')" "$png"
  hover $B2 $BY; png=$(shot "$lang-diagram-btn2")
  info "$lang diagram editor: button 2 (hover: 'Show Source')" "$png"

  click_at $B1 $BY 8
  png=$(shot "$lang-diff-head")
  info "$lang: Diff vs HEAD (the buffer's $old→$new change should be marked)" "$png"
  back_to_diagram
  click_at $MORE $BY 1.5
  png=$(shot "$lang-more-menu")
  info "$lang: '…' menu (between git revisions… / vs Controller)" "$png"
  xdotool key --clearmodifiers Escape; sleep 0.5
  click_at $B2 $BY 4
  png=$(shot "$lang-show-source")
  info "$lang: Show Source → the text beside the diagram" "$png"
  # Close that text tab again (Ctrl+W, focus is on it). The diagram still
  # has the document open, so ideally this just closes; VS Code instead
  # asks to save — see the SFC round for what "Don't Save" then does.
  xdotool key --clearmodifiers ctrl+w; sleep 1.5
  png=$(shot "$lang-close-source")
  if (( $(px_count "$png" 1180 645 1250 680 'r > 90 and b > 150 and g < 110') > 200 )); then
    warn "$lang: closing Show Source's text tab while the diagram editor still has the file open prompts Save / Don't Save" "$png"
    click_at 1214 662 2          # Save
  fi
  back_to_diagram
  grep -qF -- "$new" "$f" && pass "$lang: the carried-over edit saved from the diagram session ($new on disk)" \
    || { click_at 1500 1000 0.5; xdotool key --clearmodifiers ctrl+s; sleep 1.5
         grep -qF -- "$new" "$f" && pass "$lang: Ctrl+S in the diagram editor saved the carried-over edit" \
           || fail "$lang: the carried-over edit never reached disk"; }
  [[ $(title) != "●"* ]] || fail "$lang: diagram still dirty after the save: $(title)"
  # It REPLACED the text tab rather than opening beside it: closing it
  # leaves no editor at all.
  xdotool key --clearmodifiers ctrl+w; sleep 1.5
  [[ $(title) == "$(basename "$PROJ") - Visual Studio Code" ]] \
    && pass "$lang: the diagram replaced the text tab (closing it leaves no editor)" \
    || fail "$lang: an editor was left behind after closing the diagram: '$(title)'"
}

ext_scaffold my-plant
one FBD my-plant program.fbd "62.0" "61.0"
one Ladder my-plant interlocks.ld "90.0" "85.0"
ext_fixture tank-batch
one SFC tank-batch batch.sfc "Start AND NOT Abort" "Start AND Abort"

# The Don't-Save case, once: an unsaved diagram edit, Show Source, close the
# text, "Don't Save". The diagram is still open on the same document.
S=$PROJ/batch.sfc
as_diagram batch.sfc
click_at 73 132 1; xdotool type --delay 40 Extra; key Return; sleep 2
[[ $(title) == "● batch.sfc"* ]] || fail "SFC: add-step did not dirty the diagram"
# Show Source by the palette here: the button was exercised per language
# above, and right after a webview edit its first click is unreliable.
key Escape
vs_cmd "nautilus: Show Source" 4
[[ $(title) == "● batch.sfc"* ]] || info "SFC: Show Source title: $(title)"
xdotool key --clearmodifiers ctrl+w; sleep 1.5
png=$(shot dont-save-prompt)
if (( $(px_count "$png" 1180 645 1250 680 'r > 90 and b > 150 and g < 110') > 200 )); then
  click_at 1046 662 2           # Don't Save
  png=$(shot dont-save-after)
  if [[ $(title) != "●"* ]] && ! grep -q "STEP Extra" "$S"; then
    warn "SFC: 'Don't Save' on closing Show Source's text DISCARDED the diagram editor's unsaved edit (step Extra gone; diagram clean, still open)" "$png"
  else
    pass "SFC: the diagram kept its unsaved edit after the text tab was closed" "$png"
  fi
else
  info "SFC: no save prompt on closing the text (Show Source may not have opened) — Don't-Save case not exercised" "$png"
fi
