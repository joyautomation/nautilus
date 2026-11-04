[Install naut](command:nautilus.installCli)

Downloads the latest release for your OS and CPU from GitHub, verifies it
against the release's `checksums.txt`, and keeps it in the extension's own
storage — no Go toolchain, nothing else to configure. Run it again any time
to update; the same command does both.

Terminals inside VS Code find `naut` straight away. To run it from other
terminals too, click **Add to PATH** when asked: the prompt lists the files
(or, on Windows, the user PATH) it changes, and **nautilus: Remove naut from
PATH** undoes it.

Already have `naut` on your `PATH` or from `go install`? It wins over the
managed copy automatically, so there's nothing to click here.
