//! nautilus-fastloop — Phase 3 spike (docs/design/realtime.md).
//!
//! A fixed fast loop in Rust: no garbage collector, one thread pinned to a
//! core under SCHED_FIFO, woken by clock_nanosleep(TIMER_ABSTIME) on an
//! absolute schedule, running a PID on a handful of REAL tags that it
//! exchanges with the Nautilus supervisor through a shared-memory segment
//! (see shm layout below). It reports the same lateness statistics as the
//! Go harness (tools/jitter), so the two sit side by side in one table.
//!
//! The question the spike answers is only "what does the OS give a loop
//! with no runtime underneath it, on this box, with this kernel" — not
//! "can Rust run IEC logic". The logic here is one fixed block.
//!
//! ```text
//! fastloop [--period-us 1000] [--cpu N] [--priority P] [--duration-s 60]
//!          [--shm /dev/shm/nautilus-rt] [--json out.json]
//! ```
//!
//! Shared-memory layout (little-endian, 4096 bytes, both sides agree):
//!
//! ```text
//! 0    magic u64 "NAUTRTSH"   8  version u32 (1)   12 period_ns u32
//! 16   n_in u32  20 n_out u32  (8 and 8 in this spike)
//! 64   in.seq  u32   — seqlock, written by the SUPERVISOR (Go)
//! 72   in.vals [f64; 8]       setpoints / commands → the loop
//! 256  out.seq u32   — seqlock, written by the LOOP (Rust)
//! 264  out.stamp_ns u64  CLOCK_MONOTONIC when written
//! 272  out.scan     u64  the loop's scan counter
//! 280  out.vals [f64; 8]      results → the supervisor
//! ```
//!
//! Seqlock rule: the writer bumps seq to odd, writes, bumps to even; a
//! reader copies between two equal even reads. The loop is the writer of
//! `out` (never blocks) and the reader of `in` (bounded retry: if the
//! supervisor is mid-write for more than a few tries, the loop keeps the
//! last values and counts a `stale` — it never waits on the other side).

use std::env;
use std::fs::OpenOptions;
use std::io::Write;
use std::os::unix::io::AsRawFd;
use std::ptr;
use std::sync::atomic::{fence, AtomicU32, Ordering};

const MAGIC: u64 = 0x4e41_5554_5254_5348; // "NAUTRTSH"
const SHM_SIZE: usize = 4096;
const N_IN: usize = 8;
const N_OUT: usize = 8;
const OFF_IN_SEQ: usize = 64;
const OFF_IN_VALS: usize = 72;
const OFF_OUT_SEQ: usize = 256;
const OFF_OUT_STAMP: usize = 264;
const OFF_OUT_SCAN: usize = 272;
const OFF_OUT_VALS: usize = 280;

// ---- lateness statistics: the same geometry as runtime/lateness.go ----

const FINE_SUB: usize = 32;
const FINE_OCTAVES: usize = 30;
const FINE_BUCKETS: usize = 1 + FINE_OCTAVES * FINE_SUB;
const COARSE_EDGES_US: [f64; 16] = [
    1., 2., 5., 10., 20., 50., 100., 200., 500., 1000., 2000., 5000., 10000., 20000., 50000., 100000.,
];

struct Lateness {
    threshold_us: f64,
    n: u64,
    late: u64,
    overruns: u64,
    missed: u64,
    max_us: f64,
    fine: Vec<u64>,
    coarse: [u64; 17],
}

