---
title: Not supported, by design
description: What nautilus deliberately does not do, why, and the alternative in each case.
sidebar:
  order: 9
---

These are declines, not gaps. Each has a reason and a way to get the same
result. For things that are missing but wanted, see the "Planned" rows on
the [Coming from…](/coming-from/studio-5000/) pages.

| Not supported | Why | Do this instead |
| --- | --- | --- |
| Ladder `JMP` and `LBL` ([#223](https://github.com/joyautomation/nautilus/issues/223)) | Rungs run top to bottom, every scan. A jump hides which logic ran, and a skipped rung is the same as one with an enabling condition | Put the logic in a `FUNCTION_BLOCK` and call it only when needed, or gate the rungs on a shared condition |
| Ladder `MCR` zones ([#223](https://github.com/joyautomation/nautilus/issues/223)) | A zone changes the meaning of every rung between two markers, which a reader has to hold in mind | A function block with an `Enable` input; see [the pattern](/coming-from/studio-5000/#mcr) |
| A library manager ([#193](https://github.com/joyautomation/nautilus/issues/193)) | Versioned, installable libraries are a package ecosystem; for now the project owns its code in git | Keep shared blocks in `lib/` and copy or vendor them between projects with git |
| Numeric SFC transition priority | Declaration order already defines a single, deterministic outcome | Declare the higher-priority transition first |
| Transition conditions written in LD or FBD | An SFC transition is one ST boolean expression, so a diff reads as text | Put the logic in a block or a ladder program, expose a BOOL, and use it in the transition |
| Action bodies in LD, FBD or IL | Action bodies are ST | Call a function block from the action |
| Vendor instruction sets as the default | The vocabulary is the IEC standard. Vendor idioms come as an opt-in `dialect:` of blocks | `dialect: logix` for [Logix](/coming-from/studio-5000/) (in review, [#225](https://github.com/joyautomation/nautilus/pull/225)); `siemens` and `codesys` are reserved |
| Negated coils `( /Tag )` | One way to say it | An NC contact, or a reset coil |

The language pages keep their own "Not supported" lists for the rest:
[Structured Text](/languages/structured-text/#not-supported),
[Ladder](/languages/ladder/#not-supported),
[FBD](/languages/function-block/#not-supported),
[SFC](/languages/sfc/#not-supported).
