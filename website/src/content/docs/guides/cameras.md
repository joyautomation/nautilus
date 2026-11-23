---
title: Cameras
description: Show a plant camera next to the equipment it watches. The controller relays it, so the browser never sees the camera's address or password; a still refreshes by default, live video starts on a click.
---

Operators want to see the equipment next to its data: the conveyor discharge,
the top of a bin. A camera is a row in the manifest, bound to a piece of
equipment from `plant.yaml`, and it shows on that equipment's asset page. It is
**optional**: a project with no `cameras:` section has no camera routes and no
camera on any page.

This page describes virtual cameras, JPEG snapshot URLs, motion-JPEG streams,
RTSP cameras (live H.264 video as HLS) and the pictures kept when an alarm
activates. ONVIF cameras and pan and tilt are a later step and are not built yet.

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
| `kind` | `virtual` is drawn by the controller (demos, tests, no hardware). `snapshot` is a JPEG at a URL. `mjpeg` is a motion-JPEG stream at a URL. `rtsp` is an H.264 camera the controller opens over RTSP and plays as HLS (below). `hls` and `onvif` are accepted by the manifest but show no picture yet unless a `snapshot-url` is given. |
| `url` | The stream or still. Never a user name or password in it: the manifest is refused. |
| `snapshot-url` | A JPEG URL the controller fetches for the still, for a kind whose `url` is a stream. |
| `credentials` | `true` when the camera wants a user name and password. They are kept in the secrets file, never in the manifest. |
| `interval` | Seconds between stills on the asset page (default 5, 1 to 3600). |
| `max-viewers` | How many people may watch the camera live at once (default 2, up to 16). |
| `snapshot-on-alarm` | `true` keeps a still when an alarm on the asset's equipment activates (see [Pictures when an alarm activates](#pictures-when-an-alarm-activates)). Needs a still: a `snapshot-url`, or a kind that has one. |
| `snapshot-alarms` | Alarm ids, of any equipment, that also keep a still. |

### Credentials

A camera's user name and password are the secret `camera.<id>` of the
controller's secrets file (the same file the alarm channels use), `user:password`
on standard input:

```
naut alarms secrets set camera.bin-top
```

`naut check` says which cameras need credentials the file does not hold. The
controller reads the file when a picture is asked for, so a changed password
needs no restart. It speaks basic and digest authentication to the camera, and
sends nothing until the camera asks for credentials, then only in the scheme it
asked for. Over plain `http://` that is the password in the clear (basic) or
hashed (digest) on the plant network: `naut check` warns, and a camera that has
`https://` should use it. A URL with a user name, password or token in its query
(`?user=`, `?pwd=`, `?token=`) is refused like one with them in its authority.

## What the controller does

The browser asks the controller for a camera by id and never by address. The
controller fetches only the URLs of the manifest's rows, with the camera's
credentials. Nothing in an answer, an error or a log carries the camera's
address or a credential: a camera that cannot be reached is "the camera did not
answer", with the reason in one word (connection refused, timed out, its host
name does not resolve). A camera that redirects to another host, port or scheme (https to http) is
refused. A snapshot larger than 4 MB, or one that is not a JPEG, is refused. A
motion-JPEG stream that sends a part that is not a JPEG, a malformed part or a
frame larger than 4 MB is ended, and one that sends nothing for 15 seconds is
ended: a camera never holds a place. Errors from the camera's connection are
replaced by fixed words, so a reset connection never tells the browser or the
journal where the camera is.

A still is fetched from the camera at most once a second per camera, however many
people ask, and a camera that did not answer is not asked again for two seconds.

| Route | Answers |
|---|---|
| `GET /api/cameras` | The cameras: id, label, asset, kind, refresh interval, whether live video and a still exist, who is watching, and what the live relay carries now in bits per second. |
| `GET /api/cameras/{id}/snapshot` | A still (`image/jpeg`, never cached). |
| `GET /api/cameras/{id}/stream` | Live motion-JPEG (`multipart/x-mixed-replace`), for `virtual` and `mjpeg` cameras. |

## Who may view

