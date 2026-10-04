# clips-index.sh — the per-run REVIEW INDEX for the rig's clips. Sourced on
# the HOST by smoke/run.sh and selftest.sh once a run's out/ dir is pulled
# back; writes <dir>/clips.html (open it: every clip inline, next to its
# verdict and evidence PNGs) and <dir>/clips.md (the same as a table).
#
#   smoke_clips_index    <out/smoke>     one row per check, from results.tsv
#   selftest_clips_index <out/selftest>  one row per verb, from verbs-selftest.tsv
#
# Clips come from lib.sh's clip_start/clip_stop (RIG_CLIPS=0 records none,
# and the index then says "no clip").

# _clips_write <dir> <title> — reads rows on stdin:
#   <id> TAB <verdict> TAB <what> TAB <clip file|""> TAB <png files, space-separated>
# (file names relative to <dir>).
_clips_write() {
  local dir=$1 title=$2 rows
  rows=$(cat)
  awk -F'\t' -v title="$title" -v md="$dir/clips.md" -v html="$dir/clips.html" '
    function esc(s) { gsub(/&/, "\\&amp;", s); gsub(/</, "\\&lt;", s); gsub(/>/, "\\&gt;", s); gsub(/"/, "\\&quot;", s); return s }
    function mdesc(s) { gsub(/\|/, "\\|", s); return s }
    BEGIN {
      print "# " title "\n\n| check / verb | verdict | what | clip | evidence |\n|---|---|---|---|---|" > md
      print "<!doctype html><meta charset=\"utf-8\"><title>" esc(title) "</title>" > html
      print "<style>body{font:14px system-ui,sans-serif;margin:16px;background:#fff;color:#111}" > html
      print "section{margin:0 0 28px}video{width:100%;max-width:960px;display:block;border:1px solid #ccc}" > html
      print ".png img{max-width:300px;border:1px solid #ccc;margin:4px 4px 0 0}.FAIL,.XPASS{color:#b00;font-weight:600}.PASS,.XFAIL{color:#070}.WARN{color:#a60}</style>" > html
      print "<h1>" esc(title) "</h1>" > html
    }
    {
      id = $1; v = $2; what = $3; clip = $4; n = split($5, pngs, " ")
      ev = ""; evh = ""
      for (i = 1; i <= n; i++) {
        ev = ev (i > 1 ? ", " : "") "[" pngs[i] "](" pngs[i] ")"
        evh = evh "<a href=\"" esc(pngs[i]) "\"><img loading=\"lazy\" src=\"" esc(pngs[i]) "\" alt=\"" esc(pngs[i]) "\"></a>"
      }
      print "| " mdesc(id) " | " v " | " mdesc(what) " | " (clip != "" ? "[" clip "](" clip ")" : "—") " | " (ev != "" ? ev : "—") " |" > md
      print "<section><h3>" esc(id) " <span class=\"" esc(v) "\">" esc(v) "</span></h3><p>" esc(what) "</p>" \
        (clip != "" ? "<video controls preload=\"metadata\" src=\"" esc(clip) "\"></video>" : "<p><i>no clip</i></p>") \
        (evh != "" ? "<div class=\"png\">" evh "</div>" : "") "</section>" > html
    }' <<<"$rows"
  echo "  review index: $dir/clips.html"
}

smoke_clips_index() { # <out/smoke>
  local dir=$1 tsv=$1/results.tsv src=/dev/null
  [[ -d $dir ]] || return 0
  [[ -f $tsv ]] && src=$tsv
  {
    # Checks in results order, then any clip whose check never wrote a row.
    { awk -F'\t' 'NR > 1 && !seen[$1]++ { print $1 }' "$src"
      ls "$dir" 2>/dev/null | sed -n 's/^\([0-9][0-9]-.*\)\.mp4$/\1/p'; } | awk '!seen[$0]++' |
    while IFS= read -r c; do
      awk -F'\t' -v c="$c" -v clip="$( [[ -f $dir/$c.mp4 ]] && echo "$c.mp4")" '
        NR > 1 && $1 == c {
          cnt[$2]++
          if ($2 == "FAIL" && fail == "") fail = $3
          if ($4 != "") { p = $4; sub(/.*\//, "", p); if (!got[p]++) pngs = pngs (pngs == "" ? "" : " ") p }
        }
        END {
          v = cnt["FAIL"] ? "FAIL" : cnt["WARN"] ? "WARN" : cnt["PASS"] ? "PASS" : cnt["SKIP"] ? "SKIP" : "—"
          what = fail != "" ? fail : sprintf("%d pass, %d warn, %d skip, %d note", cnt["PASS"], cnt["WARN"], cnt["SKIP"], cnt["NOTE"])
          printf "%s\t%s\t%s\t%s\t%s\n", c, v, what, clip, pngs
        }' "$src"
    done
  } | _clips_write "$dir" "Smoke clips — $(head -1 "$tsv" 2>/dev/null | sed 's/^# *//')"
}

selftest_clips_index() { # <out/selftest>
  local dir=$1 tsv=$1/verbs-selftest.tsv
  [[ -f $tsv ]] || return 0
  while IFS=$'\t' read -r n verb verdict detail; do
    local base="verbs-$n-$verb" clip="" png=""
    [[ -f $dir/$base.mp4 ]] && clip=$base.mp4
    [[ -f $dir/$base.png ]] && png=$base.png
    printf '%s\t%s\t%s\t%s\t%s\n' "$n $verb" "$verdict" "$detail" "$clip" "$png"
  done <"$tsv" | _clips_write "$dir" "Verb self-test clips"
}
