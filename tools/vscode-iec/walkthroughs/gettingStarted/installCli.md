[Install naut](command:nautilus.installCli)

Downloads the latest release for your OS and CPU from GitHub, verifies it
against the release's `checksums.txt`, and keeps it in the extension's own
storage — no Go toolchain, nothing else to configure. Run it again any time
to update; the same command does both.

Already have `naut` on your `PATH` or from `go install`? It wins over the
managed copy automatically, so there's nothing to click here.
