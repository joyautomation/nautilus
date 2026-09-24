// End-to-end: a real VS Code, launched the way a desktop launcher launches
// it: nothing useful on PATH. Two runs of suite.ts:
//
//   found    HOME holds only ~/go/bin/nautilus (a plain `go install`), and
//            the extension must find it: diagnostics from the language
//            server, and the diagram editors reach the CLI.
//   missing  no CLI anywhere, and the extension must still activate cleanly
//            and give the diagram editors the actionable message.
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
const exe = process.platform === "win32" ? "nautilus.exe" : "nautilus";

async function main(): Promise<void> {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "nautilus-e2e-"));
  try {
    let bin = process.env.NAUTILUS_E2E_BIN;
    if (!bin) {
      bin = path.join(tmp, exe);
      execFileSync("go", ["build", "-o", bin, "./cmd/nautilus"], { cwd: repoRoot, stdio: "inherit" });
    }

    const ws = path.join(tmp, "ws");
    fs.mkdirSync(ws);
    fs.writeFileSync(
      path.join(ws, "broken.st"),
      "PROGRAM Broken\nVAR\n  x : INT;\nEND_VAR\nx := ;\nEND_PROGRAM\n"
    );
    const emptyPath = path.join(tmp, "empty-path");
    fs.mkdirSync(emptyPath);

    for (const expect of ["found", "missing"]) {
      const home = path.join(tmp, `home-${expect}`);
      fs.mkdirSync(path.join(home, "go", "bin"), { recursive: true });
      if (expect === "found") fs.copyFileSync(bin, path.join(home, "go", "bin", exe));
      console.log(`\n── e2e: CLI ${expect} ──`);
      await runTests({
        extensionDevelopmentPath: extRoot,
        extensionTestsPath: path.join(__dirname, "suite"),
        launchArgs: [ws, "--disable-extensions"],
        extensionTestsEnv: {
          NAUTILUS_E2E_EXPECT: expect,
          HOME: home,
          USERPROFILE: home,
          PATH: emptyPath,
          GOBIN: "",
          GOPATH: "",
          LOCALAPPDATA: path.join(home, "AppData", "Local"),
        },
      });
    }
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