Video of a plant floor shows people, which is more sensitive than a tag value,
and a site often gives a contractor or a remote viewer `read` without wanting
them to watch the floor. So viewing a camera needs the `camera` permission as
well as `read`. Every built-in role that reads holds it (so a project with no
`auth:` block, and every existing role setup, keeps working), and a custom role
takes it away:

```yaml
auth:
  roles:
    contractor:
      inherits: viewer
      deny: [camera]
```

`camera` is **whole-controller**: it is not scoped to an area, so a person
fenced to one area's tags who holds it may view every camera. Neither is `read`
scoped to an area in the auth model; per-area viewing is a separate, bigger
question that is not answered here. A person with `read` and no `camera` sees the
asset's cameras listed with "no permission to view cameras" in place of a
picture, and the controller never contacts the camera for them. A refused live
view is journalled (`camera.view`, refused). A person with no `read`, or no
sign-in on a project with an `auth:` block, is refused by the same gate that
refuses every API read, before the camera is named. `/api/me` lists the
permission only on a project that has cameras.

A live view of a real camera is journalled twice: `camera.view` with `event:
start` when the first frame is sent, and with `event: stop` when it ends, with the
seconds, the bytes it carried and why it ended if it was not the viewer leaving.
A view that could not start (the camera refused the controller) is journalled as
an error. Stills are not journalled (the asset page asks for one every few
seconds, and they are fetched at most once a second), and neither are views of a
virtual camera, which shows nothing of the plant.

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

Three caps bound it: `max-viewers` limits one camera, one person (or token)
may hold at most **two** live streams, and a controller relays at most **four
at once** across every camera. A person over a cap is told which one it is.
A live view also ends after **15 minutes** (the picture stops: click Watch live again), when
the person signs out or may no longer read, and when the browser stops reading
(each write to it has a 10-second limit).

## Live video of an RTSP camera

An `rtsp` camera (`url: rtsp://…` or `rtsps://…`) is opened by the controller when
the first person asks to watch it, and played in the browser as HLS (fragmented MP4).
The controller does not decode or encode video: the camera's **H.264** is repackaged
as it comes, so the cost is mostly copying bytes. A camera that offers another codec
(H.265, MJPEG) is refused with "set its stream to H.264"; most cameras offer a second
stream in H.264, and a low-resolution one is the right one for a slow link.

- **RTSP runs over the control connection (TCP).** A camera that will only send
  video over UDP is refused. The controller opens no UDP port.
- **Credentials** are the secret `camera.<id>` as for any camera, sent only after the
  camera asks, in the scheme it asked for (digest preferred; never basic after digest).
  They are never in an error, a log or an answer, and neither is the camera's address.
- **The stream starts on the first request and stops 30 seconds after the last one.**
  A camera nobody is watching is not being read.
- **The player** is the kit's own (the asset page's "Watch live"): a browser with
  Media Source Extensions (Chrome, Firefox, Edge, Safari on a Mac) plays it through
  them, and measures the bitrate; one without (an iPhone) is given the playlist and
  plays it natively, with the bitrate not measured. The segments are written the way
  ffmpeg's HLS muxer writes them (a `styp` and a `sidx` in front of each `moof`/`mdat`,
  an `iso5` `ftyp`, `EXT-X-INDEPENDENT-SEGMENTS`), which iPhones play. Chrome's own
  native HLS player, which is new, is not relied on: it fails on live playlists,
  ffmpeg's own included (see the maintainers' notes in the PR), so the kit's player
  is used wherever Media Source Extensions exist. An RTSP camera with no
  `snapshot-url` has no still to show: the page says so and offers Watch live.
  The latency is a few segments, about 4 to 8 seconds; this is not a control view.
- **Who may view, the caps and the journal** are the same as for motion-JPEG: a
  viewer is a lease kept alive by the player's requests and dropped 15 seconds
  after the last one, so the per-camera `max-viewers`, two streams a person and
  four across the controller count people watching, and `camera.view` is
  journalled when a person starts and stops. A view ends after 15 minutes, and a
  person who loses read or the `camera` permission is refused at their next
  request (a segment is two seconds).

### What a stream can use

A hostile or broken camera cannot grow the controller's memory. Each stream holds at
most:

