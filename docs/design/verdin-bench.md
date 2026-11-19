# Verdin bench record (#721)

Every measurement behind `website/src/content/docs/guides/verdin.md` and
`docs/claims/verdin.yaml`, with its conditions. One bench unit, measured
2026-11-17 and 2026-11-18.

## The unit

- Toradex Verdin AM62 Dual 1GB IT V1.2A on a Toradex carrier board, wired
  Ethernet, powered from a USB-C PD supply.
- Torizon OS 7.8.0+build.78 ("Torizon OS", no containers pre-provisioned),
  kernel 6.6.157-7.8.0 (`PREEMPT`, not `PREEMPT_RT`), systemd 255,
  Docker 25.0.9.
- 2 × Cortex-A53 at 1.25 GHz, 862 MB RAM; governor `schedutil` unless a
  row says otherwise.
- The deploying machine: amd64 Linux on the same network.

## Deploy (tools/deploy/check.sh)

`NAUT=<naut> tools/deploy/check.sh <user>@<verdin> -runtime <linux_arm64 naut>`,
with the naut of #764 (2026-11-17), of the cron-mode branch
(2026-11-18) and of #808 (2026-11-18), all ALL PASS under `Type=notify`:

| step | #764 | #808 |
|---|---:|---:|
| build + install | 12.2 s | 12.5 s |
| build + update | 7.8 s | 7.8 s |
| rollback | 4.3 s | 4.5 s |
| an update whose first scan hangs, rolled back | 73.1 s | 73.3 s |

## Priority under the unit

`tools/deploy/boot-probe` (task `priority: 80`, `cpu: 1`) installed with
`naut deploy`: `ps -L -o cls,rtprio,psr` shows one controller thread
`FF 80 1`, and the journal has no refusal. With `GOMAXPROCS=4` in
`/etc/nautilus/env`, the controller's environment carries it after a
reboot.

## Jitter (tools/jitter, cross-built for linux/arm64)

"Loaded" is both cores saturated at normal priority: two `yes >/dev/null`
and a loop of `dd if=/dev/zero bs=1M count=64 conv=fsync` to the eMMC.
Only the main (1 ms) task is placed with `-priority`/`-cpu`; the other
tasks run at normal priority. Each run is a transient systemd unit
(`systemd-run`), because Torizon's logind kills a logged-out session's
processes. `late` counts scans later than a tenth of the period.

### Fast tasks alone (`-slow 0 -alloc 0`), 5 min a row

