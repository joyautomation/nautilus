## Tests (`*_test.yaml`)

```yaml
tests:
  - name: high-high switch closes the fill valve
    given: { Start: true, LSHH101: true }
    scans: 2
    expect: { XV101_Cmd: false }

  - name: high-high alarm waits its 2 s
    given: { LSHH101: true }
    steps:
      - advance: 1.9s
        expect: { HighHighAlm: false }
      - advance: 0.2s
        expect: { HighHighAlm: true, Horn: true }
```

Time is virtual: `advance: 10s` runs ten seconds exactly, instantly. A step
takes one of `scans`, `advance`, `until`; `expect` checks its end, `always`
every scan, and both take ST expressions (`- E101_Out = 0.0`) too. Test
every timer, permissive and abort path.

A stimulus that must happen N times (a counter, a debounce, a pulse train)
is `repeat: N` on a step, or on a group of `steps:` (`- repeat: 24` with
`steps:` holding the pulse's set and clear); never write the same step out
N times.

Write the tests from the specification, not from the code: every sentence
that says the control sets, clears, holds, waits for or times something
gets a test that checks it, including a command the control must clear
once it has taken it.

## Before you finish

- `naut check`: no errors, no new warnings (the screen builds). `naut test`
  passes.
- New or changed behavior has a test in `*_test.yaml`: every timer,
  permissive and abort path.
- Every rung and network has a comment; every step and transition a name.
- Rungs of about six elements at most, FBD nesting two deep at most, one
  writer per tag, no manifest tag redeclared in `VAR`, no `(* @layout *)`
  added or changed.