impl Lateness {
    fn new(threshold_us: f64) -> Self {
        Lateness { threshold_us, n: 0, late: 0, overruns: 0, missed: 0, max_us: 0.0, fine: vec![0; FINE_BUCKETS], coarse: [0; 17] }
    }
    fn fine_index(us: f64) -> usize {
        if us.is_nan() || us < 1.0 {
            return 0;
        }
        if us >= (1u64 << FINE_OCTAVES) as f64 {
            return FINE_BUCKETS - 1;
        }
        let octave = 63 - (us as u64).leading_zeros() as usize;
        let lo = (1u64 << octave) as f64;
        let sub = (((us - lo) / lo) * FINE_SUB as f64) as usize;
        1 + octave * FINE_SUB + sub.min(FINE_SUB - 1)
    }
    fn fine_upper(i: usize) -> f64 {
        if i == 0 {
            return 1.0;
        }
        let i = i - 1;
        let (octave, sub) = (i / FINE_SUB, i % FINE_SUB);
        let lo = (1u64 << octave) as f64;
        lo + lo * (sub + 1) as f64 / FINE_SUB as f64
    }
    fn record(&mut self, late_us: f64, overrun: bool) {
        self.n += 1;
        if late_us > self.max_us {
            self.max_us = late_us;
        }
        if late_us > self.threshold_us {
            self.late += 1;
        }
        if overrun {
            self.overruns += 1;
        }
        self.fine[Self::fine_index(late_us)] += 1;
        let mut c = 0;
        while c < COARSE_EDGES_US.len() && late_us >= COARSE_EDGES_US[c] {
            c += 1;
        }
        self.coarse[c] += 1;
    }
    fn percentiles(&self) -> (f64, f64, f64) {
        if self.n == 0 {
            return (0., 0., 0.);
        }
        let k = |p: f64| ((p * self.n as f64).ceil() as u64).max(1);
        let (k50, k99, k999) = (k(0.5), k(0.99), k(0.999));
        let (mut p50, mut p99, mut p999) = (0., 0., 0.);
        let mut cum = 0u64;
        for (i, c) in self.fine.iter().enumerate() {
            cum += c;
            if p50 == 0. && cum >= k50 {
                p50 = Self::fine_upper(i);
            }
            if p99 == 0. && cum >= k99 {
                p99 = Self::fine_upper(i);
            }
            if p999 == 0. && cum >= k999 {
                p999 = Self::fine_upper(i);
                break;
            }
        }
        (p50, p99, p999)
    }
}

// ---- time and scheduling ----

fn mono_ns() -> i64 {
    let mut ts = libc::timespec { tv_sec: 0, tv_nsec: 0 };
    unsafe { libc::clock_gettime(libc::CLOCK_MONOTONIC, &mut ts) };
    ts.tv_sec * 1_000_000_000 + ts.tv_nsec
}

fn sleep_until(ns: i64) {
    let ts = libc::timespec { tv_sec: (ns / 1_000_000_000) as libc::time_t, tv_nsec: (ns % 1_000_000_000) as libc::c_long };
    loop {
        let r = unsafe { libc::clock_nanosleep(libc::CLOCK_MONOTONIC, libc::TIMER_ABSTIME, &ts, ptr::null_mut()) };
        if r != libc::EINTR {
            return;
        }
    }
}

fn place_thread(cpu: Option<usize>, prio: i32) -> Result<(), String> {
    unsafe {
        // Timer slack to 1 µs, like runtime/sleep_linux.go.
        libc::prctl(libc::PR_SET_TIMERSLACK, 1000usize);
        if let Some(c) = cpu {
            let mut set: libc::cpu_set_t = std::mem::zeroed();
            libc::CPU_SET(c, &mut set);
            if libc::sched_setaffinity(0, std::mem::size_of::<libc::cpu_set_t>(), &set) != 0 {
                return Err(format!("pin to cpu {c}: {}", std::io::Error::last_os_error()));
            }
        }
        if prio > 0 {
            let param = libc::sched_param { sched_priority: prio };
            if libc::sched_setscheduler(0, libc::SCHED_FIFO, &param) != 0 {
                return Err(format!(
                    "SCHED_FIFO {prio}: {} — needs CAP_SYS_NICE (setcap cap_sys_nice+ep on this binary) or an rtprio rlimit",
                    std::io::Error::last_os_error()
                ));
            }
        }
    }
    Ok(())
}

// ---- shared memory ----

struct Shm {
    base: *mut u8,
}

impl Shm {
    fn open(path: &str, period_ns: u32) -> Result<Shm, String> {
        let f = OpenOptions::new().read(true).write(true).create(true).truncate(false).open(path).map_err(|e| format!("{path}: {e}"))?;
        f.set_len(SHM_SIZE as u64).map_err(|e| e.to_string())?;
        let base = unsafe {
            libc::mmap(ptr::null_mut(), SHM_SIZE, libc::PROT_READ | libc::PROT_WRITE, libc::MAP_SHARED, f.as_raw_fd(), 0)
        };
        if base == libc::MAP_FAILED {
            return Err(format!("mmap: {}", std::io::Error::last_os_error()));
        }
        let base = base as *mut u8;
        // Lock it: a page fault inside the loop is a latency spike.
        unsafe { libc::mlock(base as *const libc::c_void, SHM_SIZE) };
        let s = Shm { base };
        unsafe {
            s.put_u64(0, MAGIC);
            s.put_u32(8, 1);
            s.put_u32(12, period_ns);
            s.put_u32(16, N_IN as u32);
            s.put_u32(20, N_OUT as u32);
        }
        Ok(s)
    }
    unsafe fn put_u64(&self, off: usize, v: u64) { ptr::write_volatile(self.base.add(off) as *mut u64, v) }
    unsafe fn put_u32(&self, off: usize, v: u32) { ptr::write_volatile(self.base.add(off) as *mut u32, v) }
    fn seq(&self, off: usize) -> &AtomicU32 { unsafe { &*(self.base.add(off) as *const AtomicU32) } }

