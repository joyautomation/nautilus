# Handoff: what does Opto's CODESYS I/O driver actually do? (research, branch `plcnext-groov`)

Question: on a groov EPIC, is there any sanctioned path to the I/O lower
than OptoMMP over loopback? Everything observed says no (brief §2: the
OptoMMP server `mmpServer` is the only process that references and may
open the FPGA I/O device `/dev/fpgaDirectIo`, which is `root:mmp22 0660`;
Opto's PAC engine goes through OptoMMP; Opto documents only OptoMMP for
on-device programs). The one artefact not yet examined is Opto's CODESYS
integration. Reading it settles the question either way. Research only:
no driver work here, and nothing gets installed on the EPIC.

## What to look at

1. **Opto 22 Library Package for CODESYS** — free download at
   opto22.com (support → downloads → "Opto 22 Library Package for CODESYS
   Development System", v3.1.3.0 as of 2026-03). Per developer.opto22.com
   /codesys it contains "device definitions for the EPIC PLC runtime and
   groov I/O modules, an I/O communication driver, and function blocks
   for accessing any OptoMMP memory map address", with auto-detection of
   the chassis. A CODESYS `.package` is a zip: unpack into its own empty
   directory under the scratchpad and inventory it. Libraries come as
   `.library` (source, XML, readable) or `.compiled-library` (binary,
   strings still readable); device descriptions are `.devdesc.xml`
   (readable — they declare the I/O driver and its parameters).
2. **The runtime side**: "CODESYS Control for groov EPIC" is enabled by
   Opto's free `GROOV-LIC-CRE` licence and lives in Opto's firmware
   (`codesys` user exists on the bench EPIC, in group `dialout`). Do not
   enable it on the bench EPIC for this; the question is answerable from
   the package. If the package's device description points at a native
   component (an `.so` name, a "driver" with a localhost address, a
   `DeviceType` with an I/O driver module), that is the finding.
3. Opto's own notes: blog.opto22.com "codesys integration on groov epic"
   and "codesys library package gets new optommp features"; the groov
   EPIC User's Guide (form 2267) CODESYS chapter.

## What to record (brief §2.4, one paragraph, and the memory note)

- Whether the CODESYS I/O driver talks OptoMMP to `localhost:2001` (then
  OptoMMP is confirmed as the floor for everyone, Opto included) or a
  private interface (name it, and whether it is reachable without root
  or the `mmp22` group).
- Anything the package reveals about channel quality / status that the
  OptoMMP driver session (branch `optommp-driver`) should expose.

## Rules

- Downloads are untrusted: each archive in its own new directory, no
  interpreter run from inside it, `python3 -I` for any parsing.
- Reading is fine; do not redistribute Opto's files in the repo. Quote
  file names and short strings only.
- Nothing is installed or changed on the EPIC for this.
