# AGENTS.md eval: claude, sonnet

Started 2026-11-04 01:21 UTC. k = 3 per cell. AGENTS.md sha256 `7280d6a4b760`. naut at `3de610f41b8c`. Limits per run: 600 s, $1.50.

**Good** = compiles, the required language is a task program, every hidden acceptance test passes, `naut check` prints no warning, and no structure rule is broken. The other columns split that verdict: correct (compiles + language + hidden tests), clean (no warnings), structured (no structure rule broken).

## Good, per task

| task | lang | with: good | with: correct · clean · structured | without: good | without: correct · clean · structured |
|---|---|---|---|---|---|
| analog-pid | fbd | **3/3** | 3/3 · 3/3 · 3/3 | **0/3** | 3/3 · 3/3 · 0/3 |
| batch-sfc | sfc | **3/3** | 3/3 · 3/3 · 3/3 | **1/3** | 2/3 · 2/3 · 1/3 |
| conveyor-fbd | fbd | **3/3** | 3/3 · 3/3 · 3/3 | **0/3** | 1/3 · 1/3 · 2/3 |
| motor-seal-in | ld | **3/3** | 3/3 · 3/3 · 3/3 | **0/3** | 0/3 · 0/3 · 3/3 |
| tank-fill | ld | **3/3** | 3/3 · 3/3 · 3/3 | **0/3** | 1/3 · 1/3 · 1/3 |
| totalizer-st | st | **3/3** | 3/3 · 3/3 · 3/3 | **2/3** | 2/3 · 3/3 · 3/3 |
| traffic-light | ld | **3/3** | 3/3 · 3/3 · 3/3 | **0/3** | 1/3 · 1/3 · 2/3 |
| **all** | | **21/21 (100%)** | 100% · 100% · 100% | **3/21 (14%)** | 48% · 52% · 57% |

## Why runs were not Good

Runs showing each cause (a run can show several).

| cause | with | without |
|---|---|---|
| does not compile | 0/21 | 7/21 |
| hidden tests did not run | 0/21 | 4/21 |
| structure: fbd.logic-outside-network | 0/21 | 4/21 |
| structure: fbd.nesting>2 | 0/21 | 2/21 |
| structure: ld.parse-error | 0/21 | 1/21 |
| structure: ld.series>6 | 0/21 | 1/21 |
| structure: ld.uncommented-rung | 0/21 | 2/21 |
| structure: sfc.action-steers-chart | 0/21 | 1/21 |
| structure: sfc.parse-error | 0/21 | 1/21 |
| structure: sfc.unnamed-transition | 0/21 | 1/21 |
| warning: unbound-tag | 0/21 | 3/21 |
| wrong language: no fbd task | 0/21 | 2/21 |
| wrong language: no ld task | 0/21 | 1/21 |

## How the agent worked

| condition | runs | mean turns | mean cost | mean time | ran `naut check` | ran `naut test` | wrote tests | read AGENTS.md | timed out |
|---|---|---|---|---|---|---|---|---|---|
| with | 21 | 9.0 | $0.39 | 44 s | 95% | 95% | 43% | 0% | 0 |
| without | 21 | 19.8 | $0.44 | 67 s | 52% | 52% | 29% | 0% | 0 |

"read AGENTS.md" counts an explicit Read of the file; Claude Code also loads it through CLAUDE.md's `@AGENTS.md` import without one.

## Runs

