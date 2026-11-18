// Package wire is PROFINET IO's encoding, with no I/O: DCP frames and
// blocks, the DCE/RPC connectionless header and PNIO request/response
// bodies, PNIO blocks (AR, IOCR, AlarmCR, ExpectedSubmodule, IODControl,
// I&M), RT_CLASS_1 cyclic frames with their DataStatus, RTA (alarm) PDUs,
// LLDP announcements, and IO data values.
//
// Both the IO device and the IO controller (profinet/device,
// profinet/controller) build on it. Ported from tentacle-next's
// internal/profinet; see docs/design/profinet.md.
package wire