| run | note | task | ran / due | late | missed | p50 | p99 | p99.9 | max |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| A-quiet-normal | fast tasks alone (-slow 0 -alloc 0); quiet, normal priority, unpinned, governor schedutil | main 1 ms | 298868 / 300010 | 27376 | 1132 | 84 µs | 408 µs | 1.41 ms | 19.42 ms |
| A-quiet-normal | fast tasks alone (-slow 0 -alloc 0); quiet, normal priority, unpinned, governor schedutil | fast2 10 ms | 29999 / 30001 | 107 | 1 | 98 µs | 752 µs | 3.26 ms | 11.39 ms |
| A-quiet-normal | | GC | 3 collections | | | p50 262 µs | p99 524 µs | | 524 µs |
| B-loaded-normal | fast tasks alone (-slow 0 -alloc 0); loaded (2x yes + eMMC dd loop), normal priority, unpinned, governor schedutil | main 1 ms | 230321 / 300010 | 105664 | 69680 | 80 µs | 7.04 ms | 12.03 ms | 44.23 ms |
| B-loaded-normal | fast tasks alone (-slow 0 -alloc 0); loaded (2x yes + eMMC dd loop), normal priority, unpinned, governor schedutil | fast2 10 ms | 29949 / 30001 | 2711 | 50 | 108 µs | 4.35 ms | 19.46 ms | 55.43 ms |
| B-loaded-normal | | GC | 2 collections | | | p50 918 µs | p99 2.62 ms | | 2.62 ms |
| C-loaded-fifo-pinned | fast tasks alone (-slow 0 -alloc 0); loaded, SCHED_FIFO 80, main task pinned to CPU 1, governor schedutil | main 1 ms | 287372 / 300009 | 88683 | 12627 | 54 µs | 528 µs | 9.98 ms | 54.76 ms |
| C-loaded-fifo-pinned | fast tasks alone (-slow 0 -alloc 0); loaded, SCHED_FIFO 80, main task pinned to CPU 1, governor schedutil | fast2 10 ms | 29912 / 30000 | 4181 | 87 | 212 µs | 10.50 ms | 20.48 ms | 59.89 ms |
| C-loaded-fifo-pinned | | GC | 3 collections | | | p50 229 µs | p99 393 µs | | 393 µs |
| D-quiet-fifo-pinned | fast tasks alone (-slow 0 -alloc 0); quiet, SCHED_FIFO 80, main task pinned to CPU 1, governor schedutil | main 1 ms | 299898 / 300001 | 11362 | 101 | 26 µs | 172 µs | 320 µs | 11.56 ms |
| D-quiet-fifo-pinned | fast tasks alone (-slow 0 -alloc 0); quiet, SCHED_FIFO 80, main task pinned to CPU 1, governor schedutil | fast2 10 ms | 29998 / 30000 | 158 | 1 | 44 µs | 464 µs | 3.26 ms | 14.14 ms |
| D-quiet-fifo-pinned | | GC | 3 collections | | | p50 197 µs | p99 459 µs | | 459 µs |
| E-loaded-fifo-pinned-perf | fast tasks alone (-slow 0 -alloc 0); loaded, SCHED_FIFO 80, pinned to CPU 1, governor performance | main 1 ms | 297284 / 300010 | 106737 | 2716 | 66 µs | 456 µs | 1.41 ms | 26.94 ms |
| E-loaded-fifo-pinned-perf | fast tasks alone (-slow 0 -alloc 0); loaded, SCHED_FIFO 80, pinned to CPU 1, governor performance | fast2 10 ms | 29951 / 30001 | 3827 | 49 | 200 µs | 5.89 ms | 15.62 ms | 55.65 ms |
| E-loaded-fifo-pinned-perf | | GC | 3 collections | | | p50 197 µs | p99 3.67 ms | | 3.67 ms |

### Full mix, 5 min a row

