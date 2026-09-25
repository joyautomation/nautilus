import { test } from "node:test";
import assert from "node:assert/strict";
import { parseGitLog, LOG_FORMAT } from "./gitHistory";

const FS = "\x1f";
const RS = "\x1e";

test("LOG_FORMAT separates five fields with US and records with RS", () => {
  assert.equal(LOG_FORMAT, `%h${FS}%H${FS}%ad${FS}%an${FS}%s${RS}`);
});

test("parseGitLog reads records newest first, with each commit's own path (renames)", () => {
  // Exactly what `git log --follow --name-only --format=LOG_FORMAT` prints:
  // fields, RS, blank line, the path at that commit, blank line.
  const stdout =
    ["78858b9", "78858b9aaaa", "2026-08-18", "James A Joy", "fix: cap the integral"].join(FS) +
    RS +
    "\n\nplant/program.fbd\n\n" +
    ["db554bf", "db554bfbbbb", "2026-08-17", "James A Joy", "test: settled is not done — stay there"].join(FS) +
    RS +
    "\n\nold/program.fbd\n";
  const commits = parseGitLog(stdout);
  assert.equal(commits.length, 2);
  assert.deepEqual(commits[0], {
    short: "78858b9",
    sha: "78858b9aaaa",
    date: "2026-08-18",
    author: "James A Joy",
    subject: "fix: cap the integral",
    path: "plant/program.fbd",
  });
  assert.equal(commits[1].subject, "test: settled is not done — stay there");
  assert.equal(commits[1].path, "old/program.fbd");
});

test("parseGitLog without --name-only output still yields commits, with an empty path", () => {
  const stdout = ["a1", "a1full", "2026-01-01", "x", "s"].join(FS) + RS + "\n";
  const commits = parseGitLog(stdout);
  assert.equal(commits.length, 1);
  assert.equal(commits[0].path, "");
});

test("parseGitLog tolerates empty output and drops malformed records", () => {
  assert.deepEqual(parseGitLog(""), []);
  assert.deepEqual(parseGitLog("\n"), []);
  const bad = ["abc", "abcdef"].join(FS) + RS;
  assert.deepEqual(parseGitLog(bad), []);
});

test("parseGitLog keeps a subject that itself contains the field separator", () => {
  const stdout = ["a1", "a1full", "2026-01-01", "x", `odd${FS}subject`].join(FS) + RS + "\n\nf.ld\n";
  assert.equal(parseGitLog(stdout)[0].subject, `odd${FS}subject`);
  assert.equal(parseGitLog(stdout)[0].path, "f.ld");
});
