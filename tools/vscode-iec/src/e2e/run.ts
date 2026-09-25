// End-to-end: a real VS Code, launched the way a desktop launcher launches
// it: nothing useful on PATH. Two runs of suite.ts:
//
//   found    HOME holds only ~/go/bin/naut (a plain `go install`), and
//            the extension must find it: diagnostics from the language
//            server, and the diagram editors reach the CLI.
//   missing  no CLI anywhere, and the extension must still activate cleanly
//            and give the diagram editors the actionable message.
//            It also downloads a ladder program that instantiates a block
//            from a ladder LIBRARY (lib/rungs.ld) to a real `naut run`
//            controller: the prelude must carry that block transpiled, which
//            only `naut compose` knows how to do.
//   managed  the CLI only where "nautilus: Install or Update the naut CLI"
//            puts it (the extension's global storage, bin/), and the language
//            server must run from there. The binary is copied in rather than
//            downloaded, so CI never depends on GitHub's API rate limit.
//
// Builds the CLI from this repo with `go build` unless NAUTILUS_E2E_BIN
// names one. Run with `npm run test:e2e` (under xvfb-run on a headless box).

import { execFileSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { runTests } from "@vscode/test-electron";

const extRoot = path.resolve(__dirname, "..", "..");
const repoRoot = path.resolve(extRoot, "..", "..");
const exe = process.platform === "win32" ? "naut.exe" : "naut";

/** A scenario that hasn't finished in three minutes is hung (a healthy one
 * takes under a minute): show what VS Code is doing and fail now, rather
 * than holding the CI runner until the job timeout. */
function withWatchdog(name: string, run: Promise<number>): Promise<number> {
  let timer: NodeJS.Timeout | undefined;
  const watchdog = new Promise<never>((_, reject) => {
    timer = setTimeout(() => {
      try {
        const ps =
          process.platform === "win32"
            ? execFileSync("tasklist", { encoding: "utf8" })
            : execFileSync("ps", ["-axo", "pid,etime,command"], { encoding: "utf8" });
        console.error(ps.split("\n").filter((l) => /code|electron|naut/i.test(l)).join("\n"));
      } catch {
        // The diagnosis is best-effort; the failure below is what matters.
      }
      reject(new Error(`e2e "${name}" hung for 3 minutes`));
    }, 180_000);
  });
  return Promise.race([run, watchdog]).finally(() => clearTimeout(timer));
}

async function main(): Promise<void> {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "nautilus-e2e-"));
  try {
    let bin = process.env.NAUTILUS_E2E_BIN;
    if (!bin) {
      bin = path.join(tmp, exe);
      execFileSync("go", ["build", "-o", bin, "./cmd/naut"], { cwd: repoRoot, stdio: "inherit" });
    }

    const ws = path.join(tmp, "ws");
    fs.mkdirSync(ws);
    fs.writeFileSync(
      path.join(ws, "broken.st"),
      "PROGRAM Broken\nVAR\n  x : INT;\nEND_VAR\nx := ;\nEND_PROGRAM\n"
    );
    // A project for the online-edit case: a ladder program using a block
    // from a ladder library under lib/, on a controller with online edits.
    const port = 20000 + Math.floor(Math.random() * 10000);
    const proj = path.join(ws, "compose");
    fs.mkdirSync(path.join(proj, "lib"), { recursive: true });
    fs.writeFileSync(
      path.join(proj, "lib", "rungs.ld"),
      "FUNCTION_BLOCK PumpSeq\nVAR_INPUT  Start : BOOL; Stop : BOOL; END_VAR\nVAR_OUTPUT Run : BOOL; END_VAR\n" +
        "LD\n  RUNG seal  [ Start | Run ] /Stop ( Run )\nEND_LD\nEND_FUNCTION_BLOCK\n"
    );
    fs.writeFileSync(
      path.join(proj, "main.ld"),
      "PROGRAM Main\nVAR_EXTERNAL\n    Start : BOOL;\n    Stop : BOOL;\n    Run : BOOL;\nEND_VAR\n" +
        "VAR\n    seq : PumpSeq; (* lib/rungs.ld *)\nEND_VAR\nLD\n  RUNG call\n" +
        "    seq:PumpSeq(Start := Start, Stop := Stop, Run => Run)\nEND_LD\nEND_PROGRAM\n"
    );
    fs.writeFileSync(
      path.join(proj, "nautilus.yaml"),
      `name: compose-e2e\nserver:\n  addr: "127.0.0.1:${port}"\n  online-edits: true\n` +
        "tasks:\n  - program: main.ld\n    scan: 100ms\n" +
        "tags:\n  - { name: Start, role: setpoint, init: false }\n  - { name: Stop, role: setpoint, init: false }\n" +
        "  - { name: Run, role: output, init: false }\ndriver:\n  type: memory\n"
    );
    const emptyPath = path.join(tmp, "empty-path");
    fs.mkdirSync(emptyPath);

    for (const expect of ["found", "missing", "managed"]) {
      const home = path.join(tmp, `home-${expect}`);
      fs.mkdirSync(path.join(home, "go", "bin"), { recursive: true });
      if (expect === "found") fs.copyFileSync(bin, path.join(home, "go", "bin", exe));
      const profile = path.join(tmp, `profile-${expect}`);
      const managedBin = path.join(profile, "User", "globalStorage", "joyauto.vscode-iec", "bin");
      if (expect === "managed") {
        fs.mkdirSync(managedBin, { recursive: true });
        fs.copyFileSync(bin, path.join(managedBin, exe));
      }
      console.log(`\n── e2e: CLI ${expect} ──`);
      await withWatchdog(expect, runTests({
        extensionDevelopmentPath: extRoot,
        extensionTestsPath: path.join(__dirname, "suite"),
        // The scratch HOME has no login keychain, and VS Code on macOS asks
        // the keychain for secret storage at startup; the prompt to create
        // one is invisible on CI and blocks the extension host forever.
        // A fresh profile per scenario: the harness default lives under
        // .vscode-test, which CI caches, and a restored profile left the
        // window unresponsive at startup.
        launchArgs: [
          ws,
          "--disable-extensions",
          `--user-data-dir=${profile}`,
          `--extensions-dir=${path.join(tmp, `extensions-${expect}`)}`,
          ...(process.platform === "darwin" ? ["--use-mock-keychain"] : []),
        ],
        extensionTestsEnv: {
          NAUTILUS_E2E_EXPECT: expect,
          NAUTILUS_E2E_MANAGED_BIN: managedBin,
          NAUTILUS_E2E_PORT: String(port),
          HOME: home,
          USERPROFILE: home,
          PATH: emptyPath,
          GOBIN: "",
          GOPATH: "",
          LOCALAPPDATA: path.join(home, "AppData", "Local"),
        },
      }));
    }
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
