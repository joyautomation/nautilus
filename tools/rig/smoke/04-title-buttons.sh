#!/usr/bin/env bash
# 04 — editor-title buttons, per diagram language (.fbd, .ld, .sfc):
#
#   text editor     "Open as Diagram Editor" is there, and REPLACES the tab
#                   (reopenActiveEditorWith) carrying an unsaved edit over
#   diagram editor  "Diff … (vs git HEAD)" and "Show Source" are there, and
#                   the "…" menu has "between git revisions…" and
#                   "vs Controller"
#
# Every button is found in the workbench DOM by its label (the command's
# title, the action's aria-label), so the git extension's "Open Changes"
# joining the row when the file is modified on disk no longer moves the
# target; it is hovered for the tooltip in the PNG, and what it DOES is
# asserted from the file, the title and the number of editors. The modal
# is read as DOM too (window.dialogStyle custom).
set -euo pipefail
CHECK=04-title-buttons
source "$HOME/smoke/lib.sh"
export G_PACE=fast
source "$HOME/fixtures/gestures.sh"   # cdp page / page_click_button / g_click
rm -rf "$PROFILE"

# The editor-title actions, by aria-label (JS regex source).
AS_DIAGRAM='Open as Diagram Editor'
DIFF_HEAD='Diff \w+ Diagram \(vs git HEAD\)'
SHOW_SOURCE='Show Source'
MORE='^More Actions'

# back_to_diagram — close whatever opened beside (group 2+), focus group 1.
back_to_diagram() {
  xdotool key --clearmodifiers ctrl+1; sleep 0.5
  vs_cmd "View: Close Editors in Other Groups" 1.5
}

# label <regex> — the matching title action's aria-label ("" if none).
label() { pg "($(title_action_el "$1"))?.getAttribute('aria-label') ?? ''"; }
# save_prompt — the custom modal is up and offers Save and Don't Save.
save_prompt() { dialog_up && [[ " / $(dialog_buttons) / " == *" / Save / "* && $(dialog_buttons) == *"Don't Save"* ]]; }
# as_diagram <file> — quick-open the text, then Open as Diagram Editor.
as_diagram() { open_file "$1" 3; vs_cmd "nautilus: Open as Diagram Editor" 6; }

one() { # <lang label> <project> <file> <old literal> <new literal>
  local lang=$1 file=$3 old=$4 new=$5
  local f=$PROJ/$file png
  smoke_open "$PROJ" "$file"
  key Escape; hide_sidebar
  page_hover "$(title_action_el "$AS_DIAGRAM")" || true
  png=$(shot "$lang-text-buttons")
  info "$lang text editor: title buttons (hover: 'Open as Diagram Editor'; DOM label '$(label "$AS_DIAGRAM")')" "$png"

  text_replace "$f" "$old" "$new"
  [[ $(title) == "● $file"* ]] || { fail "$lang: the unsaved text edit did not register ($(title))"; return; }
  page_click "$(title_action_el "$AS_DIAGRAM")" 6 || fail "$lang: no 'Open as Diagram Editor' title button"
  png=$(shot "$lang-as-diagram")
  [[ $(title) == "● $file"* ]] && pass "$lang: Open as Diagram Editor → diagram, unsaved edit carried over (still dirty)" "$png" \
    || fail "$lang: after Open as Diagram Editor the title is '$(title)'" "$png"

  page_hover "$(title_action_el "$DIFF_HEAD")" || true; png=$(shot "$lang-diagram-btn1")
  info "$lang diagram editor: button 1 (hover: 'Diff … (vs git HEAD)'; DOM label '$(label "$DIFF_HEAD")')" "$png"
  page_hover "$(title_action_el "$SHOW_SOURCE")" || true; png=$(shot "$lang-diagram-btn2")
  info "$lang diagram editor: button 2 (hover: 'Show Source'; DOM label '$(label "$SHOW_SOURCE")')" "$png"

  page_click "$(title_action_el "$DIFF_HEAD")" 8 || fail "$lang: no 'Diff … (vs git HEAD)' title button"
  png=$(shot "$lang-diff-head")
  info "$lang: Diff vs HEAD (the buffer's $old→$new change should be marked)" "$png"
  back_to_diagram
  page_click "$(title_action_el "$MORE")" 1.5 || fail "$lang: no '…' (More Actions) title button"
  png=$(shot "$lang-more-menu")
  info "$lang: '…' menu (between git revisions… / vs Controller)" "$png"
  xdotool key --clearmodifiers Escape; sleep 0.5
  page_click "$(title_action_el "$SHOW_SOURCE")" 4 || fail "$lang: no 'Show Source' title button"
  png=$(shot "$lang-show-source")
  info "$lang: Show Source → the text beside the diagram" "$png"
  # Close that text tab again (Ctrl+W, focus is on it). The diagram still
  # has the document open, so ideally this just closes; VS Code instead
  # asks to save — see the SFC round for what "Don't Save" then does.
  xdotool key --clearmodifiers ctrl+w; sleep 1.5
  png=$(shot "$lang-close-source")
  if save_prompt; then
    warn "$lang: closing Show Source's text tab while the diagram editor still has the file open prompts Save / Don't Save" "$png"
    page_click_button '^Save$' 2 || fail "$lang: the save prompt has no Save button ($(dialog_buttons))"
  fi
  back_to_diagram
  grep -qF -- "$new" "$f" && pass "$lang: the carried-over edit saved from the diagram session ($new on disk)" \
    || { click_canvas "" 0.5 || true; xdotool key --clearmodifiers ctrl+s; sleep 1.5
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
click_button "+ step" || fail "SFC: no '+ step' button"
wait_js 'doc.activeElement?.matches(".addform input")' 4 || true
xdotool type --delay 40 Extra; key Return; sleep 2
[[ $(title) == "● batch.sfc"* ]] || fail "SFC: add-step did not dirty the diagram"
# Show Source by the palette here: the button was exercised per language
# above, and right after a webview edit its first click is unreliable.
key Escape
vs_cmd "nautilus: Show Source" 4
[[ $(title) == "● batch.sfc"* ]] || info "SFC: Show Source title: $(title)"
xdotool key --clearmodifiers ctrl+w; sleep 1.5
png=$(shot dont-save-prompt)
if save_prompt; then
  page_click_button "^Don't Save\$" 2 || fail "SFC: the save prompt has no Don't Save button ($(dialog_buttons))"
  png=$(shot dont-save-after)
  if [[ $(title) != "●"* ]] && ! grep -q "STEP Extra" "$S"; then
    warn "SFC: 'Don't Save' on closing Show Source's text DISCARDED the diagram editor's unsaved edit (step Extra gone; diagram clean, still open)" "$png"
  else
    pass "SFC: the diagram kept its unsaved edit after the text tab was closed" "$png"
  fi
else
  info "SFC: no save prompt on closing the text (Show Source may not have opened) — Don't-Save case not exercised" "$png"
fi