| What | Bound |
|---|---|
| Buffers reserved up front (one picture being reassembled, one fragmented NAL unit, frames read before PLAY is answered) | 2 MB + 1 MB + 2 MB = 5 MB |
| Finished segments | 6 segments and 16 MB, the oldest dropped first |
| The segment being built | 4 MB (each picture counted with its bookkeeping) and 1,500 pictures |
| The picture waiting for its duration | 2 MB |
| An RTSP line, header block or body | 8 KB a line, 64 headers, 64 KB of body |

That is **27 MB counted at the very most for one camera's stream**, and in practice the
window of six two-second segments (about 3 MB at 2 Mbit/s) plus the 5 MB reserve. The
count is what the stream holds; for the moment a segment is cut its fragment is
copied once more (up to 4 MB), so the heap can pass the count by that much, and the
garbage collector's slack comes on top.
The 4 MB segment is a whole group of pictures, so it allows 16 Mbit/s at a two-second
keyframe interval, 8 Mbit/s at four seconds and 3 Mbit/s at ten: more than 1080p
needs in H.264. A camera whose groups of pictures do not fit twice in a row ends
the stream with "the camera's keyframes are too far apart for its bitrate: set its
keyframe interval to 2 seconds, or a lower bitrate". A picture over 2 MB (an
uncompressed 4K keyframe is far past what a camera sends) is dropped.

**All live streams share one budget**, 64 MB by default, so a hostile camera on a
small controller cannot make it swap. A stream that would pass it ends with "camera
stream too large for this controller". Change it in the manifest:

```yaml
camera-streams:
  memory-mb: 64     # 16 to 4096
```

At most four cameras' streams are open at once (a stream stays open 30 seconds
after its last viewer, so one person asking for every camera cannot hold them
all), a camera that fails is not redialled for five seconds, and a camera that
sends nothing for 15 seconds, answers PLAY with nothing, or sends a malformed
description ends the stream with a reason.

### Live video over a slow link

Many sites reach their HMI over a radio link of a few hundred kilobits a
second. One live stream can use all of it and starve the tag values. That is why
the page starts with a still and why live video is an explicit click with its
bitrate on screen: it is the operator's choice, at a cost.

Three caps bound it: `max-viewers` limits one camera, one person (or token)
may hold at most **two** live streams, and a controller relays at most **four
at once** across every camera. A person over a cap is told which one it is.
A live view also ends after **15 minutes** (the picture stops: click Watch live again), when
the person signs out or may no longer read, and when the browser stops reading
(each write to it has a 10-second limit).

## Live video of an RTSP camera

An `rtsp` camera (`url: rtsp://…` or `rtsps://…`) is opened by the controller when
the first person asks to watch it, and played in the browser as HLS (fragmented MP4).
The controller does not decode or encode video: the camera's **H.264** is repackaged
as it comes, so the cost is mostly copying bytes. A camera that offers another codec
(H.265, MJPEG) is refused with "set its stream to H.264"; most cameras offer a second
stream in H.264, and a low-resolution one is the right one for a slow link.

- **RTSP runs over the control connection (TCP).** A camera that will only send
  video over UDP is refused. The controller opens no UDP port.
- **Credentials** are the secret `camera.<id>` as for any camera, sent only after the
  camera asks, in the scheme it asked for (digest preferred; never basic after digest).
  They are never in an error, a log or an answer, and neither is the camera's address.
- **The stream starts on the first request and stops 30 seconds after the last one.**
  A camera nobody is watching is not being read.
- **The player** is the kit's own (the asset page's "Watch live"): Safari and iPhone
  play the HLS playlist natively, other browsers through Media Source Extensions.
  The latency is a few segments, about 4 to 8 seconds; this is not a control view.
- **Who may view, the caps and the journal** are the same as for motion-JPEG: a
  viewer is a lease kept alive by the player's requests and dropped 15 seconds
  after the last one, so the per-camera `max-viewers`, two streams a person and
  four across the controller count people watching, and `camera.view` is
  journalled when a person starts and stops. A view ends after 15 minutes, and a
  person who loses read or the `camera` permission is refused at their next
  request (a segment is two seconds).

### What a stream can use

A hostile or broken camera cannot grow the controller's memory. Each stream holds at
most:

