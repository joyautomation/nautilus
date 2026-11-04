# AGENTS.md eval: claude, haiku

Started 2026-11-04 01:21 UTC. k = 3 per cell. AGENTS.md sha256 `7280d6a4b760`. naut at `3de610f41b8c`. Limits per run: 600 s, $1.00.

**Good** = compiles, the required language is a task program, every hidden acceptance test passes, `naut check` prints no warning, and no structure rule is broken. The other columns split that verdict: correct (compiles + language + hidden tests), clean (no warnings), structured (no structure rule broken).

## Good, per task

| task | lang | with: good | with: correct · clean · structured | without: good | without: correct · clean · structured |
|---|---|---|---|---|---|
| analog-pid | fbd | **3/3** | 3/3 · 3/3 · 3/3 | **0/3** | 1/3 · 1/3 · 2/3 |
| batch-sfc | sfc | **3/3** | 3/3 · 3/3 · 3/3 | **1/3** | 1/3 · 3/3 · 2/3 |
| conveyor-fbd | fbd | **3/3** | 3/3 · 3/3 · 3/3 | **0/3** | 2/3 · 3/3 · 0/3 |
| motor-seal-in | ld | **3/3** | 3/3 · 3/3 · 3/3 | **1/3** | 1/3 · 1/3 · 3/3 |
| tank-fill | ld | **3/3** | 3/3 · 3/3 · 3/3 | **0/3** | 0/3 · 0/3 · 3/3 |
| totalizer-st | st | **3/3** | 3/3 · 3/3 · 3/3 | **3/3** | 3/3 · 3/3 · 3/3 |
| traffic-light | ld | **3/3** | 3/3 · 3/3 · 3/3 | **0/3** | 3/3 · 3/3 · 0/3 |
| **all** | | **21/21 (100%)** | 100% · 100% · 100% | **5/21 (24%)** | 52% · 67% · 62% |

## Why runs were not Good

Runs showing each cause (a run can show several).

| cause | with | without |
|---|---|---|
| does not compile | 0/21 | 4/21 |
| hidden tests did not run | 0/21 | 5/21 |
| hidden tests fail | 0/21 | 1/21 |
| structure: fbd.logic-outside-network | 0/21 | 4/21 |
| structure: ld.series>6 | 0/21 | 3/21 |
| structure: ld.uncommented-rung | 0/21 | 3/21 |
| structure: sfc.unnamed-transition | 0/21 | 1/21 |
| warning: unbound-tag | 0/21 | 3/21 |
| wrong language: no fbd task | 0/21 | 2/21 |
| wrong language: no ld task | 0/21 | 1/21 |

## How the agent worked

| condition | runs | mean turns | mean cost | mean time | ran `naut check` | ran `naut test` | wrote tests | read AGENTS.md | timed out |
|---|---|---|---|---|---|---|---|---|---|
| with | 21 | 14.2 | $0.06 | 132 s | 81% | 67% | 100% | 0% | 0 |
| without | 21 | 33.0 | $0.12 | 183 s | 71% | 67% | 52% | 0% | 0 |

"read AGENTS.md" counts an explicit Read of the file; Claude Code also loads it through CLAUDE.md's `@AGENTS.md` import without one.

## Runs

| task | cond | # | verdict | hidden | warnings | structure | turns | cost |
|---|---|---|---|---|---|---|---|---|
| analog-pid | with | 1 | **good** | 7/7 |  |  | 16 | $0.03 |
| analog-pid | with | 2 | **good** | 7/7 |  |  | 11 | $0.03 |
| analog-pid | with | 3 | **good** | 7/7 |  |  | 13 | $0.03 |
| analog-pid | without | 1 | correct | 7/7 |  | fbd.logic-outside-network | 44 | $0.07 |
| analog-pid | without | 2 | fail | – | unbound-tag×7 |  | 14 | $0.02 |
| analog-pid | without | 3 | fail | – | unbound-tag×7 |  | 15 | $0.03 |
| batch-sfc | with | 1 | **good** | 6/6 |  |  | 29 | $0.09 |
| batch-sfc | with | 2 | **good** | 6/6 |  |  | 16 | $0.04 |
| batch-sfc | with | 3 | **good** | 6/6 |  |  | 13 | $0.04 |
| batch-sfc | without | 1 | **good** | 6/6 |  |  | 57 | $0.23 |
| batch-sfc | without | 2 | fail | – |  | sfc.unnamed-transition | 42 | $0.29 |
| batch-sfc | without | 3 | fail | – |  |  | 38 | $0.11 |
| conveyor-fbd | with | 1 | **good** | 8/8 |  |  | 9 | $0.03 |
| conveyor-fbd | with | 2 | **good** | 8/8 |  |  | 10 | $0.03 |
| conveyor-fbd | with | 3 | **good** | 8/8 |  |  | 8 | $0.03 |
| conveyor-fbd | without | 1 | fail | 3/8 |  | fbd.logic-outside-network | 83 | $0.48 |
| conveyor-fbd | without | 2 | correct | 8/8 |  | fbd.logic-outside-network | 66 | $0.25 |
| conveyor-fbd | without | 3 | correct | 8/8 |  | fbd.logic-outside-network | 31 | $0.05 |
| motor-seal-in | with | 1 | **good** | 8/8 |  |  | 10 | $0.02 |
| motor-seal-in | with | 2 | **good** | 8/8 |  |  | 9 | $0.03 |
| motor-seal-in | with | 3 | **good** | 8/8 |  |  | 10 | $0.03 |
| motor-seal-in | without | 1 | **good** | 8/8 |  |  | 28 | $0.04 |
| motor-seal-in | without | 2 | no compile | – |  |  | 22 | $0.04 |
| motor-seal-in | without | 3 | fail | – | unbound-tag×2 |  | 17 | $0.03 |
| tank-fill | with | 1 | **good** | 7/7 |  |  | 10 | $0.04 |
| tank-fill | with | 2 | **good** | 7/7 |  |  | 8 | $0.03 |
| tank-fill | with | 3 | **good** | 7/7 |  |  | 12 | $0.04 |
| tank-fill | without | 1 | no compile | – |  |  | 14 | $0.06 |
| tank-fill | without | 2 | no compile | – |  |  | 9 | $0.04 |
| tank-fill | without | 3 | no compile | – |  |  | 15 | $0.05 |
| totalizer-st | with | 1 | **good** | 4/4 |  |  | 11 | $0.03 |
| totalizer-st | with | 2 | **good** | 4/4 |  |  | 20 | $0.04 |
| totalizer-st | with | 3 | **good** | 4/4 |  |  | 15 | $0.03 |
| totalizer-st | without | 1 | **good** | 4/4 |  |  | 9 | $0.02 |
| totalizer-st | without | 2 | **good** | 4/4 |  |  | 30 | $0.05 |
| totalizer-st | without | 3 | **good** | 4/4 |  |  | 13 | $0.03 |
| traffic-light | with | 1 | **good** | 4/4 |  |  | 12 | $0.06 |
| traffic-light | with | 2 | **good** | 4/4 |  |  | 44 | $0.45 |
| traffic-light | with | 3 | **good** | 4/4 |  |  | 12 | $0.09 |
| traffic-light | without | 1 | correct | 4/4 |  | ld.series>6, ld.uncommented-rung | 37 | $0.10 |
| traffic-light | without | 2 | correct | 4/4 |  | ld.series>6, ld.uncommented-rung | 60 | $0.39 |
| traffic-light | without | 3 | correct | 4/4 |  | ld.series>6, ld.uncommented-rung | 49 | $0.19 |
