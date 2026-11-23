---
title: Continuous deployment
description: Commit-to-running-controller — every merge that passes its acceptance suites becomes a controller image, rolled out by sha to a redundant pair.
---

`naut new` with the **Kubernetes deploy** feature (or `--deploy`)
scaffolds the whole pipeline:

```
Dockerfile                      the controller image (distroless, ~binary-sized)
deploy/k8s.yaml                 redundant pair: RBAC, Deployment ×2, Service
.github/workflows/deploy.yml    check → test → build → image → (rollout)
```

The chain is the pitch made concrete: a PR that fails its acceptance
suites cannot merge (`ci.yml`); a merge that passes becomes an image
tagged with its **sha**; the rollout step points the Deployment at that
sha. Every controller running on the floor is attributable to one commit,
and `kubectl rollout undo` is a one-command downgrade.

## The image

`naut build` already emits a self-contained controller — runtime plus
project, one static binary — so the Dockerfile is three meaningful lines
on `distroless/static`: no shell, no package manager, CA roots included
for MQTT/TLS. The base has no libc, so the binary must be static: release
CLIs are, and the workflow sets `CGO_ENABLED=0` for its own `go install`.

A controller for another platform builds from any machine: give
`naut build` that platform's `naut` as its runtime. Take it from the same
release as the `naut` you build with, since one checks the project and
the other runs it.

```sh
tar xzf nautilus_<version>_linux_arm64.tar.gz naut
mv naut naut-arm64
naut build -runtime ./naut-arm64 -o my-plant-arm64
# built my-plant-arm64 — … — for linux/arm64
```

The controller is that binary byte for byte with the project appended,
so it runs on an ARM64 IPC and reports that release's version. Pass the
`naut` inside the archive, not the archive itself; `naut build` refuses
anything that is not an executable.

### Build the screens off the box

`naut build` also builds the project's operator screens and ships them inside the
controller, built. The controller serves them at boot and never builds them, and
`naut deploy` builds this way too, so a box you deploy to rebuilds nothing (a controller you hand it with `-controller` is whatever you built, so build that one with `naut build` too).
`naut run` is different: it builds the screens every time it starts. That is about
four seconds on a desktop, and on a small ARM board it can take minutes of every core before
anything is served (still running after a minute and a half on a two-core Cortex-A53
board). On an ARM board `naut run` says so before it starts. Use `naut build` or `naut
deploy` from a faster machine, and run the controller it makes.

## The cluster

`deploy/k8s.yaml` runs **two replicas** with the manifest's `retain: {}`
and `redundancy: {}` sections (scaffolded together): a Lease elects the
scanning leader, setpoints and online edits ride a ConfigMap across
restarts and failovers, and either replica answers the Service — a
standby proxies API traffic to the leader. The RBAC is exactly the verbs
those two objects need, delete not among them. See the
[Redundancy guide](/guides/redundancy/) for how takeover works.

Kill the leader and watch the tank not care:

```sh
kubectl delete pod -l app=<name> --field-selector ... # or just delete the leader
# the standby's next 1s tick acquires the lease; worst case 4s
```

## The rollout