| What | Bound |
|---|---|
| Finished segments | 6 segments and 24 MB, the oldest dropped first |
| The segment being built | 8 MB; a run that cannot be cut (a camera that sends no keyframes, or one picture over the bound) is dropped and the stream waits for the next keyframe |
| One picture being reassembled | 4 MB, at most 1,024 NAL units |
| One fragmented NAL unit | 2 MB |
| An RTSP line, header block or body | 8 KB a line, 64 headers, 64 KB of body |

That is **27 MB at the very most for one camera's stream**, and in practice the
window of six two-second segments (about 3 MB at 2 Mbit/s). At most four cameras
are watched at once, and each stream stops 30 seconds after the last request. A
camera that sends nothing for 15 seconds, answers PLAY with nothing, or sends a malformed
description ends the stream with a reason.

### Live video over a slow link

H.264 at even 500 kbit/s is more than a 256 kbit/s radio link carries. Pick the
camera's low-resolution stream and check the bitrate before offering live video
there. The asset page starts with a still and shows the bitrate before live video
runs, as for motion-JPEG.

### What the CPU cost is

Not measured yet: a figure belongs to a measurement on the small ARM controllers,
which has not been taken. Passing H.264 through involves no decoding, but the
controller does read, repackage and serve every byte of each watched stream.

## Pictures when an alarm activates

A camera with `snapshot-on-alarm: true` keeps one still each time an alarm on its
asset's equipment activates (an alarm's `equipment:` key is the plant's equipment
id, case ignored), and `snapshot-alarms: [ids]` adds particular alarms. The
picture is attached to the alarm's occurrence, and the alarm journal shows it
beside the activation.

```yaml
camera-snapshots:        # optional: how long, how many, how much
  keep-days: 30
  max-count: 500
  max-mb: 200
```

- **Only a new activation** keeps a picture: not a condition that stood through a
  restart or a takeover, an acknowledgement or a return to normal.
- **Only the controller that leads** captures. After a takeover, pictures taken
  by the previous leader stay on its box, not on the new leader's.
- **A flood shares a picture.** An alarm that activates within five seconds of
  the camera's last picture, or while a picture of that camera is being fetched,
  is attached to that picture instead of fetching another. At most two captures run at once, and a capture of another camera that finds both
  places taken is skipped and journalled as failed ("captures are busy").
- **Each capture is journalled**, by the system, as `camera.snapshot` (the
  camera, the alarm and its occurrence, the file and its size, or why it failed).
- **Looking at a kept picture is journalled**, as a live view is: `camera.snapshot-view` names the person, the picture and the occurrences it is for, once per person and picture in ten minutes (a journal page loads every thumbnail, and a reload would otherwise write a row for each).
- **No picture leaves the controller in a page.** Pictures are never put in an
  SMS, an email or a push; they are seen in the alarm journal by a person with
  `read` and the `camera` permission, who is refused with "no permission to view
  cameras" otherwise.

### How long they are kept

These are pictures of people, so they are bounded in time as well as in space:

- Nothing is kept longer than `keep-days` (30 by default). The bound holds when
  nothing is being written.
- At most `max-count` pictures (500) and `max-mb` megabytes (200), the oldest out.
- The bytes are also capped at 5% of the state filesystem's free space when the
  controller starts, and **no picture is kept** (the capture is journalled as
  failed, and a line is logged) while less than 5% of the filesystem, or 256 MB,
  is free. Pictures never take the space the retained state and the
  operator-intent log need.

On a small state filesystem (256 MB or less) the floor is always reached, so no
picture is ever kept there: the journal says why (`camera.snapshot`, failed, "nearly
full"), and the fix is a larger filesystem or no `snapshot-on-alarm`.

They live in `snapshots/` of the controller's state directory (mode 0700, the
files 0600), under names the controller makes. To drop them sooner:

```
naut camera snapshots purge --host controller --all
naut camera snapshots purge --host controller --older-than-days 7
```

A purge needs the `config` permission and a fresh password (`NAUTILUS_TOKEN` or a
token always counts as fresh), and is journalled as `camera.snapshot-purge`.

## What is not here yet

- RTSP and HLS cameras: live video of those kinds, and ONVIF discovery and pan
  and tilt, are later steps. Until then they show a still if a `snapshot-url` is
  given.
- The playground draws no camera: the camera relay is controller-only code and
  is not in the browser build.