| run | note | task | ran / due | late | missed | p50 | p99 | p99.9 | max |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| A-quiet-normal | quiet, normal priority, unpinned, governor schedutil | main 1 ms | 285162 / 300011 | 13426 | 14838 | 34 µs | 2.11 ms | 8.96 ms | 24.26 ms |
| A-quiet-normal | quiet, normal priority, unpinned, governor schedutil | fast2 10 ms | 29801 / 30001 | 1439 | 198 | 47 µs | 8.96 ms | 25.09 ms | 28.86 ms |
| A-quiet-normal | quiet, normal priority, unpinned, governor schedutil | slow 100 ms | 2999 / 3000 | 21 | 0 | 100 µs | 7.94 ms | 16.13 ms | 20.33 ms |
| A-quiet-normal | quiet, normal priority, unpinned, governor schedutil | alloc 50 ms | 5999 / 6000 | 50 | 0 | 42 µs | 3.39 ms | 15.10 ms | 23.36 ms |
| A-quiet-normal | | GC | 24 collections | | | p50 328 µs | p99 918 µs | | 918 µs |
| B-loaded-normal | loaded (2x yes + eMMC dd loop), normal priority, unpinned, governor schedutil | main 1 ms | 215708 / 300004 | 87392 | 84295 | 62 µs | 9.47 ms | 22.02 ms | 79.31 ms |
| B-loaded-normal | loaded (2x yes + eMMC dd loop), normal priority, unpinned, governor schedutil | fast2 10 ms | 29594 / 30000 | 4889 | 405 | 110 µs | 16.13 ms | 32.26 ms | 74.48 ms |
| B-loaded-normal | loaded (2x yes + eMMC dd loop), normal priority, unpinned, governor schedutil | slow 100 ms | 2999 / 3000 | 144 | 0 | 384 µs | 20.99 ms | 41.98 ms | 53.85 ms |
| B-loaded-normal | loaded (2x yes + eMMC dd loop), normal priority, unpinned, governor schedutil | alloc 50 ms | 6000 / 6000 | 751 | 0 | 256 µs | 23.55 ms | 47.10 ms | 62.47 ms |
| B-loaded-normal | | GC | 23 collections | | | p50 262 µs | p99 33.55 ms | | 33.55 ms |
| C-loaded-fifo-pinned | loaded, SCHED_FIFO 80, main task pinned to CPU 1, governor schedutil | main 1 ms | 281184 / 300012 | 75348 | 18816 | 51 µs | 1.06 ms | 14.08 ms | 114.24 ms |
| C-loaded-fifo-pinned | loaded, SCHED_FIFO 80, main task pinned to CPU 1, governor schedutil | fast2 10 ms | 29632 / 30001 | 4310 | 367 | 72 µs | 14.59 ms | 32.77 ms | 449.85 ms |
| C-loaded-fifo-pinned | loaded, SCHED_FIFO 80, main task pinned to CPU 1, governor schedutil | slow 100 ms | 2999 / 3000 | 78 | 0 | 240 µs | 17.41 ms | 75.78 ms | 91.31 ms |
| C-loaded-fifo-pinned | loaded, SCHED_FIFO 80, main task pinned to CPU 1, governor schedutil | alloc 50 ms | 5995 / 6000 | 353 | 4 | 252 µs | 19.97 ms | 34.82 ms | 64.33 ms |
| C-loaded-fifo-pinned | | GC | 24 collections | | | p50 262 µs | p99 25.17 ms | | 25.17 ms |
| D-quiet-fifo-pinned | quiet, SCHED_FIFO 80, main task pinned to CPU 1, governor schedutil | main 1 ms | 288996 / 300020 | 11635 | 11015 | 19 µs | 1.06 ms | 9.98 ms | 21.57 ms |
| D-quiet-fifo-pinned | quiet, SCHED_FIFO 80, main task pinned to CPU 1, governor schedutil | fast2 10 ms | 29791 / 30002 | 1513 | 210 | 31 µs | 18.94 ms | 24.06 ms | 32.72 ms |
| D-quiet-fifo-pinned | quiet, SCHED_FIFO 80, main task pinned to CPU 1, governor schedutil | slow 100 ms | 2999 / 3000 | 15 | 0 | 74 µs | 7.17 ms | 18.43 ms | 18.43 ms |
| D-quiet-fifo-pinned | quiet, SCHED_FIFO 80, main task pinned to CPU 1, governor schedutil | alloc 50 ms | 5999 / 6000 | 16 | 0 | 36 µs | 1.63 ms | 11.01 ms | 18.85 ms |
| D-quiet-fifo-pinned | | GC | 24 collections | | | p50 229 µs | p99 2.62 ms | | 2.62 ms |
| E-loaded-fifo-pinned-perf | loaded, SCHED_FIFO 80, pinned to CPU 1, governor performance | main 1 ms | 282630 / 300036 | 67069 | 17381 | 49 µs | 912 µs | 15.36 ms | 380.98 ms |
| E-loaded-fifo-pinned-perf | loaded, SCHED_FIFO 80, pinned to CPU 1, governor performance | fast2 10 ms | 29588 / 30003 | 3811 | 413 | 70 µs | 16.13 ms | 36.86 ms | 105.91 ms |
| E-loaded-fifo-pinned-perf | loaded, SCHED_FIFO 80, pinned to CPU 1, governor performance | slow 100 ms | 2998 / 3000 | 135 | 1 | 118 µs | 21.50 ms | 49.15 ms | 86.70 ms |
| E-loaded-fifo-pinned-perf | loaded, SCHED_FIFO 80, pinned to CPU 1, governor performance | alloc 50 ms | 5992 / 6000 | 523 | 7 | 560 µs | 17.41 ms | 39.94 ms | 64.73 ms |
| E-loaded-fifo-pinned-perf | | GC | 24 collections | | | p50 197 µs | p99 16.78 ms | | 16.78 ms |

### Full mix, FIFO 80 on CPU 1, by GOMAXPROCS, 3 min a row