The scaffolded workflow pushes to GHCR out of the box (nothing to
configure — it uses the repo's own `GITHUB_TOKEN`). The final step —
`kubectl set image` by sha + `rollout status` — ships commented, because
it needs one secret only you can add: a namespace-scoped kubeconfig as
`KUBE_CONFIG`. Uncomment it and the loop closes.

Rolling by sha rather than `latest` is deliberate: a sha pins the code,
while a Deployment that says `:latest` redeploys to whatever was pushed most
recently.

## Without a cluster: systemd

On a single industrial PC the controller is a systemd service. Make it
`Type=notify` and give it a watchdog:

```ini
[Service]
Type=notify
ExecStart=/opt/nautilus/current/controller
WorkingDirectory=/var/lib/nautilus
WatchdogSec=10s
Restart=on-failure
RestartSec=2
```

- **Started means scanning.** The controller tells systemd it is ready
  (`READY=1`) once its first scan has completed, or, as a redundant
  replica, once it has decided it is the standby. `systemctl start` and
  `restart` return only then, so a release that cannot start fails the
  command instead of reporting success.
- **A hung scan is restarted.** While the scans complete, the controller
  sends systemd a heartbeat at half of `WatchdogSec`. It stops when a
  scan stops finishing, or when a task stops starting its scans on
  schedule (starved: its slot overdue, a higher-priority thread on its
  core, say). Both are judged by the same check that closes the
  [write gate](/guides/redundancy/#redundancy), about 2 to 4 s in. systemd then kills the process (`SIGABRT`, so the journal holds
  every goroutine's stack) and `Restart=` starts it again.
  `systemctl status` shows why, in its status line: `scans not live: …`.
- **How long to set.** At least 8 s; naut warns below that, where a
  scan that overruns and then finishes could get a healthy controller
  killed. Measured with 10 s: the kill came 10 s after the last
  heartbeat and the restart 1 s after that (`RestartSec=1`).
- A retained value that hangs the scan comes back with the restart, so a
  controller can restart in a loop. systemd's default start limit (five
  starts in 10 s) never catches it, because each round takes the
  watchdog's 10 s and more. To stop after five rounds, widen the window
  in `[Unit]`: `StartLimitIntervalSec=5min` and `StartLimitBurst=5`.
- **One instance per box.** Run by hand next to the service,
  `controller version` (or `-v`, `--version`) prints the naut it was built
  with, and `controller --help` how it is configured; neither starts it,
  and any other argument is refused. A controller binds its tag API's
  address before anything else starts, and one that finds the address
  held (by its own service, say) refuses to start, so two never drive the
  same I/O. That address is its only exclusion, so a built controller
  also refuses to start on port 0 (a new port each start) or on an
  address it cannot bind. (`naut run`, a bench tool, still moves to the
  next free port, or runs without its tag API.)

## To a box over SSH

`naut deploy` installs that service on a Linux box and moves it from one
release to the next, from the project's directory:

```sh
naut deploy install deploy@ipc       # build, install nautilus.service, start
naut deploy update deploy@ipc        # build, swap in, restart; exit 3 if it rolled back
naut deploy rollback deploy@ipc      # back to the release before; again goes forward
naut deploy status deploy@ipc
naut deploy backup deploy@ipc        # retained values -> nautilus-retain-….tgz
naut deploy restore deploy@ipc nautilus-retain-….tgz
```

The box needs systemd, `sh`, coreutils and `tar`, and nothing else. The
SSH user must be root, have `sudo` without a password, or be allowed one
command, [the release helper](#with-sudo-for-one-command-only-the-release-helper). ssh is the
system's, so `~/.ssh/config`, keys and the agent work as usual, and
`NAUT_SSH_OPTS` adds options (`-p 2222 -i ~/.ssh/ipc`).

Where a password can come from decides how ssh signs in:

- **A person at a terminal** gets ssh's own prompts on any box: the
  password, when keys do not get in, and an unknown host key's yes/no.
- **No terminal (CD)** signs in with keys only (`BatchMode`), so a job
  fails in seconds instead of waiting at a prompt nobody will answer.
- **`NAUT_SSH_ASKPASS=<program>`** lets an unattended run use a password:
  the program prints it. `tools/deploy/askpass-bw.sh` reads it from a
  Bitwarden item named by `NAUT_BW_ITEM`, from an already unlocked vault,
  and answers nothing but a password prompt, so make the host key known
  first. The standard `SSH_ASKPASS` with `SSH_ASKPASS_REQUIRE=force`
  works too.

Whenever a password can be asked, ssh asks once per connection, so a
wrong answer is one failed login, not three, on a box that locks an
account out. Each command asks once (one connection serves it all), and
a box that does not answer gives up after 10 seconds.

- **What it builds on.** For a box of another architecture, naut
  downloads its own release for that architecture and checks it against
  the release's `checksums.txt`. A box like the machine it runs on uses
  naut itself. `-runtime vX.Y.Z` or `-runtime <naut>` overrides it.
  Builds are reproducible: the same project on the same naut release
  binary is the same controller, whether a person deploys it or CD does.
  A naut from `go install` is a different binary from the release, so for
  a box like its own machine it builds a different controller. Pass
  `-runtime vX.Y.Z` to match CD.
- **Releases.** Each release is
  `/opt/nautilus/releases/<project>-<git describe>-<sha256:8>`, for
  example `plant-v1.4.0-3f2a91bc`. `current` and `previous` link to two
  of them, and `status` names both. The same binary again is a no-op. The
  same commit built on another naut is a new release.
- **An update that does not start rolls itself back.** That covers a
  release that will not start, one that exits within 3 s, and, under
  `Type=notify`, one whose first scan never finishes. The last waits for
  the 60 s start timeout. In each case `update` puts both links back,
  puts back the retained values the failed release may have saved over,
  restarts the release before it, and exits 3. An `install` that does not
  start leaves nothing installed, so it can simply run again. Updating to
  the release that is already current is a no-op while it runs, and
  starts it if it is down. A retried CD job therefore cannot pass while
  the controller is stopped.
- **Retained values stay where they are.** They live in
  `/var/lib/nautilus`, outside every release, so update and rollback keep
  them. A copy is taken before each update, with the controller stopped,
  and is what a rolled-back update puts back. `restore` stops the controller
  first, because it saves every 2 s and once more on its way out.
- **The unit.** `Type=notify` with `WatchdogSec=10s` and the start limit
  above. It runs as a dynamic user holding only `CAP_SYS_NICE` (for
  `priority:`) and `CAP_NET_BIND_SERVICE` (ports under 1024). A
  controller built on a naut that predates notification gets
  `Type=simple` without a watchdog instead; this is chosen per release.
- Everything is `sync`ed before a link moves. If power is lost
  mid-update, the box boots the old release or the whole new one.

### Over a slow link

A built controller is 55-75 MB, most of it the runtime. An update over a
radio link does not send that again. `naut deploy update` first asks the box
what its current controller is built on, and sends only what the box cannot
rebuild:

| The box's runtime is | Sent |
|---|---|
| the one this was built on | the project alone, compressed (about 1 MB; 72 MB controller of the lift-station example, one rung changed: 1.2 MB, 40 s at 256 kbit/s) |
| another, or there is none (a first install) | the whole controller, compressed |

**Compression.** The upload is xz when the box can unpack it and this machine
has `xz`, else gzip. xz makes a controller about a quarter smaller (a 51 MB
ARMv7 controller: 15.3 MB as xz, 20.6 MB as gzip; at 256 kbit/s, 8 minutes
instead of 10). The box says what it can unpack when naut asks what it has:
`xzcat` (BusyBox boxes such as the PLCnext, which have no `xz`), `xz` or
`unxz`. naut uses preset 6 and the CRC32 check every decoder has, so the box
needs about 10 MB of memory to unpack it, not the 65 MB of the largest preset,
for less than 1% more bytes. The plan line says which and, when it is gzip,
why (`the box cannot unpack xz`, or `xz is not installed here`);
`-compress gzip|xz|auto` overrides. The resume offsets are on the compressed
file, and the SHA-256 checks are on the unpacked bytes, as before.

The box joins the runtime it has with the project, checks the SHA-256 of the
whole file against the one computed here, and only then names it a release,
so a damaged or cut transfer never becomes a release candidate (`update`
only ever acts on a file that matched). A transfer that is cut off resumes
where the box left off: naut retries a few times by itself, and running the
same command again picks up what the box already holds, as long as it builds
the same controller (the same `-controller` file, a pinned `-id`, or a build
that comes out byte for byte the same). A release id names one controller:
the same `-id` with different contents is refused. Rollback is as it was.

Before it sends, `naut deploy` says how much and how long, at 256 kbit/s unless
`-bandwidth 128k` says otherwise. `-limit 128k` holds the send to that rate
(the link carries the site's other traffic too), and it suggests one when a
send would take more than a few minutes. `-dry-run` builds, asks the box and
says all this without sending anything:

```sh
naut deploy update deploy@ipc -dry-run
naut deploy update deploy@ipc -limit 128k
```

A box whose release helper is from before xz is sent gzip until the helper is
installed again once (`naut deploy` says so). A box on the release helper keeps the old, whole-file upload until the helper
is installed again once (`naut deploy helper -user <ssh-user> <dir>`, as root on
the box): `naut deploy` says so, with the size it is about to send.

### Letting operators open the screen

Right after `install`, the controller is reachable only from its own box:
it listens on `server.addr`, which the scaffolds set to `localhost:8080`,
so the screen and the API answer on the box and nowhere else. Three
settings decide what other machines get, and the unit reads the last two
from a file that `naut deploy` never writes:

1. **The address.** Set `server.addr: "0.0.0.0:8080"` in `nautilus.yaml`
   and run `naut deploy update`, or put `NAUTILUS_ADDR=0.0.0.0:8080` in
   `/etc/nautilus/env` (the environment variable wins over the manifest).
2. **The name people type.** By IP address the screen opens. By a name
   (`http://plc1:8080`) the controller answers `403` and says so, because
   a page on another site could point a name it controls at the box. List
   the names you use in `NAUTILUS_ALLOWED_HOSTS` (comma-separated), or set
   a token (next).
3. **A token, once the address leaves the box.** `NAUTILUS_TOKEN` makes
   writes and online edits require it. The editor sends it from its
   `nautilus.token` setting, and `naut pull` from its own
   `NAUTILUS_TOKEN`.

The unit's `EnvironmentFile` is `/etc/nautilus/env`, one `NAME=value` per
line. The install does not create it, nor `/etc/nautilus`, and `update`
and `rollback` leave it alone. Create it once on the box and restart:

```sh
sudo install -d /etc/nautilus
printf 'NAUTILUS_ALLOWED_HOSTS=plc1\nNAUTILUS_TOKEN=%s\n' "$(openssl rand -hex 16)" | sudo tee /etc/nautilus/env >/dev/null
sudo chmod 600 /etc/nautilus/env      # it holds the token
sudo systemctl restart nautilus
```

Other settings that belong there are in the guides that introduce them:
`NAUTILUS_TLS_CERT` and `NAUTILUS_TLS_KEY` ([HTTPS](#https)), the
historian's address, and a redundant pair's node and peer token.

### With sudo for one command only: the release helper

A box whose SSH user should not have unrestricted `sudo` can grant it
one command instead, the way controller vendors' own sudoers drop-ins
do. `naut deploy helper` writes the three files someone with root
installs once:

```sh
naut deploy helper -user deploy ./helper     # naut-deploy-box, nautilus.service, nautilus-deploy
# then, on the box, as root:
install -d -m 0755 /usr/local/sbin /usr/local/share/nautilus
install -m 0755 naut-deploy-box  /usr/local/sbin/naut-deploy-box
install -m 0644 nautilus.service /usr/local/share/nautilus/nautilus.service
visudo -cf nautilus-deploy && install -m 0440 nautilus-deploy /etc/sudoers.d/nautilus-deploy
```

From then on `naut deploy` finds the helper by itself and runs
`sudo -n /usr/local/sbin/naut-deploy-box <command>` instead of a root
shell. The helper is the boundary between that user and root, so:

- it pins its paths, mode, `PATH` and umask itself, takes nothing from
  the environment, and accepts only its own commands and plain release
  names;
- the unit is always its root-owned template, whatever is uploaded;
- `status` does not run the uploaded controller;
- like every mode, it never reads, writes or `chown`s through a link
  in `/var/lib/nautilus` (the controller owns that directory and could
  leave one there), and `restore` reads the backup's two members as
  data rather than unpacking it.

The helper is the on-box half of the `naut` that wrote it. When a
newer `naut` changes that half or the unit, `naut deploy` refuses and
says to install the helper again from `naut deploy helper`, except for
the change that added the slow-link upload: a helper from before it still
works, with the whole-file upload (see [over a slow link](#over-a-slow-link)).

### Without systemd or root: cron

Some controllers run Linux without systemd and give their SSH user no
root: a PLCnext has sysvinit, and `admin` has only an allowlisted
`sudo`. Install once with `-supervisor cron` and a directory that user
can write:

```sh
naut deploy install admin@plc -supervisor cron -root /opt/plcnext/apps/nautilus
naut deploy update admin@plc         # the box remembers cron mode and the root
```

Nothing runs under `sudo`. The layout is the same under `-root`
(`releases/`, `current`, `previous`), with retained values and the log
in `state/` and optional variables in `env`. `env` is read by `sh`, not
systemd, so it is shell syntax: quote a value with spaces
(`NAUTILUS_TOKEN='a b'`). Two lines in that user's crontab run
`keepalive.sh`: every minute, which is what keeps it running, and at
boot, which only makes the first start sooner (some crons ignore
`@reboot`). It starts `current` unless it is already running. Update, rollback, backup and
restore work as above. An update that does not start, or exits within
3 s, rolls back and exits 3; as under `Type=simple`, a first scan that
hangs is not caught. A crash is restarted within a minute rather than
2 s. If `naut-launch` (from the PLCnext I/O release) sits in the root,
the keepalive starts the controller through it, so `priority:` keeps
working across updates without root (see the
[PLCnext guide](/guides/plcnext/#start-on-boot)). Uninstall removes
the crontab lines and what deploy made, and leaves the root and
`naut-launch`.

### Through CD

A deployment repository holds the project and pins the naut version
that builds it. A tag runs `naut deploy update` on a runner that can
reach the box. **GitHub's hosted runners cannot reach a plant LAN**: run a
self-hosted runner on the plant network, or add a step that joins the
runner to a tailnet or VPN that reaches the box.

`naut new <name> --deploy-ssh` writes the workflow,
`.github/workflows/deploy-ssh.yml`. For a repository that already
exists, scaffold a throwaway project with the flag and copy the file
across. On every `v*` tag it:

- fails unless the tag's commit is on the default branch, so a tag pushed
  on an unmerged branch never deploys, and waits for a required reviewer
  of the `plant` environment to approve;
- installs the pinned naut release for the runner's architecture into
  the runner's temp directory (never the checkout, which `naut build`
  would embed in the controller), checked against the release's
  `checksums.txt`. `NAUT_VERSION` is the naut that scaffolded it; a
  development naut leaves a placeholder that fails the job until it is
  set;
- runs `naut check` and `naut test`;
- writes the SSH key to a file only the runner's user can read, and the
  box's host key to its own known-hosts file, both removed when the step
  ends, and runs `naut deploy update` on the box named by the repository
  variable `DEPLOY_TARGET`. ssh accepts only that key and that host key,
  whatever the runner's own ssh configuration says. An update the box
  rolled back fails the job.

The job's token can only read the repository (`permissions: contents:
read`), `actions/checkout` is pinned to a commit and leaves no
credentials in the checkout, and one deploy runs at a time: a newer tag
queued behind a running deploy replaces an older one still waiting.

Its header lists what to set up once: the runner's `plant` label, the
`DEPLOY_TARGET` variable, the `DEPLOY_SSH_KEY` and `DEPLOY_KNOWN_HOSTS`
secrets, and the `plant` environment with its required reviewers. No
address or key goes in the repository.

A person deploying by hand from a clean checkout of the same tag, with
the same naut release, gets the same release: the build embeds the
project directory, so an untracked file in it changes the controller.
Going back is `naut deploy rollback` from either.

`tools/deploy/check.sh <ssh-target>` proves all of this against a real
box. It installs, updates, rolls back, backs up and restores, deploys a
release that cannot start and one that hangs, and asserts after every
step which release answers and that a retained setpoint survived.

## HTTPS

The quickest way to get HTTPS is a certificate the controller makes for itself:

```yaml
server:
  tls: self-signed      # naut auth init writes this
```

- **What it makes.** On its first start, the controller makes an ECDSA P-256 key and a certificate for `localhost`, its loopback addresses, its host name and its network addresses. Both go into one file, `tls/self-signed.pem` in its state directory, mode 0600: `auth.state-dir`, else systemd's `$STATE_DIRECTORY` (`/var/lib/nautilus` under `naut deploy`, so nothing more is needed there), else, in a checkout, `$XDG_STATE_HOME/nautilus/<project>`. It never goes in the project.
- **Its fingerprint.** `naut run` prints the certificate's SHA-256 fingerprint at every start (`naut auth init` prints it too).
- **Browsers warn.** No authority signed the certificate, so a browser shows a warning until you accept it. Before you accept, open the certificate's details from the warning page and compare its SHA-256 fingerprint with the one `naut run` printed. If they match, it's your controller. A script or the historian needs the certificate in its trust store: copy the file's `CERTIFICATE` block.
- **It stays.** Later starts serve the same certificate, so a browser that accepted it keeps accepting it.
- **Renewal.** It lasts 397 days. From 30 days before its expiry, `naut run` makes a new one, with a new fingerprint: at its start, and in a daily check while it runs. It logs the old and the new fingerprint, and the audit journal records the change (`tls.changed`). Only `naut run` renews. `naut logix serve`, `naut siemens serve` and `naut auth init` make one when there is none, but never replace one.
- **Its names are fixed.** The names and addresses the certificate is valid for are set when it's made, and `naut run` lists them at every start. If the controller has an address the certificate lacks, `naut run` names it. If clients reach the controller by that address, delete `tls/self-signed.pem` and restart `naut run` to make a certificate for every current address. The fingerprint changes too.
- **The clock.** A controller whose clock was never set makes a certificate that clients with a correct clock refuse as not yet valid. `naut run` warns when the clock reads earlier than its own build. Set the clock (NTP), delete the file and restart.
- **naut's own clients.** `naut` commands that call a controller (`naut pull`, `naut iolink param`) check its certificate against the system's trust store, and have no option yet to trust one certificate or pin its fingerprint. Add the file's `CERTIFICATE` block to the trust store of the machine they run on, or give the controller a certificate that store already trusts.
- **Not on a redundant pair.** Each controller would make its own certificate, and a standby forwards to the leader only when both serve the same one. The load refuses `self-signed` with `redundancy:`. Make one certificate, copy it to both, and name it as below.
- **Moving to a real certificate.** Replace `tls: self-signed` with `cert` and `key` (below), or set `NAUTILUS_TLS_CERT` and `NAUTILUS_TLS_KEY`, which win over `self-signed` too.

With a certificate you put on the box:

```yaml
server:
  addr: ":8443"
  tls:
    cert: /etc/nautilus/tls/plc.crt   # PEM: the certificate (and chain)
    key:  /etc/nautilus/tls/plc.key   # PEM: its private key
```

- **Where the files go.** Both paths are absolute and outside the project: a private key never belongs in a checkout, a build or git. The controller refuses a key every user on the box may read. A group may read it (the `ssl-cert` convention).
- **Setting them without the manifest.** `NAUTILUS_TLS_CERT` and `NAUTILUS_TLS_KEY` override `server.tls`, for example in `/etc/nautilus/env`.
- **Renewal.** The controller looks at both files at most once a minute and serves a new certificate as soon as it loads; no restart is needed. A renewal it can't load yet (half copied, a key that doesn't match) leaves the old certificate in place until it can. That failure is journalled once (`tls.reload-failed`), and so is the next renewal that loads (`tls.reloaded`). Every change of the certificate served is logged with its new and old fingerprint and journalled (`tls.changed`).
- **Protocol.** TLS 1.2 at least, and 1.3 where the client has it.
- **Clients verify it.** The historian, the VS Code extension and your scripts check the certificate as any HTTPS client does. Use one they trust: from a CA they know, or add your CA to their trust store.

**Under `naut deploy`'s service.** The unit runs as a dynamic user (`DynamicUser=yes`), which can't read a root-owned key in `/etc`. Hand the files to the service as systemd credentials, with a drop-in (`systemctl edit nautilus`):

```ini
[Service]
LoadCredential=tls.crt:/etc/nautilus/tls/plc.crt
LoadCredential=tls.key:/etc/nautilus/tls/plc.key
```

Then point the controller at the copies systemd makes, in `/etc/nautilus/env`:

```sh
NAUTILUS_TLS_CERT=/run/credentials/nautilus.service/tls.crt
NAUTILUS_TLS_KEY=/run/credentials/nautilus.service/tls.key
```

Credentials need a machine or a virtual machine systemd can mount them on: in an unprivileged LXC container `CREDENTIALS_DIRECTORY` stays empty and the controller fails to start with `tls: stat /run/credentials/nautilus.service/tls.crt: permission denied`. Use a virtual machine or a real machine (a privileged container has not been tried).

systemd copies credentials when the service starts, so **under `DynamicUser` a renewal needs a service restart**: the controller's own re-reading never sees the new files. With certbot, restart from the deploy hook:

```sh
certbot renew --deploy-hook "systemctl restart nautilus"
```

**Behind a proxy.** A proxy that terminates TLS and keeps the Host header (`tailscale serve`, nginx) can stand in for `server.tls`. Name it in `server.trusted-proxies`, by address or prefix:

```yaml
server:
  trusted-proxies: ["127.0.0.1"]   # tailscale serve on the same box
```

Only a request from one of those addresses has its `X-Forwarded-Proto: https` believed: that request counts as secure, which is what decides Secure cookies and password sign-in. Its `X-Forwarded-For` is believed too, so the [audit journal](/guides/http-api/) names the client rather than the proxy. The journal takes the right-most address that isn't itself a trusted proxy. With the list empty (the default), neither header is believed.

**A redundant pair.** A standby forwards writes to the leader's tag API, over HTTPS when `server.tls` is set.

- **Which certificate it accepts.** The leader is reached by the address in `peers:`, often an IP the certificate doesn't name, so the standby doesn't check the name. It accepts the leader only if the leader serves the same certificate as this box: the one it serves now, or the one that renewal replaced. So give A and B **the same certificate**.
- **What forwarding covers.** The standby forwards reads and the live stream as well as writes. A pair on different certificates therefore leaves the standby's HMI blank, not only its writes refused.
- **Renewing, with re-reading.** Copy the renewal to both boxes; the standby first is safest. A standby that meets a certificate it doesn't know re-reads its own files at once. So forwarding keeps working whichever box loads the renewal first, as long as the files are on the standby by then.
- **How long the replaced certificate is accepted.** Only until the leader shows the new one, and for a day at most.
- **After an emergency re-key** (a leaked key), restart both boxes: a running controller would otherwise go on accepting the old certificate from its peer for up to that day.
- **Renewing, with restarts** (`DynamicUser`, above). A restart forgets the certificate that was replaced, so between restarting one box and the other, forwarded requests are refused. Restart them one after the other.
- **A pair on different certificates.** The standby refuses to forward (502, "the leader's certificate is not this controller's") and journals `cluster.forward-refused` once, until a forward succeeds again.
- **What the leader sees.** A standby drops any `X-Forwarded-*` header a client wrote itself; it sends its own hop and `X-Forwarded-Proto: https` only when the request reached it over TLS. The leader believes those only if it lists the standby in its `server.trusted-proxies`. Otherwise the journal records the standby's address as the source of a forwarded write.
- **Whether a forwarded request counts as secure.** The leader judges that by the standby-to-leader hop: over `server.tls` that hop is always TLS. So it says nothing about how the *client* connected. So password sign-in, its cookie and `GET /api/me` are decided at the box the client reached, never after a forward: a standby refers sign-in to the leader (see [Users, roles & sign-in](/guides/auth/#a-redundant-pair)).

**`naut logix serve` and `naut siemens serve`** take the project's `server.tls` (or the two variables) the same way, but not `server.trusted-proxies`. Without TLS, listening beyond localhost, they warn that tokens and every value written cross the network in cleartext.

## The artifact carries its own provenance

`naut build` embeds the project's git history in the binary it emits —
`— N commits of program history embedded` on the build line. The deployed
controller then serves `GET /api/program/history` with no git, no `.git`
dir, and no network: which commit it was built from, every change as a
diff, and the ability to warm-swap back to any of them. The loop this page
closes — commit to running controller — gets its mirror: see
[Program history](/guides/program-history/).
