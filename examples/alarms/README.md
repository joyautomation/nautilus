# alarms — the smallest project that is a real alarm system

Two analog points on a UDT, two rules that claim their limit bits, one
hand-written definition for a standalone contact, and an acceptance suite
that walks a five-minute on-delay in a few milliseconds of virtual time.

```sh
nautilus check examples/alarms         # validate the rules, offline
nautilus alarms list examples/alarms   # see what the two rules expanded to
nautilus test examples/alarms          # the suite
nautilus run examples/alarms           # dashboard, /api/alarms, /api/alarms/journal
```

## What is here

| file | what it is |
| --- | --- |
| `blocks.st` | the `AnalogInput` UDT — a library, because it has no `PROGRAM` |
| `program.st` | the bits: limits compared at scan rate. Knows nothing about alarms |
| `sim.st` | the plant, so `nautilus run` shows alarms coming and going |
| `nautilus.yaml` | tags, and the `alarms:` section: rules, defaults, journal, notify |
| `alarms/rtu9.yaml` | the standalone contacts — a generated artifact's shape |
| `alarms_test.yaml` | on-delay, ack, shelve expiry, unshelve, suppression |

## The division of labour

`program.st` computes `FIT_001.HH` by comparing a measurement to a limit.
That is control. Whether a person has to be told about it, and whether
they have seen it yet, is alarm management — and it lives in the host's
alarm engine, which the program has never heard of. Nothing in the ST
changes when you add, remove or re-prioritize an alarm.

## Two rules, five definitions

```yaml
rules:
  - match: { type: AnalogInput, member: HH }
    name: "{desc} High High"
    priority: high
    on-delay: 5m
    enable: "{site}__Online"
```

One rule, both analog points — and it would be the same one rule for two
hundred. `site-from:` pulls `RTU9` out of the tag name by regexp, so
`{site}__Online` resolves to the node's own online flag without a
generator writing that line on every entry.

`nautilus alarms list` prints what they became, which is the whole reason
rules are an acceptable trade:

```
ID                     PRIORITY  SITE  CONDITION              NAME
RTU9_TNK01_LIT_001.HH  high      RTU9  RTU9_TNK01_LIT_001.HH  Tank 1 level High High
RTU9_TNK01_LIT_001.L   medium    RTU9  RTU9_TNK01_LIT_001.L   Tank 1 level Low
RTU9_WEL15_FIT_001.HH  high      RTU9  RTU9_WEL15_FIT_001.HH  Well 15 flow High High
RTU9_WEL15_FIT_001.L   medium    RTU9  RTU9_WEL15_FIT_001.L   Well 15 flow Low
RTU9_WEL15_INT_001_YA  critical  RTU9  RTU9_WEL15_INT_001_YA  RTU 9 Well 15 Intrusion
```

## The suite is the interesting part

An on-delay is the assertion a wall clock makes untestable. Here the alarm
engine reads the same virtual clock the scans run on, so `advance: 4m` is
short of a five-minute delay and `advance: 1m1s` crosses it — exactly, in
microseconds, on every machine:

```yaml
- given: { RTU9_WEL15_FIT_001.PV: 120.0 }
  advance: 4m
  expect: { RTU9_WEL15_FIT_001.HH: true }   # the bit is set
  alarms: { active: [], unacked: 0 }        # the ALARM is not, yet
- advance: 1m1s
  alarms: { active: ["RTU9_WEL15_FIT_001.HH"], unacked: 1 }
```

The last test is the one worth reading twice: taking `RTU9__Online` false
moves the site's alarms to **Suppressed** rather than leaving them frozen
lit. Host tags hold their last values, so without `enable:` a site that
dies with alarms up keeps them up forever — on a screen nobody can do
anything about.

See the [Alarms guide](../../website/src/content/docs/guides/alarms.md).
