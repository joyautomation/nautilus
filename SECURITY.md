# Security policy

nautilus runs control logic for real equipment, so a security bug can be
a safety problem too. Please report one privately, and give us a chance
to fix it before it is public.

## Supported versions

Security fixes go into the **latest minor release** of each part:

- the `naut` CLI and runtime, the drivers and the Go packages, which are
  released together;
- the HMI kit (`@joyautomation/nautilus-hmi`);
- the VS Code extension.

The parts are versioned separately. nautilus is pre-1.0, and older minor
releases are not patched, so upgrade to the latest release to get a
fix.

## How to report

1. **Preferred:** use **Report a vulnerability** on the repository's
   [Security tab](https://github.com/joyautomation/nautilus/security).
   The report stays private between you and the maintainers.
2. **If you can't use GitHub:** email **joyja@joyautomation.com** with
   "nautilus security" in the subject.

Please do not open a public issue, discussion or PR about a
vulnerability until a fix has been released.

## What to include

- The version (`naut version`) and the platform: OS, architecture, and
  the controller hardware if it matters.
- The part affected: a driver, the tag API, the HMI, a deploy path, and
  so on.
- Steps to reproduce: a minimal manifest or program, and the requests
  or traffic that trigger it.
- What an attacker can do with it. Say whether it can change outputs to
  equipment or bypass a write gate, interlock or authentication.
- Any proof of concept, logs or packet captures. Remove addresses,
  hostnames, credentials and plant names from your own systems first.

## What to expect

nautilus is maintained by a small team, so these are aims, not
guarantees:

- We acknowledge a report within a few business days.
- We tell you whether we can reproduce it and how we plan to fix it.
- We credit you in the release notes when the fix ships, unless you'd
  rather not be named.

There is no bug bounty.

Test only against equipment and systems you own or are authorised to
test. Never test against a running plant.
