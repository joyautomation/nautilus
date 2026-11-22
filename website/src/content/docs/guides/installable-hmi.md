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

Push notifications for alarms are a later part of
[#722](https://github.com/joyautomation/nautilus/issues/722).
