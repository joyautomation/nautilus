// nautilus_io_shm.h — the exchange page between nautilus_io and the Go
// plcnext driver (`transport: shm`). The one definition of the layout:
// plcnext/shm.go mirrors these numbers and plcnext/shm_test.go reads this
// file and fails if the two disagree.
//
// One 4096-byte POSIX shared-memory file, little-endian (the controller is
// ARMv7 LE), in rt/fastloop's seqlock shape: two blocks, each with ONE
// writer, a sequence word bumped to odd before the stores and to even
// after them; a reader copies between two equal even reads, with bounded
// retries, and nobody ever waits on the other side.
//
//   header          written once by the shim when it opens the page
//   inputs block    written by the SHIM every cycle: the Axioline input
//                   frame, the bytes it wrote to the output frame (its own
//                   view of what the bus got), the bus RUN bit from
//                   DiagVars, the cycle's stamp and lateness statistics
//   outputs block   written by NAUT: the output bytes it owns (from 0),
//                   a stamp and a generation, and the watchdog after which
//                   the shim drives the frame to zero
//
// The shim creates the file (as plcnext_firmware) with mode 0666, the way
// the firmware's own /dev/shm segments are made, so `admin` (naut) maps it
// read-write. naut never creates it: no page means no shim.
//
// Stamps are CLOCK_MONOTONIC nanoseconds, one clock for both processes.
#ifndef NAUTILUS_IO_SHM_H
#define NAUTILUS_IO_SHM_H

#define NIO_SHM_PATH "/dev/shm/nautilus-plcnext-io"
#define NIO_SHM_SIZE 4096
#define NIO_MAGIC 0x4E415554504E494FULL   // "NAUTPNIO"
#define NIO_VERSION 1
#define NIO_FRAME_LEN 512                 // DI4096 / DO4096, the whole BusConductor image

// header
#define NIO_OFF_MAGIC 0        // u64
#define NIO_OFF_VERSION 8      // u32
#define NIO_OFF_PERIOD_NS 12   // u32  the shim's cycle
#define NIO_OFF_FRAME_LEN 16   // u32  bytes of each frame below
#define NIO_OFF_PID 20         // u32  the shim's pid
#define NIO_OFF_STARTED_NS 24  // u64  when the shim opened the page: a new value is a shim restart
#define NIO_OFF_LATE_US 32     // u32  the lateness threshold NIO_OFF_LATE counts against

// inputs block: the shim writes, naut reads
#define NIO_OFF_IN_SEQ 64      // u32  seqlock
#define NIO_OFF_STATUS 68      // u32  NIO_ST_* bits
#define NIO_OFF_STAMP_NS 72    // u64  this cycle (advances every cycle, I/O or not)
#define NIO_OFF_FRAME_NS 80    // u64  the last input-frame read (stops when the I/O does)
#define NIO_OFF_CYCLES 88      // u64  cycle counter
#define NIO_OFF_LATE 96        // u64  cycles later than NIO_OFF_LATE_US
#define NIO_OFF_WORST_US 104   // u32  worst lateness since start
#define NIO_OFF_LAST_US 108    // u32  this cycle's lateness
#define NIO_OFF_APPLIED 112    // u64  the outputs generation last written to the bus frame
#define NIO_OFF_TORN 120       // u64  cycles naut's block was mid-write on every try (last outputs kept)
#define NIO_OFF_INPUTS 128     // [NIO_FRAME_LEN] the Axioline input frame
#define NIO_OFF_ECHO 640       // [NIO_FRAME_LEN] what the shim wrote to the output frame this cycle

// outputs block: naut writes, the shim reads
#define NIO_OFF_OUT_SEQ 2048   // u32  seqlock
#define NIO_OFF_OUT_LEN 2052   // u32  bytes naut owns from offset 0; 0 = never written (frame stays zero)
#define NIO_OFF_OUT_NS 2056    // u64  when naut wrote
#define NIO_OFF_OUT_GEN 2064   // u64  naut's write counter
#define NIO_OFF_WATCHDOG_MS 2072 // u32 outputs older than this are dropped to zero; 0 holds the last
#define NIO_OFF_OUTPUTS 2112   // [NIO_FRAME_LEN] the output bytes

// status bits
#define NIO_ST_PLC 0x01        // the PLC domain is started: the cycle touches the frames
#define NIO_ST_FRAMES 0x02     // 1:IN / 1:OUT acquired
#define NIO_ST_RUN_KNOWN 0x04  // DiagVars was read this cycle
#define NIO_ST_RUN 0x08        // AXIO_DIAG_STATUS_REG_RUN: the bus exchanges process data
#define NIO_ST_FIFO 0x10       // the cycle runs SCHED_FIFO
#define NIO_ST_WATCHDOG 0x20   // naut's outputs went stale: the frame is being driven to zero

#endif