    /// Reader side of the `in` seqlock, bounded: Some(values) on a clean
    /// read, None if the supervisor was mid-write every time we looked.
    fn read_in(&self) -> Option<[f64; N_IN]> {
        let seq = self.seq(OFF_IN_SEQ);
        for _ in 0..8 {
            let s1 = seq.load(Ordering::Acquire);
            if s1 & 1 == 1 {
                continue;
            }
            let mut v = [0f64; N_IN];
            for (i, slot) in v.iter_mut().enumerate() {
                *slot = unsafe { ptr::read_volatile(self.base.add(OFF_IN_VALS + 8 * i) as *const f64) };
            }
            fence(Ordering::Acquire);
            if seq.load(Ordering::Acquire) == s1 {
                return Some(v);
            }
        }
        None
    }

    /// Writer side of the `out` seqlock: never blocks, never retries.
    fn write_out(&self, stamp_ns: i64, scan: u64, vals: &[f64; N_OUT]) {
        let seq = self.seq(OFF_OUT_SEQ);
        let s = seq.load(Ordering::Relaxed);
        seq.store(s.wrapping_add(1), Ordering::Release); // odd: writing
        fence(Ordering::Release);
        unsafe {
            self.put_u64(OFF_OUT_STAMP, stamp_ns as u64);
            self.put_u64(OFF_OUT_SCAN, scan);
            for (i, v) in vals.iter().enumerate() {
                ptr::write_volatile(self.base.add(OFF_OUT_VALS + 8 * i) as *mut f64, *v);
            }
        }
        fence(Ordering::Release);
        seq.store(s.wrapping_add(2), Ordering::Release); // even: done
    }
}

// ---- the fixed block: the same PI loop the Go harness's fast task runs ----

struct Pi {
    integral: f64,
}

impl Pi {
    // in: [pv, sp, kp, ki, ...]; out: [cv, err, integral, ...]
    fn step(&mut self, inp: &[f64; N_IN], dt_s: f64, out: &mut [f64; N_OUT]) {
        let (pv, sp, kp, ki) = (inp[0], inp[1], inp[2], inp[3]);
        let err = sp - pv;
        self.integral = (self.integral + ki * err * dt_s).clamp(-100.0, 100.0);
        let cv = (kp * err + self.integral).clamp(0.0, 100.0);
        out[0] = cv;
        out[1] = err;
        out[2] = self.integral;
    }
}