| task | cond | # | verdict | hidden | warnings | structure | turns | cost |
|---|---|---|---|---|---|---|---|---|
| analog-pid | with | 1 | **good** | 7/7 |  |  | 9 | $0.38 |
| analog-pid | with | 2 | **good** | 7/7 |  |  | 8 | $0.37 |
| analog-pid | with | 3 | **good** | 7/7 |  |  | 11 | $0.40 |
| analog-pid | without | 1 | correct | 7/7 |  | fbd.logic-outside-network | 29 | $0.51 |
| analog-pid | without | 2 | correct | 7/7 |  | fbd.logic-outside-network, fbd.nesting>2 | 25 | $0.49 |
| analog-pid | without | 3 | correct | 7/7 |  | fbd.logic-outside-network, fbd.nesting>2 | 20 | $0.44 |
| batch-sfc | with | 1 | **good** | 6/6 |  |  | 8 | $0.37 |
| batch-sfc | with | 2 | **good** | 6/6 |  |  | 8 | $0.38 |
| batch-sfc | with | 3 | **good** | 6/6 |  |  | 11 | $0.42 |
| batch-sfc | without | 1 | no compile | – |  | sfc.parse-error | 12 | $0.36 |
| batch-sfc | without | 2 | **good** | 6/6 |  |  | 59 | $0.95 |
| batch-sfc | without | 3 | correct | 6/6 |  | sfc.unnamed-transition, sfc.action-steers-chart | 45 | $0.74 |
| conveyor-fbd | with | 1 | **good** | 8/8 |  |  | 8 | $0.38 |
| conveyor-fbd | with | 2 | **good** | 8/8 |  |  | 7 | $0.38 |
| conveyor-fbd | with | 3 | **good** | 8/8 |  |  | 8 | $0.38 |
| conveyor-fbd | without | 1 | fail | – | unbound-tag×2 |  | 7 | $0.29 |
| conveyor-fbd | without | 2 | correct | 8/8 |  | fbd.logic-outside-network | 55 | $0.76 |
| conveyor-fbd | without | 3 | fail | – | unbound-tag×2 |  | 8 | $0.29 |
| motor-seal-in | with | 1 | **good** | 8/8 |  |  | 8 | $0.33 |
| motor-seal-in | with | 2 | **good** | 8/8 |  |  | 8 | $0.32 |
| motor-seal-in | with | 3 | **good** | 8/8 |  |  | 8 | $0.36 |
| motor-seal-in | without | 1 | no compile | – |  |  | 11 | $0.34 |
| motor-seal-in | without | 2 | no compile | – |  |  | 11 | $0.33 |
| motor-seal-in | without | 3 | no compile | – |  |  | 10 | $0.31 |
| tank-fill | with | 1 | **good** | 7/7 |  |  | 8 | $0.37 |
| tank-fill | with | 2 | **good** | 7/7 |  |  | 9 | $0.40 |
| tank-fill | with | 3 | **good** | 7/7 |  |  | 8 | $0.39 |
| tank-fill | without | 1 | correct | 7/7 |  | ld.uncommented-rung | 19 | $0.43 |
| tank-fill | without | 2 | no compile | – |  | ld.parse-error | 10 | $0.33 |
| tank-fill | without | 3 | fail | – | unbound-tag×3 |  | 8 | $0.29 |
| totalizer-st | with | 1 | **good** | 4/4 |  |  | 12 | $0.41 |
| totalizer-st | with | 2 | **good** | 4/4 |  |  | 11 | $0.40 |
| totalizer-st | with | 3 | **good** | 4/4 |  |  | 10 | $0.41 |
| totalizer-st | without | 1 | fail | – |  |  | 7 | $0.32 |
| totalizer-st | without | 2 | **good** | 4/4 |  |  | 7 | $0.31 |
| totalizer-st | without | 3 | **good** | 4/4 |  |  | 24 | $0.44 |
| traffic-light | with | 1 | **good** | 4/4 |  |  | 7 | $0.43 |
| traffic-light | with | 2 | **good** | 4/4 |  |  | 9 | $0.38 |
| traffic-light | with | 3 | **good** | 4/4 |  |  | 13 | $0.44 |
| traffic-light | without | 1 | correct | 4/4 |  | ld.series>6, ld.uncommented-rung | 28 | $0.58 |
| traffic-light | without | 2 | no compile | – |  |  | 11 | $0.36 |
| traffic-light | without | 3 | no compile | – |  |  | 10 | $0.32 |
