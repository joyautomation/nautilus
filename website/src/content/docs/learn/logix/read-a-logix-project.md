---
title: Read a Logix project as text, with no Rockwell software
description: "Turn a Studio 5000 L5X export into a nautilus project of plain files, check it, and run its logic on your computer."
sidebar:
  order: 1
---

Your Logix project lives in an `.ACD`, a binary only Studio 5000 can open.
An **L5X export** is the same project as XML, and `naut` reads it with no
Rockwell software, no licence and no Windows. In this lesson you turn an
export into plain files, look at them, and run the logic on your computer.
About fifteen minutes. You need `naut`
([install](/learn/getting-started/install/)) and any L5X.

Logix stays your controller here. This is a copy to read, diff, test and
review; nothing is written back to a PLC.

## Export from Studio 5000

In Logix Designer, choose **File, then Export** and save an `.L5X`. (If you
only have an `.ACD` on another machine, [Allen-Bradley
Logix](/guides/logix/) shows how to convert it with an agent beside a
licensed Studio 5000.) The output below is from a small project: a receiving
tank and a transfer handshake, one task, one routine.

## Import it

```sh
naut logix import --project line Line.L5X
```

```text
wrote line/MainProgram.ld
wrote line/lib/logix_types.st
wrote line/nautilus.yaml
wrote line/tags/logix.yaml
1 ladder routine, 1 complete; 5 rungs, 5 carried
```

The last line is the report: how many ladder routines it found and how many
rungs it could carry into nautilus. A rung it cannot carry stays in the
program as a comment, and the report lists it, so nothing disappears
silently.

```sh
cd line
naut check
```

```text
naut check: 2 file(s), 0 with errors
```

## Read the ladder

Open `MainProgram.ld`. Each routine became a program; the rungs are the same
rungs, as text:

```text
RUNG r1 (* A request, no fault, and a ready tank starts the accept-handshake timer *)
  Line_Request Line_Ready /Line_Fault AcceptTmr:TON(PT := T#2S)

RUNG r2 (* Accept once the handshake timer has settled *)
  AcceptTmr.Q ( Line_Accept )
```

A contact is the tag's name, `/Line_Fault` is a normally closed contact, and
`( Line_Accept )` is the coil. The `TON` is dropped into the rung, and its
`.DN` bit is `.Q`. The rung comments came across, including the
interlock one of the engineers added after commissioning. The
[Studio 5000 instruction map](/coming-from/studio-5000/) lists every
instruction.

## Run it

```sh
naut run
```

The logic scans on your computer, against an in-memory stand-in for the
field:

```sh
curl -s localhost:8080/api/state | jq -c .tags
```

```text
{"Line_Accept":false,"Line_BatchId":0,"Line_Fault":false,"Line_Ready":true,"Line_Request":false,"Line_Status":{"Alarm":false,"Mode":1},"Line_TankLevel":20}
```

`Line_Ready` is true because the tank level, 20, is below the 80 that rung
`r0` allows. `Line_Request` is an input: it belongs to the field, so a write is refused,
and you hold it with a force (the [dashboard
lesson](/learn/getting-started/dashboard-and-tags/#drive-an-input-with-a-force)
shows how):

```sh
curl -s -X POST localhost:8080/api/forces -d '{"name":"Line_Request","value":true}'
```

The handshake timer starts, and two seconds later `Line_Accept` goes true.
You are watching your Logix program run without a Logix controller.

## Next

[Where things live](/learn/logix/where-things-live/): what each file is,
against the Studio 5000 organizer you know. To write the project back to a
controller, read [Allen-Bradley Logix](/guides/logix/).
