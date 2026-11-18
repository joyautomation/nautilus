#!/usr/bin/env python3
"""boottime.py: time a box from a reboot to its controller's first good scan.

    tools/deploy/boottime.py <ssh-target> [-n 3] [-port 18090]

Run it against a box with tools/deploy/boot-probe installed by naut deploy
(its tag API listens on every interface, and Scans counts scans and is not
retained, so Scans > 0 after the reboot means a scan ran). Each round:
`sudo systemctl reboot` over SSH, then, by this machine's clock, when SSH
stops and starts answering, when the tag API answers, and when Scans > 0.
Then the box's own view: kernel and userspace boot time, and when
nautilus.service started and became active (under Type=notify, active
means its first scan completed).

A software reboot is a warm boot: firmware and bootloader run, but the
module stayed powered. A cold power-on adds the power supply and the boot
ROM's first stage, which only a power cycle measures.
"""
import argparse, json, socket, subprocess, sys, time, urllib.request

ap = argparse.ArgumentParser()
ap.add_argument("target")
ap.add_argument("-n", type=int, default=3, help="reboots")
ap.add_argument("-port", type=int, default=18090)
a = ap.parse_args()
host = a.target.split("@")[-1]
ssh = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", a.target]


def tcp(port):
    try:
        with socket.create_connection((host, port), timeout=0.2):
            return True
    except OSError:
        return False


def scans():
    try:
        with urllib.request.urlopen(f"http://{host}:{a.port}/api/state", timeout=0.3) as r:
            return json.load(r)["tags"].get("Scans")
    except Exception:
        return None


def onbox():
    q = ("systemd-analyze 2>/dev/null | head -1; "
         "for p in ExecMainStartTimestampMonotonic ActiveEnterTimestampMonotonic; do "
         "echo \"$p=$(systemctl show -p $p --value nautilus.service)\"; done")
    return subprocess.run(ssh + [q], capture_output=True, text=True).stdout


rows = []
for i in range(1, a.n + 1):
    if scans() is None:
        sys.exit(f"{host}:{a.port} does not answer: install tools/deploy/boot-probe first")
    t0 = time.monotonic()
    subprocess.run(ssh + ["sudo -n systemctl reboot"], capture_output=True)
    marks = {}
    while time.monotonic() - t0 < 300:
        up = tcp(22)
        if not up and "down" not in marks:
            marks["down"] = time.monotonic() - t0
        if "down" in marks:
            if up and "ssh" not in marks:
                marks["ssh"] = time.monotonic() - t0
            s = scans()
            if s is not None and "api" not in marks:
                marks["api"] = time.monotonic() - t0
            if s is not None and s > 0:
                marks["scan"] = time.monotonic() - t0
                break
        time.sleep(0.05)
    if "scan" not in marks:
        sys.exit(f"round {i}: no good scan within 300 s ({marks})")
    time.sleep(2)
    box = onbox()
    mono = dict(l.split("=", 1) for l in box.splitlines() if "=" in l)
    start = int(mono.get("ExecMainStartTimestampMonotonic", 0)) / 1e6
    active = int(mono.get("ActiveEnterTimestampMonotonic", 0)) / 1e6
    print(f"round {i}: reboot -> ssh {marks['ssh']:.1f} s, tag API {marks['api']:.1f} s, "
          f"first good scan {marks['scan']:.1f} s (this machine's clock); "
          f"on the box: naut started {start:.1f} s and first scan {active:.1f} s after the kernel")
    analyze = [l for l in box.splitlines() if l.startswith("Startup finished")]
    print("   ", analyze[0] if analyze else "(systemd-analyze: the boot had not finished yet)")
    rows.append((marks["scan"], start, active))

print(f"\nreboot -> first good scan: {min(r[0] for r in rows):.1f}-{max(r[0] for r in rows):.1f} s; "
      f"kernel -> first scan: {min(r[2] for r in rows):.1f}-{max(r[2] for r in rows):.1f} s; "
      f"naut start -> first scan: {min(r[2]-r[1] for r in rows):.1f}-{max(r[2]-r[1] for r in rows):.1f} s")
