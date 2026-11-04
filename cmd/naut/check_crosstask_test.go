package main

import "testing"

// #327: naut check checks one file at a time, yet a reference to another
// task's instance is explained as the runtime explains it — the owner is
// read from the manifest's tasks, ladder inline instances included.
func TestCheckGoldenCrossTaskInstance(t *testing.T) {
	out, code := checkGolden(t, map[string]string{
		"nautilus.yaml": `tasks:
  - program: owner.ld
  - name: alarms
    program: reader.st
tags:
  - { name: Run,  role: state, init: false }
  - { name: Done, role: state, init: false }
`,
		"owner.ld": `PROGRAM Owner
LD
  RUNG timer (* times Run *)
    Run t1:TON(PT := T#5S)
END_LD
END_PROGRAM
`,
		"reader.st": `PROGRAM Reader
Done := t1.Q;
END_PROGRAM
`,
	})
	assertGolden(t, out, code, 1, `reader.st:2:9: undeclared identifier "t1": t1 is a local of program Owner (task main), and one program cannot read another's variables. Tasks share only tags: in Owner, write the value to a tag (an assignment or a coil) and read that tag here
naut check: 2 file(s), 1 with errors`)
}