| run | note | task | ran / due | late | missed | p50 | p99 | p99.9 | max |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| quiet-gmp2 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=2, quiet | main 1 ms | 172532 / 180018 | 5763 | 7469 | 22 µs | 1.02 ms | 9.22 ms | 61.53 ms |
| quiet-gmp2 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=2, quiet | fast2 10 ms | 17885 / 18001 | 1169 | 115 | 33 µs | 13.31 ms | 24.58 ms | 55.31 ms |
| quiet-gmp2 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=2, quiet | slow 100 ms | 1799 / 1800 | 9 | 0 | 70 µs | 9.98 ms | 18.43 ms | 20.78 ms |
| quiet-gmp2 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=2, quiet | alloc 50 ms | 3599 / 3600 | 4 | 0 | 42 µs | 168 µs | 5.76 ms | 22.10 ms |
| quiet-gmp2 | | GC | 14 collections | | | p50 229 µs | p99 2.62 ms | | 2.62 ms |
| quiet-gmp4 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=4, quiet | main 1 ms | 179904 / 180051 | 1436 | 99 | 19 µs | 96 µs | 188 µs | 11.64 ms |
| quiet-gmp4 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=4, quiet | fast2 10 ms | 18000 / 18005 | 135 | 0 | 30 µs | 832 µs | 3.46 ms | 11.78 ms |
| quiet-gmp4 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=4, quiet | slow 100 ms | 1800 / 1800 | 0 | 0 | 50 µs | 1.25 ms | 3.71 ms | 4.11 ms |
| quiet-gmp4 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=4, quiet | alloc 50 ms | 3600 / 3601 | 3 | 0 | 40 µs | 1.89 ms | 4.99 ms | 7.19 ms |
| quiet-gmp4 | | GC | 15 collections | | | p50 262 µs | p99 655 µs | | 655 µs |
| quiet-gmp8 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=8, quiet | main 1 ms | 179662 / 180001 | 1656 | 337 | 19 µs | 100 µs | 216 µs | 60.43 ms |
| quiet-gmp8 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=8, quiet | fast2 10 ms | 17997 / 18000 | 1050 | 3 | 35 µs | 5.25 ms | 5.63 ms | 15.40 ms |
| quiet-gmp8 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=8, quiet | slow 100 ms | 1799 / 1800 | 0 | 0 | 60 µs | 2.30 ms | 5.38 ms | 8.47 ms |
| quiet-gmp8 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=8, quiet | alloc 50 ms | 3599 / 3600 | 1 | 0 | 42 µs | 264 µs | 2.94 ms | 5.71 ms |
| quiet-gmp8 | | GC | 15 collections | | | p50 262 µs | p99 6.29 ms | | 6.29 ms |
| loaded-gmp2 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=2, loaded | main 1 ms | 168776 / 180016 | 42236 | 11218 | 56 µs | 1.44 ms | 14.08 ms | 132.94 ms |
| loaded-gmp2 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=2, loaded | fast2 10 ms | 17804 / 18001 | 2628 | 195 | 88 µs | 15.62 ms | 30.21 ms | 125.60 ms |
| loaded-gmp2 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=2, loaded | slow 100 ms | 1799 / 1800 | 134 | 0 | 180 µs | 17.41 ms | 69.63 ms | 111.88 ms |
| loaded-gmp2 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=2, loaded | alloc 50 ms | 3598 / 3600 | 431 | 1 | 304 µs | 16.13 ms | 40.96 ms | 52.44 ms |
| loaded-gmp2 | | GC | 14 collections | | | p50 229 µs | p99 25.17 ms | | 25.17 ms |
| loaded-gmp4 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=4, loaded | main 1 ms | 175536 / 180004 | 34024 | 4463 | 45 µs | 440 µs | 4.74 ms | 144.89 ms |
| loaded-gmp4 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=4, loaded | fast2 10 ms | 17919 / 18000 | 2127 | 80 | 84 µs | 6.40 ms | 16.13 ms | 136.04 ms |
| loaded-gmp4 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=4, loaded | slow 100 ms | 1798 / 1800 | 55 | 1 | 108 µs | 25.09 ms | 77.82 ms | 107.41 ms |
| loaded-gmp4 | full mix, FIFO 80 on CPU 1, GOMAXPROCS=4, loaded | alloc 50 ms | 3597 / 3600 | 257 | 2 | 384 µs | 14.85 ms | 40.96 ms | 104.72 ms |
| loaded-gmp4 | | GC | 15 collections | | | p50 1.31 ms | p99 25.17 ms | | 25.17 ms |

