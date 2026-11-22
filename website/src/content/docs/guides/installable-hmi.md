---
title: Install an HMI on a phone or desktop
description: A screen naut builds installs as an app on Android, iOS, Windows and Linux, opens its shell offline and says "not connected" instead of showing old values. Needs HTTPS.
---

An HMI that `naut run` or `naut build` makes from your mimics or your `hmi/`
app is a progressive web app with no extra work: it has a web app manifest,
icons and a service worker. A browser offers to install it, and an installed
copy opens like an app, without the address bar.

## It needs HTTPS

Browsers install a web app, and run its service worker, only from a secure
page: `https://`, or `http://localhost` on the machine itself. A controller
on plain `http://` over the LAN **cannot be installed from a phone**, and
nothing in the HMI can change that. Your ways to HTTPS:

- **A certificate the browser trusts** on the controller: `server.tls` with
  `cert` and `key` (see [HTTPS](/guides/deployment/#https)).
- **A proxy that has one**: `tailscale serve`, or any reverse proxy in front
  of the controller.
- **Not `tls: self-signed`.** Browsers warn about its certificate, and a
  browser refuses to register a service worker on a page whose certificate it
  warns about, even after you click through the warning. A self-signed
  controller works as a web page, not as an installed app, until its
  certificate is added to the device's trust store.

## What installing gets you

- **The shell works offline.** The service worker keeps the app's files, so a
  screen opens with the controller unreachable. The controller's data is
  never kept: `/api/*`, the live stream and the built-in dashboard
  (`/_nautilus/`) always go to the network. Offline, the HMI shows
  **Offline** and *Waiting for the controller…* and no tag value, so an old
  value is never shown as a live one.
- **A new build replaces the old shell.** The worker's version is a hash of
  the files it holds. Redeploy the HMI and the next load updates it.

## Name, color and icon

All optional. In `nautilus.yaml`:

```yaml
pwa:
  name: Grain plant         # the installed app's name (default: the project's name)
  short-name: Grain         # the label under the icon
  theme-color: "#123456"    # browser chrome and splash, #rrggbb
  icon: brand/icon.png      # a square PNG, 512 pixels or more, in the project
```

Without `icon`, naut draws one in the theme color; give one for anything
people will see on a home screen. An `hmi/` app that has its own
`static/manifest.webmanifest` or `static/sw.js` keeps them: naut adds and
links nothing then.

## Installing

- **Android and desktop Chrome or Edge:** the browser's install button in the
  address bar or menu.
- **iOS and iPadOS:** Safari's Share menu, *Add to Home Screen*.
- **Linux and Windows:** Chrome or Edge, as above.

## Notifications: alarms on a phone

An installed HMI can page its person on their phone when an alarm
escalates to them. The controller sends a Web Push through the push service
of the phone's browser (Google, Apple or Mozilla); the page is encrypted to
the phone, so the push service sees only ciphertext and an opaque topic.

**On the controller**, add the channel and a way to sign in (a subscription
belongs to a signed-in person):

```yaml
auth:
  users:
    alice: [engineer]
alarms:
  channels:
    push:
      subject: mailto:ops@example.com   # the push services may reach you here
  escalation:
    - id: wet
      match: { area: wetwell }
      levels:
        - { after: 0s, notify: [alice], via: [push] }
```

The first `naut run` makes the push key (the secret `push.vapid-key` in the
controller's `alarm-secrets.json`). On a redundant pair it makes none: make it
on one box with `naut alarms secrets generate push.vapid-key` and copy
`alarm-secrets.json` to the other, because a phone's subscription is bound to
the key. [Alarms](/guides/alarms/#channels) has the rest: who is paged, how a
subscription ends, and what a held mass revocation is.

**On the phone**, open the HMI over HTTPS, signed in, and go to
**Diagnostics → This device → Turn on notifications**. The built-in HMI
shows that card; in your own `hmi/` app, add `<PushCard />` from the kit.
It says plainly when it cannot:

| The card says | What to do |
|---|---|
| Install and notifications need https | open the controller over https (above) |
| …added to the Home Screen first (iPhone, iPad) | Share, *Add to Home Screen*, open it from there (iOS 16.4 or later) |
| This browser cannot receive push | use Chrome, Edge, Firefox or Safari |
| This controller has no sign-in | add an `auth:` block with users |
| Sign in to be paged | sign in: a subscription belongs to a person |
| …does not send push notifications | add `alarms.channels.push` |
| Notifications are blocked | allow them in the browser's site settings |

Each alarm that pages you shows as `HIGH: <alarm name>` with its state, area
and time. A second page for the same alarm replaces the first. **Tapping it
opens the alarms page** (`/alarms`) and does nothing else: it never
acknowledges. Turn it off from the same card; the list below it removes your
other devices.

What it will not do: it ends with a password change, a removed or disabled
user or a revoked role (sign in and turn it on again), not when you sign out,
and it needs the push service to be reachable from the controller.

**What has been verified:** the controller's half against a fake push service
that decrypts, and the worker and the card in Chromium. Not yet on a real
phone, a real push service, or iOS, Android and Windows. The controller
sends directly (not through `HTTPS_PROXY`) and refuses a push service that
resolves to a private address unless `allow-hosts:` names it.
