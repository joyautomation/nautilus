---
title: Cameras
description: Show a plant camera next to the equipment it watches. The controller relays it, so the browser never sees the camera's address or password; a still refreshes by default, live video starts on a click.
---

Operators want to see the equipment next to its data: the conveyor discharge,
the top of a bin. A camera is a row in the manifest, bound to a piece of
equipment from `plant.yaml`, and it shows on that equipment's asset page. It is
**optional**: a project with no `cameras:` section has no camera routes and no
camera on any page.

This page describes the first step: virtual cameras, JPEG snapshot URLs and
motion-JPEG streams. RTSP and ONVIF cameras (live video as HLS, snapshots on
an alarm, pan and tilt) are later steps and are not built yet.

## The manifest

```yaml
cameras:
  - id: bin-top
    label: Bin top
    asset: BIN-3                  # a plant.yaml equipment id
    kind: snapshot                # virtual | snapshot | mjpeg
    url: http://10.0.0.20/snap.jpg
    credentials: true             # the camera wants a user name and password
    interval: 5                   # seconds between stills on the asset page
    max-viewers: 2                # people watching this camera live at once
```

| Field | Meaning |
|---|---|
| `id` | Letters, digits, `-` and `_`: it is in a URL. |
| `asset` | The `plant.yaml` equipment the camera shows. `naut check` refuses one the plant does not have. |
| `kind` | `virtual` is drawn by the controller (demos, tests, no hardware). `snapshot` is a JPEG at a URL. `mjpeg` is a motion-JPEG stream at a URL. `hls`, `rtsp` and `onvif` are accepted by the manifest but show no picture yet unless a `snapshot-url` is given. |
| `url` | The stream or still. Never a user name or password in it: the manifest is refused. |
| `snapshot-url` | A JPEG URL the controller fetches for the still, for a kind whose `url` is a stream. |
| `credentials` | `true` when the camera wants a user name and password. They are kept in the secrets file, never in the manifest. |
| `interval` | Seconds between stills on the asset page (default 5, 1 to 3600). |
| `max-viewers` | How many people may watch the camera live at once (default 2, up to 16). |

### Credentials

A camera's user name and password are the secret `camera.<id>` of the
controller's secrets file (the same file the alarm channels use), `user:password`
on standard input:

```
naut alarms secrets set camera.bin-top
```

`naut check` says which cameras need credentials the file does not hold. The
controller reads the file when a picture is asked for, so a changed password
needs no restart. It speaks basic and digest authentication to the camera.

## What the controller does

The browser asks the controller for a camera by id and never by address. The
controller fetches only the URLs of the manifest's rows, with the camera's
credentials. Nothing in an answer, an error or a log carries the camera's
address or a credential: a camera that cannot be reached is "the camera did not
answer", with the reason in one word (connection refused, timed out, its host
name does not resolve). A camera that redirects to another host is refused. A
snapshot larger than 4 MB, or one that is not a JPEG, is refused. A motion-JPEG
stream that sends a part that is not a JPEG, or a frame larger than 4 MB, is
ended.

| Route | Answers |
|---|---|
| `GET /api/cameras` | The cameras: id, label, asset, kind, refresh interval, whether live video and a still exist, who is watching, and what the live relay carries now in bits per second. |
| `GET /api/cameras/{id}/snapshot` | A still (`image/jpeg`, never cached). |
| `GET /api/cameras/{id}/stream` | Live motion-JPEG (`multipart/x-mixed-replace`), for `virtual` and `mjpeg` cameras. |

## Who may view

Viewing a camera needs the `read` permission, as the live values it sits beside
do. `read` is whole-controller: it is not scoped to an area, so neither is a
camera. A person with no `read` (or no sign-in, on a project with an `auth:`
block) is refused by the same gate that refuses every API read, before the
camera is named. A permission to view cameras per area is a decision for the
plant's owner, not made here.

A live view is journalled twice: `camera.view` with `event: start` when the
first frame is sent, and with `event: stop` when it ends, with the seconds and
the bytes it carried. A view that could not start (the camera refused the
controller) is journalled as an error. Stills are not journalled: the asset page
asks for one every few seconds.

## On the asset page

The page shows each camera of the equipment as a **still that refreshes** every
`interval` seconds. Live video starts only when a person clicks **Watch live**,
shows the bitrate the relay is carrying, and stops with **Stop**, when the tab is
hidden, and when the page is closed. Stills pause while the tab is hidden. A
refusal (the viewer cap, a camera that does not answer) is shown in the
controller's own words.

### Live video over a slow link

Many sites reach their HMI over a radio link of a few hundred kilobits a
second. One live stream can use all of it and starve the tag values. That is why
the page starts with a still and why live video is an explicit click with its
bitrate on screen: it is the operator's choice, at a cost.

Two caps bound it. `max-viewers` limits one camera, and a controller relays at
most **four live streams at once** across every camera. A person over the cap is
told which one it is.

## What is not here yet

- RTSP and HLS cameras: live video of those kinds, and ONVIF discovery and pan
  and tilt, are later steps. Until then they show a still if a `snapshot-url` is
  given.
- A snapshot stored with an alarm.
- The playground draws no camera: the camera relay is controller-only code and
  is not in the browser build.