## Warm reboot to the first good scan (tools/deploy/boottime.py)

`tools/deploy/boot-probe` installed with `naut deploy`; `sudo systemctl
reboot` over SSH; the deploying machine polls SSH and the probe's tag API
every 50 ms. The box's own times are `ExecMainStartTimestampMonotonic` and
`ActiveEnterTimestampMonotonic` of `nautilus.service`, from kernel start.

| run | reboot → SSH | reboot → first good scan | kernel → naut start | kernel → first scan |
|---|---:|---:|---:|---:|
| 1 | 33.3 s | 36.6 s | 20.0 s | 21.2 s |
| 2 | 30.2 s | 35.6 s | 21.0 s | 22.4 s |
| 3 | 31.0 s | 37.3 s | 22.2 s | 24.4 s |
| 4 (`GOMAXPROCS=4`) | 30.1 s | 35.1 s | 20.6 s | 22.0 s |
| 5 (`GOMAXPROCS=4`) | 31.1 s | 36.9 s | 21.6 s | 23.5 s |

`systemd-analyze`: 4.4 s kernel + 27.4 s userspace. `systemd-analyze
critical-chain nautilus.service`: `network-online.target` at 17.7 s,
reached through `NetworkManager-wait-online.service` (+6.7 s).

## Cold power-on to the first good scan (one sample)

2026-11-18, `tools/deploy/boot-probe` installed with `naut deploy`,
`GOMAXPROCS=4` in `/etc/nautilus/env`; the supply switched off and on at
the bench. All times are on the deploying machine's clock (NTP):

- **Power** is the carrier's debug USB (the FT4232H on the Dahlia's supply
  rail): it disconnected when the rail went and enumerated again when it
  came back. It never disconnected through the warm reboots.
- **The box's own times** are `nautilus.service`'s
  `ExecMainStartTimestampMonotonic` and `ActiveEnterTimestampMonotonic`,
  from kernel start. Kernel start on the same clock comes from the box's
  uptime against its NTP clock, offset measured over SSH (±0.3 s).
- **Seen from outside** is the watcher polling SSH and the probe's tag API
  every 50 ms.

| event | clock | from power returned |
|---|---|---:|
| the box stops answering (module off) | 15:55:48.3 | −18.3 s |
| supply rail off (debug USB disconnects) | 15:56:02.727 | −3.9 s |
| supply rail on (debug USB enumerates) | 15:56:06.648 | 0 |
| kernel start | 15:56:18.97 | 12.3 s |
| SSH answers | 15:56:36.3 | 29.6 s |
| `nautilus.service` started (kernel + 19.36 s) | | 31.7 s |
| first scan, `ActiveEnter` (kernel + 20.14 s) | | 32.5 s |
| tag API answers, `Scans > 0` | 15:56:39.4 | 32.8 s |

`systemd-analyze`: 4.5 s kernel + 20.9 s userspace; `network-online.target`
at 14.8 s (`NetworkManager-wait-online` +5.9 s). The module went quiet
14 s before the rail dropped, so the module was switched off first (SW3)
and the supply after. The rail collapsed (the chip on it browned out), so
the SoC came up from a power-on reset. One sample; a second, with the
supply alone off for 10 s, would firm it up.

## The container path

`distroless/static-debian12:nonroot` image of the probe, `docker run
--network host --restart unless-stopped -v nautilus-state:/state`:

- `--cap-add SYS_NICE`: the runtime logs "task scheduling request refused
  … SCHED_FIFO priority 80: operation not permitted"; the task runs at
  normal priority. The kernel has no RT group scheduling
  (`cpu.rt_runtime_us` absent).
- `--ulimit rtprio=80`, no `--cap-add`: `CapEff` 0, user 65532, the task
  thread `FF 80` on CPU 1.
- A setpoint written under image `:1` read the same under `:2`, under `:1`
  again (`docker rm -f` then `docker run`, the value saved > 3 s earlier),
  and after two reboots.
- Reboot → first good scan 52.2 s and 48.1 s; `docker.service` active at
  36.8 s and 36.2 s after kernel start.

## Not measured

A second cold power-on, a `PREEMPT_RT` kernel, `isolcpus`,
every task at a priority, `GOMAXPROCS` in the container, an OS update with
nautilus installed.
