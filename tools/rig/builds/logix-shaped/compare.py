#!/usr/bin/env python3
"""compare.py <built project> <reference project> — does the gesture-built
project match the reference, modulo layout?

Ladder (.ld): per POU, the declarations as a set of (section, name, type)
(order and comments ignored — the declare offer appends, the reference is
grouped by hand), and the rungs in order as (name, header comment, body),
the body with comments dropped and whitespace collapsed (the editor prints
rungs canonically; the reference wraps long calls by hand).
Everything else (the pasted ST, YAML, manifest): byte-identical.

Prints one line per difference and exits 1 if there are any. Differences
listed in ALLOW (name → reason) are printed as "allowed:" and do not fail
— each one is a finding in FINDINGS.md.
"""
import pathlib
import re
import sys

# (file, what) → the finding that explains it
ALLOW = {
}


def strip_block_comments(t):
    return re.sub(r"\(\*.*?\*\)", " ", t, flags=re.S)


def norm(s):
    s = re.sub(r"\s+", " ", s).strip()
    s = re.sub(r"\s*([(),\[\]|])\s*", r"\1", s)
    return s


def parse_ld(text):
    pous = {}
    for m in re.finditer(r"^(PROGRAM|FUNCTION_BLOCK)\s+(\w+)(.*?)^END_\1\b", text, re.S | re.M):
        kind, name, body = m.group(1), m.group(2), m.group(3)
        decls = set()
        head = body.split("\nLD", 1)[0]
        for sm in re.finditer(r"^\s*(VAR_EXTERNAL|VAR_INPUT|VAR_OUTPUT|VAR_IN_OUT|VAR)\b(.*?)END_VAR", strip_block_comments(head), re.S | re.M):
            sec = sm.group(1)
            for d in sm.group(2).split(";"):
                d = d.strip()
                if not d:
                    continue
                n, _, ty = d.partition(":")
                for nn in n.split(","):
                    decls.add((sec, nn.strip(), re.sub(r"\s+", " ", ty.strip())))
        rungs = []
        ld = re.search(r"^\s*LD\s*$(.*?)^\s*END_LD", body, re.S | re.M)
        if ld:
            src = "\n".join(l for l in ld.group(1).split("\n") if not l.strip().startswith("//"))
            parts = re.split(r"^\s*RUNG\s+", src, flags=re.M)
            for p in parts[1:]:
                rn, _, rest = p.partition("\n") if "\n" in p else (p, "", "")
                hm = re.match(r"(\w+)\s*(\(\*(.*?)\*\))?\s*(.*)$", rn, re.S)
                rname, cmt, inline = hm.group(1), (hm.group(3) or "").strip(), hm.group(4)
                rbody = strip_block_comments(inline + "\n" + rest)
                rungs.append((rname, cmt, norm(rbody)))
        pous[(kind, name)] = (decls, rungs)
    return pous


def main():
    built, ref = map(pathlib.Path, sys.argv[1:3])
    diffs, allowed = [], []

    def diff(f, what, msg):
        key = (f, what)
        if key in ALLOW:
            allowed.append(f"allowed: {f}: {msg} — {ALLOW[key]}")
        else:
            diffs.append(f"{f}: {msg}")

    for rf in sorted(p for p in ref.rglob("*") if p.is_file()):
        rel = rf.relative_to(ref).as_posix()
        bf = built / rel
        if not bf.exists():
            diff(rel, "missing", "missing from the build")
            continue
        if rf.suffix == ".ld":
            rp, bp = parse_ld(rf.read_text()), parse_ld(bf.read_text())
            for pou in sorted(set(rp) | set(bp)):
                if pou not in bp:
                    diff(rel, f"pou {pou[1]}", f"{pou[0]} {pou[1]} missing")
                    continue
                if pou not in rp:
                    diff(rel, f"pou {pou[1]}", f"{pou[0]} {pou[1]} not in the reference")
                    continue
                (rd, rr), (bd, br) = rp[pou], bp[pou]
                for d in sorted(rd - bd):
                    diff(rel, f"decl {d[1]}", f"{pou[1]}: reference declares {d[0]} {d[1]} : {d[2]}, the build does not")
                for d in sorted(bd - rd):
                    diff(rel, f"decl {d[1]}", f"{pou[1]}: build declares {d[0]} {d[1]} : {d[2]}, the reference does not")
                if [r[0] for r in rr] != [r[0] for r in br]:
                    diff(rel, "rung order", f"{pou[1]}: rungs {[r[0] for r in br]} != reference {[r[0] for r in rr]}")
                bmap = {r[0]: r for r in br}
                for name, cmt, body in rr:
                    b = bmap.get(name)
                    if not b:
                        continue
                    if b[2] != body:
                        diff(rel, f"rung {name}", f"{pou[1]}.{name}: '{b[2]}' != reference '{body}'")
                    if b[1] != cmt:
                        diff(rel, f"comment {name}", f"{pou[1]}.{name} comment: '{b[1]}' != reference '{cmt}'")
        elif rf.read_bytes() != bf.read_bytes():
            diff(rel, "bytes", "differs from the reference")

    for a in allowed:
        print(a)
    for d in diffs:
        print(d)
    if not diffs:
        print(f"match: {built} == {ref} modulo layout" + (f" ({len(allowed)} allowed)" if allowed else ""))
    sys.exit(1 if diffs else 0)


if __name__ == "__main__":
    main()