fn main() {
    let mut period_us: u64 = 1000;
    let mut cpu: Option<usize> = None;
    let mut prio: i32 = 0;
    let mut duration_s: u64 = 60;
    let mut shm_path = String::from("/dev/shm/nautilus-rt");
    let mut json_path: Option<String> = None;
    let mut threshold_us: Option<f64> = None;
    let mut args = env::args().skip(1);
    while let Some(a) = args.next() {
        let mut val = || args.next().expect("missing value");
        match a.as_str() {
            "--period-us" => period_us = val().parse().unwrap(),
            "--cpu" => cpu = Some(val().parse().unwrap()),
            "--priority" => prio = val().parse().unwrap(),
            "--duration-s" => duration_s = val().parse().unwrap(),
            "--shm" => shm_path = val(),
            "--json" => json_path = Some(val()),
            "--threshold-us" => threshold_us = Some(val().parse().unwrap()),
            _ => {
                eprintln!("unknown flag {a}");
                std::process::exit(2);
            }
        }
    }
    let period_ns = (period_us * 1000) as i64;
    let threshold_us = threshold_us.unwrap_or(period_us as f64 / 10.0);

    if let Err(e) = place_thread(cpu, prio) {
        // Loud, like the Go runtime: a report that looks pinned when it is
        // not would be worse than no report.
        eprintln!("fastloop: placement refused: {e}");
        std::process::exit(2);
    }
    let shm = match Shm::open(&shm_path, period_ns as u32) {
        Ok(s) => s,
        Err(e) => {
            eprintln!("fastloop: {e}");
            std::process::exit(1);
        }
    };
    // No allocation inside the loop: everything it touches is on the stack
    // or already mapped. mlockall keeps the binary's pages resident too.
    unsafe { libc::mlockall(libc::MCL_CURRENT | libc::MCL_FUTURE) };

    let mut stats = Lateness::new(threshold_us);
    let mut pi = Pi { integral: 0.0 };
    let mut inp = [0f64; N_IN];
    inp[1] = 50.0; // sane defaults until the supervisor writes: sp, kp, ki
    inp[2] = 2.0;
    inp[3] = 0.5;
    let mut out = [0f64; N_OUT];
    let mut stale: u64 = 0;
    let mut scan: u64 = 0;
    let dt_s = period_ns as f64 / 1e9;

    eprintln!(
        "fastloop: {period_us} µs period, cpu {:?}, priority {prio}, {duration_s} s, shm {shm_path}",
        cpu
    );
    let start = mono_ns();
    let end = start + duration_s as i64 * 1_000_000_000;
    let mut n: i64 = 1;
    loop {
        let mut due = start + n * period_ns;
        let now = mono_ns();
        if now > due {
            let k = (now - due) / period_ns;
            if k > 0 {
                n += k;
                stats.missed += k as u64;
                due = start + n * period_ns;
            }
        }
        if now < due {
            sleep_until(due);
        }
        let t0 = mono_ns();
        if t0 > end {
            break;
        }
        // --- the scan ---
        match shm.read_in() {
            Some(v) => inp = v,
            None => stale += 1,
        }
        pi.step(&inp, dt_s, &mut out);
        scan += 1;
        shm.write_out(t0, scan, &out);
        let t1 = mono_ns();
        // ---
        let late_us = (t0 - due).max(0) as f64 / 1e3;
        stats.record(late_us, t1 - t0 > period_ns);
        n += 1;
    }
    let elapsed_s = (mono_ns() - start) as f64 / 1e9;
    let (p50, p99, p999) = stats.percentiles();
    let expected = (elapsed_s * 1e6 / period_us as f64) as u64;
    let pct = if stats.n > 0 { 100.0 * stats.late as f64 / stats.n as f64 } else { 0.0 };
    let fmt_us = |us: f64| if us >= 1000.0 { format!("{:.2} ms", us / 1000.0) } else if us >= 100.0 { format!("{us:.0} µs") } else { format!("{us:.1} µs") };
    println!(
        "| rust fastloop | {period_us} µs | {} / {} | {} ({pct:.2} %, thr {}) | {} | {} | {} | {} | {} | {} |",
        stats.n, expected, stats.late, fmt_us(threshold_us), stats.overruns, stats.missed, fmt_us(p50), fmt_us(p99), fmt_us(p999), fmt_us(stats.max_us)
    );
    println!("stale input reads (supervisor mid-write every try): {stale}");
    if let Some(p) = json_path {
        let coarse: Vec<String> = stats.coarse.iter().map(|c| c.to_string()).collect();
        let hostname = std::fs::read_to_string("/proc/sys/kernel/hostname").unwrap_or_default().trim().to_string();
        let kernel = std::fs::read_to_string("/proc/sys/kernel/osrelease").unwrap_or_default().trim().to_string();
        let json = format!(
            "{{\"tool\":\"nautilus rt/fastloop\",\"hostname\":\"{hostname}\",\"kernel\":\"{kernel}\",\"periodUs\":{period_us},\"cpu\":{},\"priority\":{prio},\"durationS\":{elapsed_s:.1},\"scans\":{},\"expected\":{expected},\"thresholdUs\":{threshold_us},\"late\":{},\"overruns\":{},\"missed\":{},\"stale\":{stale},\"p50Us\":{p50},\"p99Us\":{p99},\"p999Us\":{p999},\"maxUs\":{},\"histogram\":[{}]}}\n",
            cpu.map(|c| c.to_string()).unwrap_or("null".into()), stats.n, stats.late, stats.overruns, stats.missed, stats.max_us, coarse.join(",")
        );
        std::fs::File::create(&p).and_then(|mut f| f.write_all(json.as_bytes())).expect("write json");
    }
}
