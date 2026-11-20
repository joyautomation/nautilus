# Interactive lessons

Each folder is one step of the interactive tutorial: lesson text on the left,
the playground on the right, preloaded with the step's project. The page
at `/learn/interactive/<id>/` and the bundling are `website/scripts/sync-lessons.mjs`
(the playground shell); this folder is the content.

```
website/lessons/<id>/
  lesson.md      frontmatter: title, track, next: <id>|none, open: [file shown first],
                 carry: [files taken from the learner's previous step]; then the text
  start/         the project the learner starts with (compiles; the exercise is not done)
  solution/      the full solution, same file names (Solve loads it)
  check/         hidden test files (*_test.yaml); Check runs them against the learner's project
  variants/      ok-<name>/ and bad-<name>/: other programs (see below)
```

## The rules

1. **Behaviour, not a stopwatch.** A Check must accept every correct answer,
   including ones that differ from the solution in ways the lesson does not
   care about (a 5-scan counter versus a timer: 0.5 s versus 0.6 s). Do not
   assert "at time T the output is X" unless every correct program agrees at
   T. Use `until:` with `hold:` (wait for a state, require it to stay), `scans:`
   for exact scan counts the lesson itself fixes, and plain `given`/`expect`
   tag assertions. Say in the check file's comment what range of answers it
   accepts.
2. **Every lesson has variants.** `variants/ok-*/` holds other *correct*
   programs (at least two for a lesson with more than one natural solution),
   `variants/bad-*/` holds known *wrong* ones (never does it, does it once,
   too fast, too slow, ignores Stop...). Each variant folder holds the files
   that replace the solution's (usually just the program). `go test ./cmd/naut
   -run TestLessonsAreRealExercises` runs them: every `ok-*` must pass the
   Check, every `bad-*` must fail it. A lesson without variants is not done.
3. **The exercise must be real.** The Check passes on `solution/` and fails on
   `start/`; both compile under `naut check`. The same test enforces it.
4. **Human reasons.** Name each test and step for what is wrong
   ("the motor has not started 4.5 s after Start"): Check shows the name
   beside the failure.
5. **Share file names inside a track** (`nautilus.yaml`, `program.ld`) so a
   step's work carries into the next. `carry:` lists the files that come
   from the learner's previous step; the manifest comes from this step's
   `start/` because a step may add a tag.
6. **No `expect: "<ST expression>"` strings** until the playground's test
   runner is confirmed to evaluate them.
