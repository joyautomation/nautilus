// Package server exposes a nautilus Runtime over HTTP so HMIs, the VS Code
// extension's inline live values, and any other observer can read (and
// write) the tag store without bespoke wiring:
//
//	GET  /             a self-contained live dashboard (landing page); its
//	                   tag table writes setpoints back through POST /api/tags
//	GET  /api/state    one JSON Frame — the current tag snapshot
//	                   ?tags=glob,glob — only the tags matching these
//	GET  /api/stream   Server-Sent Events; one Frame per broadcast tick
//	                   ?tags=glob,glob — only the tags matching these
//	                   ?delta=1        — after the first frame, send only
//	                                     what changed (see handleStream)
//	                   ?blocks=delta   — and gate the NON-tag blocks the
//	                                     same way (scan/drivers/alarms)
//	                   ?full=1         — never send deltas on this stream
//	GET  /api/parameters  the setpoint parameters: value, manifest init, range, drift state (#718)
//	GET  /api/meta     tag descriptions/units/declared types, I/O binding, scan target
//	POST /api/tags     {"name": "TempSP", "value": 65.0} — write one tag,
//	                   or one member of a struct tag by dotted path:
//	                   {"name": "P101.Drive.Speed", "value": 60.0}, or several
//	                   at once: {"name": "P101", "value": {"Cmd": true}}
//	GET  /api/forces   the force table; POST /api/forces to force a tag,
//	                   DELETE /api/forces/{name}, POST /api/forces/clear
//	GET  /api/sfc      running SFC charts; POST /api/sfc/step and
//	                   /api/sfc/transition jump or fire once (force.go)
//	GET  /api/cluster  this replica's redundancy status (leader.Status JSON)
//	GET  /api/audit    the audit journal's recent entries (server/audit.go):
//	                   every write route above records one per request,
//	                   accepted or refused, before it is answered
//	GET  /assets/…     the dashboard's logo, favicon and brand fonts
//
// The Frame shape is deliberately generic (every tag, plus the scan loop's
// full PLC-style diagnostics) so the hmi kit's frame-generic realtime client
// and the editor tooling share one endpoint. Pure stdlib.
//
// Options.HMI turns the controller into a one-process HMI deploy: a built
// SPA (SvelteKit's `adapter-static` output, or any other static bundle)
// takes over "/" — with SPA-fallback routing — and the built-in dashboard
// above moves to "/_nautilus/" so it stays reachable. See Options.HMI and
// the manifest's `server.hmi`.
//
// Redundancy (Options.Cluster) makes every replica answerable behind a load
// balancer even though only the leader scans: GET /api/cluster always
// answers locally so a dashboard can see each replica's own view, and a
// standby transparently reverse-proxies everything else under /api/ to
// whoever holds the lease, since its own tag store is stale. See
// proxyStandby.
//
// Security is progressive. Every request must be addressed to a host no
// page can rebind (localhost, an IP, an allowed name) or carry the token
// (GuardHost). Writes are same-origin-only by default — enough to stop a
// random browser page from actuating outputs, with zero configuration.
// Set Options.AuthToken to require a token on writes (and allow authorized
// cross-origin writers); see authorizeWrite. Options.Auth (the manifest's
// auth: block) decides every write and read by role (authorize, the read
// gate in readgate.go). GET /healthz and /readyz answer probes without a
// credential.
//
//	srv := server.New(rt)
//	go srv.Run(ctx)                       // broadcast loop
//	http.ListenAndServe(":8080", srv.Handler())
package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/acklink"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/internal/oidcseam"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/notes"
	"github.com/joyautomation/nautilus/runtime"
)

// indexHTML is the built-in landing page: a self-contained live dashboard
// served at "/", so hitting the controller in a browser shows running tags
// and the API surface instead of a bare 404. It is also the zeroth HMI: the
// tag table writes setpoints through POST /api/tags, which is enough to
// commission a loop before anyone has built a real operator screen. It
// offers that only on tags that are neither an Input nor an Output, since
// those two are reclaimed by the driver and the logic within one scan —
// which is why /api/meta's I/O lists are load-bearing for the page, not
// just documentation.
//
//go:embed index.html
var indexHTML []byte

// staticFS carries the page's brand furniture: the nautilus spiral, the
// favicon the docs site uses, and latin subsets of the three brand faces
// (Righteous / Space Grotesk / IBM Plex Mono — see assets/fonts/LICENSE).
// They ride in the binary rather than come off a CDN because a controller
// usually sits on a plant network that can't reach one, and a dashboard that
// silently degrades to Arial on the machine it's supposed to represent is
// not the product. All four faces together are ~65 KB.
//
//go:embed assets
var staticFS embed.FS

// assetTypes maps the extensions under assets/ to their content types. Go's
// mime table doesn't know woff2, and a font served as octet-stream is a
// coin-flip across browsers.
var assetTypes = map[string]string{
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".woff2": "font/woff2",
}

// Frame is one observation of the runtime: the full tag store plus the scan
// loop's diagnostics, timestamped server-side. Scan carries the full
// PLC-style runtime diagnostics (phase breakdown, history, histogram) so a
// diagnostics page needs nothing but the stream — a fresh client gets the
// whole picture from its first frame. Locals are the program's retained VAR
// values (a PI integral, latches, FB instances with their pins) — the watch
// window's view inside the POU, read-only.
//
// # Delta frames
//
// On a delta stream (see handleStream) Tags carries only what CHANGED since
// this client's previous frame, and Seq/Full say which kind of frame this
// is: Full marks a complete snapshot to replace state with, and every other
// frame is a merge.
//
// The NON-tag blocks — Scan, Drivers, Alarms — follow the same rule, for
// the same reason. Measured on the Riverbend WTP host, they are ~18 kB per
// frame (a 55-device driver status alone is ~13 kB), so a client that
// filtered its tags down to nothing still pulled 4.3 MB a minute: a floor
// no amount of tag filtering could get under. On a delta frame each of
// them is present only when it CHANGED (Drivers, Alarms) or when its
// cadence came round (Scan — see Options.DiagnosticsInterval), and ABSENT
// means unchanged, exactly as it does for a tag. A full frame — the first
// one and every resync — always carries all of them, which is what makes
// "absent means unchanged" safe: a client is never more than one resync
// away from a block it can vouch for.
//
// A plain (non-delta) client's frames carry every block every tick, no Seq
// and no Full, byte-for-byte what this server has always sent.
type Frame struct {
	TS     int64          `json:"ts"` // epoch milliseconds
	Scans  uint64         `json:"scans"`
	Tags   map[string]any `json:"tags"`
	Locals map[string]any `json:"locals,omitempty"`
	// Scan is the scan loop's diagnostics. A pointer so a delta frame can
	// omit it: the block is ~5 kB of history rings, and it is nil on the
	// delta frames between diagnostics cadences. Never nil on a full frame
	// or on a plain stream.
	Scan    *runtime.ScanStats `json:"scan,omitempty"`
	Drivers []DriverStatus     `json:"drivers,omitempty"`
	// Quality is the tags whose value is not to be trusted — ONLY the
	// non-Good ones, keyed by tag name, valued with io.Quality's token
	// ("stale" | "bad" | "notConnected" | "substituted"). Absent from the map means Good,
	// which is what keeps a healthy 10,000-tag plant paying nothing for the
	// feature: the field is omitted entirely. A name here need not appear
	// in Tags — "notConnected" is exactly the source that has never
	// delivered a value. See runtime.Runtime.Quality.
	Quality map[string]string `json:"quality,omitempty"`
	// Seq counts the frames sent to ONE client, from 1. Present only on a
	// delta stream, where it is the gap detector: frames are built from a
	// per-client generation and are never silently dropped mid-stream, so a
	// seq that skips means the connection did, and the client should
	// reconnect for a fresh full frame rather than merge into a state it
	// can no longer vouch for.
	Seq uint64 `json:"seq,omitempty"`
	// Full marks a complete snapshot: replace, don't merge. True on a delta
	// stream's first frame, on each periodic resync, and whenever the tag
	// SET changed (a delta cannot express a deletion). Omitted on a plain
	// stream, whose every frame is full by definition.
	Full bool `json:"full,omitempty"`
	// Alarms is the alarm engine's counts — never the rows. Present only
	// when Options.Alarms is set, so a controller without alarms and an
	// HMI built before them see exactly the frame they saw before. Its
	// Rev bumps on any state change, which is how an HMI knows to refetch
	// GET /api/alarms and how it knows not to — and, on a delta stream,
	// what decides whether the block goes on the wire at all: an unchanged
	// Rev is a block the client already holds.
	Alarms *alarm.Summary `json:"alarms,omitempty"`
	// Forces is the force table — forced address → forced value — present
	// on EVERY frame while at least one force is active and absent when
	// none is. Never delta-gated (it is a handful of entries at most), so
	// on any stream "absent" means "nothing is forced". See force.go.
	Forces map[string]any `json:"forces,omitempty"`
	// Config is what runs against the manifest the controller booted
	// from (live I/O configuration, #706): present when the controller
	// has it, on /api/state and every full frame.
	Config *IOConfigState `json:"config,omitempty"`
}

// Options tunes the server; zero values mean defaults.
type Options struct {
	// Interval is the SSE broadcast period. Default 250ms — fast enough
	// for live editor values and HMI needles, slow enough to be negligible
	// load. Snapshots are taken only while at least one client is connected.
	Interval time.Duration

	// ResyncInterval is how often a delta stream sends a full frame anyway
	// (default 30s). Deltas are gap-free by construction — a frame that is
	// not enqueued does not advance the client's generation — so this is
	// not a correctness crutch; it is the bound on how long a client can
	// stay wrong if something outside that argument ever does go wrong (a
	// merge bug, a proxy that rewrites bodies, a client that reloaded its
	// own state from somewhere). Thirty seconds costs a 10,000-tag stream
	// about one full frame per 120 deltas and puts a ceiling on any drift.
	// Negative disables the periodic resync entirely.
	ResyncInterval time.Duration

	// DiagnosticsInterval is how often a DELTA frame carries the scan
	// diagnostics block (default 3s). Unlike the driver status and the
	// alarm counts, the scan block changes every single scan — there is no
	// "unchanged" to gate on — and it is ~5 kB of history rings, so at a
	// 250 ms tick it was 20 kB/s of the stream on its own.
	//
	// A cadence loses nothing as long as it is shorter than the span the
	// rings cover: Recent/Periods hold 180 samples, which is 18 s at a
	// 100 ms scan, so a block every 2 s still delivers every sample a
	// diagnostics page plots — just in batches. The live headline number
	// (Frame.Scans) rides every frame regardless.
	//
	// Zero uses the default; negative sends the block on every frame (what
	// this server did before the cadence existed). A plain, non-delta
	// stream and /api/state are unaffected — they always carry it.
	DiagnosticsInterval time.Duration

	// AuthToken turns on write authentication (progressive enhancement).
	// When empty (the default) nautilus runs unauthenticated: writes are
	// allowed only from same-origin browser pages and non-browser clients
	// (see authorizeWrite). When set, a tag write must present the token in
	// an "Authorization: Bearer <token>" or "X-Nautilus-Token: <token>"
	// header, which also permits authorized cross-origin writers. Any
	// request, read or write, by a host name not in AllowedHosts (nor
	// localhost or an IP) needs the token too (GuardHost).
	AuthToken string

	// AllowedHosts are host names (no port) the server answers by without
	// the token, besides localhost and an IP address (#811, #819): a name
	// an operator really uses for this controller, such as
	// "epic1.plant.lan". A request by any other name is refused unless it
	// carries the token, because a page on an attacker's domain can rebind
	// that name to this machine (DNS rebinding) and is then same-origin
	// with it. NAUTILUS_ALLOWED_HOSTS adds to it in every embedder (New
	// reads it).
	AllowedHosts []string

	// Substitutes are the addresses a person may start a substitute value
	// on (#707), as the manifest's `substitutes:` rules resolved: what
	// GET /api/substitutes lists and POST /api/substitutes holds a start
	// to. Empty: no start is accepted (the runtime has none to offer).
	Substitutes []SubstituteRule
	// TLS, when set (server.tls, #708), is the certificate the tag API
	// serves (the caller wraps its listener in TLS.Config()). On a
	// redundant pair a standby then forwards to the leader over TLS,
	// accepting the leader only if it serves this certificate (current or
	// previous: CertSource.Pinned).
	TLS *CertSource
	// TrustedProxies (server.trusted-proxies) are the addresses whose
	// X-Forwarded-Proto and X-Forwarded-For are believed: a TLS-terminating
	// proxy that keeps the Host (tailscale serve, nginx). Empty: none.
	TrustedProxies []netip.Prefix

	// SubstituteLog, on a redundant pair, sends substitute starts, stops
	// and acknowledgements through the replicated operator-intent log
	// (#707 PR 3b, substitutes_oplog.go). Nil: they apply here and are
	// saved to the retain store before the answer.
	SubstituteLog *SubstituteLog
	// AlarmLog, on a redundant pair, sends alarm acknowledgements, shelves
	// and unshelves through the replicated log of operator intent (#710
	// PR 3b-2c, alarms_oplog.go). Nil: they apply here.
	AlarmLog *AlarmLog

	// AckLinks mints and checks the links a page carries to acknowledge an
	// alarm from the notification (#715, acklink.go): on a pair, under a
	// key both members derive from the peer token (internal/pairkey), so
	// a link minted by one acks on the other. Nil: no links are minted,
	// and /alarm-ack/ answers that this controller sends none.
	AckLinks *acklink.Keyring
	// AckURL is alarms.oncall.ack-url, the HMI as a person paged reaches
	// it: the base of every link. "" mints none.
	AckURL string
	// AckLinkSignIn is alarms.oncall.ack-from-notification: sign-in: a
	// link acknowledges only for a browser signed in as the person it was
	// sent to. False (the default): the link alone acknowledges.
	AckLinkSignIn bool
	// Inbound takes acknowledgements back through the SMS and voice
	// provider: an SMS reply's code, a call's keypress (#715, inbound.go).
	// Nil: /alarm-inbound/ answers that this controller takes none.
	Inbound *Inbound

	// Audit is the runtime audit journal (#709): every write route records
	// one entry per request to it, accepted or refused, before the client
	// is answered (server/audit.go). naut run gives it the file journal
	// (audit.OpenFile). Nil keeps entries in memory only. Either way the
	// last 1000 are kept in a ring that GET /api/audit serves.
	Audit audit.Recorder

	// AuditUnavailable is why the journal the project configures could not
	// be opened (naut run sets it when audit.OpenFile fails, with Audit
	// nil). The server then runs with the entries in memory only, and says
	// so on the drivers block (/api/drivers, /api/state, the live frames)
	// until it restarts; strict actions (forces, program changes) are
	// refused meanwhile (#709, design §6 question 5).
	AuditUnavailable error

	// AuditCopy reports the journal's copy elsewhere (audit.sink:
	// postgres, #709 PR 9), shown as its own card on the drivers block;
	// nil when there is none.
	AuditCopy func() audit.CopyStatus

	// Auth is the manifest's compiled auth: block (#708): who may do what.
	// Nil keeps the write rule of before it existed (AuthToken when set,
	// else a same-origin request). With it, every write route asks the
	// policy (authorize), NAUTILUS_TOKEN is the principal "token" with the
	// block's token-role, and GET /api/audit needs the audit permission.
	// Every read needs the read permission (readgate.go): every GET under
	// /api/, and a page a browser opens, which goes to /signin without a
	// session. Users with a users file sign in by browser (signin.go).
	Auth *auth.Policy

	// ServiceTokens are the service tokens' secrets, by name (naut run
	// reads each from NAUTILUS_TOKEN_<NAME>). A request presenting one acts
	// as "token:<name>", with the grants the auth: block gives it.
	ServiceTokens map[string]string

	// LeaderURLs are a redundant pair's controllers as a browser reaches
	// them, by node name (redundancy.public-urls: {a: https://…, b: …}). A
	// standby refers a signed-in browser to the leader's; without one it
	// names no address (a peer address must never reach a browser).
	LeaderURLs map[string]string

	// OIDC signs browsers in at the auth: block's OpenID Connect provider
	// (#708 PR 6, oidc.go): naut run hands it auth/oidc's client. It needs
	// Auth with auth.oidc and Users (the pins and sign-outs live beside the
	// users file); without them it is ignored. OIDCRedirectURL is
	// auth.oidc.redirect-url: "" derives the redirect URI from a trusted
	// Host.
	// The seam's type is internal (internal/oidcseam): not stable Go API
	// while #708 is open.
	OIDC            oidcseam.Provider
	OIDCRedirectURL string

	// Users is the controller's users file (#708): a personal access token
	// it holds is a credential acting as "user:<name>", with the grants
	// the auth: block gives that user. Each change to the file is
	// journalled (users.change). Run re-reads it once a second.
	Users *users.Store

	// OnlineEdits enables the program endpoints (PUT /api/program, POST
	// /api/program/rollback) — PLC-style online edits of the running ST
	// program. Off by default: pushing logic is remote code execution on a
	// control system, so a controller must opt in (think keyswitch in
	// REMOTE). Online edits are ephemeral by design — a restart reverts to
	// the program the binary embeds; committing the source is how an edit
	// becomes permanent. Program writes honor AuthToken like tag writes.
	OnlineEdits bool

	// Drivers, if set, is polled for field-driver / publisher status and
	// served at GET /api/drivers (and included in each stream frame). It
	// keeps the server package free of any specific driver dependency —
	// the runner adapts eip.Health / sparkplug.Status into DriverStatus.
	Drivers func() []DriverStatus

	// Cluster reports this replica's redundancy state — leader.Elector's
	// Status method satisfies it. When set, GET /api/cluster serves it, and
	// requests a standby cannot answer from its own (stale, non-scanning)
	// tag store are reverse-proxied to the leader. Nil = no redundancy.
	Cluster interface{ Status() leader.Status }

	// HistorianURL, when set, proxies GET /api/history* to a historian
	// daemon (`naut historian`) at that base URL, so the HMI keeps one
	// origin for live and archived data. History reads answer on ANY
	// replica — the archive lives in the historian, not the tag store, so
	// a standby's copy is as good as the leader's. Empty = 503 with a
	// pointer at the missing configuration.
	HistorianURL string

	// History, when set, feeds GET /api/program/history: the project's
	// captured git provenance (see ProgramHistory). It's a getter, not a
	// value, so the runner can capture lazily — a built binary decodes its
	// embedded snapshot on first request, `naut run` shells out to git
	// then — without holding up the scan loop's start. Nil (or a getter
	// returning nil) serves an empty history; the endpoint never 404s.
	History func() *ProgramHistory

	// SourcesAt resolves a commit sha from History to every task's composed
	// program source (libraries joined ahead, exactly as boot composes
	// them), keyed by task name — what POST /api/program/activate swaps in.
	// The runner wires this through the project loader so composition rules
	// live in one place; nil disables activation while leaving the history
	// readable.
	SourcesAt func(sha string) (map[string]string, error)

	// IO, when set, serves the live I/O configuration routes (/api/io*,
	// ioconfig.go) and puts the config block (what runs against the
	// manifest) on every full frame. naut run wires the project loader's
	// IOConfig into it.
	IO *IOHook

	// Params, when set, serves the IO-Link device parameter routes
	// (/api/iolink/{port}/params*, params.go, #705): reads of the
	// parameters of the device on a port, and writes of them through its
	// master. naut run wires the project's port devices into it.
	Params *ParamHook
	// ParamWriteBack is how operator-tuned setpoints get back to the repo
	// (#718 slice 2); nil is mode off.
	ParamWriteBack *ParamWriteBack

	// Push turns Web Push on (#722); nil leaves /api/push/* answering 404.
	Push *PushOptions

	// Mounts are groups of controller-only routes (mount.go), added after the
	// core routes. cmd/naut lists them; the browser playground lists none.
	Mounts []Mount

	// Notes is the operator notes and shift log (#720) of a controller on
	// its own: a file. NotesLog, on a redundant pair, is the replicated
	// log's index and takes its place. Neither: /api/notes answers 404.
	Notes    *notes.Store
	NotesLog *NotesLog
	// SessionLog, on a redundant pair, carries browser sessions through the
	// pair's operator log so they survive a takeover (#716).
	SessionLog *SessionLog

	// HMI, when set, serves a built HMI — a SvelteKit `adapter-static`
	// build, or any other static SPA bundle — at "/", so the controller is
	// a one-process deploy: no separate web server for the operator screen.
	// An unmatched, non-"/api" path falls back to the bundle's index.html
	// (SPA client-side routing), and the built-in dashboard moves to
	// "/_nautilus/" (its assets to "/_nautilus/assets/") so it stays
	// reachable without colliding with whatever the HMI's own build puts
	// under "/assets/". Nil (the default): the dashboard keeps "/" exactly
	// as before.
	//
	// The HMI must call the API same-origin (a relative "/api/..." base
	// URL, not an absolute host) — see the manifest's `server.hmi` and the
	// "Serving the HMI from the controller" guide.
	HMI fs.FS

	// Alarms, when set, serves the five /api/alarms* routes and puts the
	// engine's Summary on every stream frame. Nil (the default): those
	// routes 404 and the frame carries no alarms field at all — which is
	// what the HMI kit's AlarmClient reads as "this controller has no
	// alarm engine" and renders nothing for, rather than showing an empty
	// list that looks like a quiet plant.
	Alarms *alarm.Engine

	// AlarmShelveTimes is the shelf durations an operator screen offers,
	// served on /api/meta as seconds. Empty uses alarm.DefaultShelveTimes.
	// It is configuration, not policy: the engine accepts any deadline in
	// the future, and this is only what the picker suggests.
	AlarmShelveTimes []time.Duration

	// TagWriter, when set, takes over POST /api/tags: an authorized write
	// is handed to it instead of the tag store. It is for a server fronting
	// a controller it does not run (`naut logix serve`), where the value
	// belongs on the device and the store only mirrors what the next poll
	// reads back. Writing the store there would be worse than useless: the
	// poll overwrites it within one cycle, so the editor would show a value
	// that never reached the PLC.
	//
	// name and value are exactly what the request carried (a dotted member
	// path is not split, an object payload is not merged). A returned error
	// is the response body; its status is 502 unless the error has an
	// HTTPStatus() int method, which is how a writer reports a request that
	// was wrong rather than a device that failed.
	TagWriter func(name string, value any) error
}

// DriverStatus is a field driver's or publisher's health, rendered by the
// HMI's driver-status components. The envelope is generic; Metrics and
// Extra carry the protocol-specific detail without the server needing to
// know the protocol.
//
// # Keeping it off the wire
//
// A driver status is the most expensive block on a frame — a Sparkplug host
// fronting 55 edge nodes renders ~13 kB of device rows and Extra — and on a
// delta stream it is sent only when it CHANGED (see hashDrivers). What
// "changed" means is therefore part of this type's contract, and an adapter
// that renders free-running values into it defeats the whole mechanism:
//
//   - AsOfMs is stamped by the server, not the adapter, and is excluded
//     from the comparison — it says WHEN this block was built, which is
//     what lets a client render "last poll 0.4s" from AtMs without the age
//     drifting upward between blocks.
//   - A metric that free-runs (a message counter, a poll count) sets
//     DriverMetric.Volatile, and one that reports a moment in time sets
//     AtMs instead of pre-rendering an age into Text.
//   - Parts of Extra that free-run are named in VolatileExtra, by key or by
//     path ("nodes.*.lastMsgMs") — nested churn is the common case.
//
// Volatile values still ride along whenever the block IS sent; they simply
// do not, on their own, put 13 kB on 4 frames a second. Their worst-case
// staleness on a delta stream is one resync (30 s by default).
type DriverStatus struct {
	Kind      string         `json:"kind"`    // "ethernet-ip" | "sparkplug"
	Name      string         `json:"name"`    // display name (host, or group/node)
	Detail    string         `json:"detail"`  // one-line address/target
	State     string         `json:"state"`   // connected|connecting|waiting|degraded|error|offline
	Message   string         `json:"message"` // human sentence for the current state
	SinceMs   int64          `json:"sinceMs"` // epoch ms the current state began (0 = unknown)
	LastError string         `json:"lastError,omitempty"`
	Metrics   []DriverMetric `json:"metrics,omitempty"` // labeled scalar readouts
	Devices   []DriverDevice `json:"devices,omitempty"` // sub-devices (Sparkplug devices, etc.)
	Extra     map[string]any `json:"extra,omitempty"`   // protocol-specific structured fields
	// Differs is set while this driver runs an I/O configuration applied live
	// that is not the manifest's (#706): the dashboard badges the row.
	Differs bool `json:"differs,omitempty"`

	// AsOfMs is when this status was OBSERVED, stamped by the server. On a
	// delta stream the block may be several seconds older than the frame
	// carrying it, so every age rendered from it (a metric's AtMs) must be
	// measured against this, not against the reader's clock — otherwise a
	// perfectly healthy "last publish 0.2s" creeps up to 30s and back on
	// every resync, which reads as a plant going quiet.
	AsOfMs int64 `json:"asOfMs,omitempty"`

	// VolatileExtra names the parts of Extra that free-run — a message
	// count, a last-seen timestamp, anything that moves on its own. They
	// are excluded from the change comparison, so they do not by themselves
	// push the whole status onto the wire. Never serialised.
	//
	// An entry is a top-level key ("unknown") or a PATH ("nodes.*.lastMsgMs"),
	// because the churn that matters is usually nested. A 55-site Sparkplug
	// host learned this the expensive way: every element of its
	// Extra["nodes"] carried a last-message stamp and a sequence number that
	// stepped on every message, so the block rode every frame — and naming
	// the whole "nodes" key volatile would have thrown away the roster
	// (online, stale, tag counts) that the gate exists to notice. In a path,
	// "*" matches every key of a map or every element of a list, and a list
	// is transparent to a plain segment, so "nodes.lastMsgMs" reaches into a
	// list of node objects too.
	VolatileExtra []string `json:"-"`
}

// DriverMetric is one labeled readout on a status card (poll rate, msgs, …).
type DriverMetric struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit,omitempty"`
	Text  string  `json:"text,omitempty"` // set for non-numeric values (overrides Value)

	// AtMs is a moment in time (epoch ms) the reader should render as an
	// age — "last poll", "last publish". Prefer it to pre-rendering the age
	// into Text: a rendered age changes on every build, which on a delta
	// stream would put the whole driver block on the wire four times a
	// second. Clients render it against DriverStatus.AsOfMs. Adapters may
	// set Text as well, for readers that predate this field; that text is
	// ignored for change detection whenever AtMs is set.
	AtMs int64 `json:"atMs,omitempty"`

	// Volatile marks a free-running readout — a counter that ticks with
	// traffic rather than with anything an operator would call a change.
	// Excluded from the change comparison (see DriverStatus). Never
	// serialised: it is a statement about the value, not part of it.
	Volatile bool `json:"-"`
}

// DriverDevice is one sub-device under a driver (a Sparkplug device).
type DriverDevice struct {
	ID     string `json:"id"`
	Online bool   `json:"online"`
	Detail string `json:"detail,omitempty"`
}

// Server fans runtime frames out to SSE clients and answers snapshot reads.
type Server struct {
	rt          *runtime.Runtime
	interval    time.Duration
	authToken   string
	policy      *auth.Policy      // Options.Auth; nil without an auth: block
	services    map[string]string // service token name (lowercased) → secret
	allowHosts  map[string]bool
	onlineEdits bool
	drivers     func() []DriverStatus
	auditCopy   func() audit.CopyStatus
	cluster     interface{ Status() leader.Status }
	historian   string
	historyFn   func() *ProgramHistory
	sourcesAt   func(sha string) (map[string]string, error)
	paramWB     *ParamWriteBack // parameter write-back (#718); nil: off
	push        *pushService    // Web Push subscriptions (push.go); nil: off
	notes       *notes.Store    // operator notes, standing alone (notes.go)
	notesLog    *NotesLog       // a pair's notes (notes.go)
	mounts      []Mount
	noteClock   func() time.Time // tests
	pushKey     string           // its VAPID public key
	io          *IOHook          // live I/O configuration (ioconfig.go); nil: none
	params      *ParamHook       // IO-Link device parameters (params.go); nil: none
	staging     ioStaging        // its staged candidates
	hmi         fs.FS
	alarms      *alarm.Engine
	shelveTimes []time.Duration
	tagWriter   func(name string, value any) error
	subRules    []SubstituteRule // Options.Substitutes
	subLog      *SubstituteLog   // Options.SubstituteLog
	alarmLog    *AlarmLog        // Options.AlarmLog
	// ack links (#715, acklink.go): Options.AckLinks, AckURL, AckLinkSignIn
	ackLinks      *acklink.Keyring
	ackURL        string
	ackLinkSignIn bool
	links         linkBook
	// inbound SMS and voice acknowledgements (#715, inbound.go)
	inbound      *Inbound
	inboundTries tries
	// inboundReplies counts the refusals texted to each person;
	// inboundAll the wrong codes from anyone, over a day.
	inboundReplies tries
	inboundAll     tries
	unknowns       *unknowns // proposals answered "outcome unknown", for their resolution (oplog_unknown.go)
	audit          *auditor

	resync time.Duration
	diag   time.Duration

	mu      sync.Mutex
	clients map[*client]struct{}
	active  string // last activated commit sha; see setActive

	// chBuf is the broadcast loop's reusable Change buffer — one delta
	// sweep per tick, shared by every delta client (see broadcast). Touched
	// only from the broadcast goroutine.
	chBuf []runtime.Change

	// The non-tag blocks' change tracking, all touched ONLY from the
	// broadcast goroutine (like chBuf) — a client's own copy of where it
	// has got to lives on the client, under its mutex.
	//
	// driversHash is the last hashDrivers value; driversRev counts the
	// times it changed, so a client compares one integer instead of a hash
	// it would have to store. scanRev counts diagnostics cadences.
	driversHash uint64
	driversRev  uint64
	scanRev     uint64
	scanAt      time.Time

	// Tag-map memo for the frame builder. A frame's Tags block is the
	// plain-JSON rendering of the WHOLE store, and on a controller with
	// thousands of tags that is by far the most expensive part of a frame —
	// rebuilt four times a second whether or not a single value moved.
	// runtime.Tags.Generation says whether it moved. The memoised map is
	// never mutated after it is built (a change replaces it wholesale), so
	// concurrent frame encoders may share one.
	tagMu   sync.Mutex
	tagGen  uint64
	tagSnap map[string]any

	users      *users.Store
	leaderURLs map[string]string // Options.LeaderURLs
	sessions   *sessions         // browser sign-ins; nil without users to sign in
	signin     *signInGate
	// oidc is the OIDC provider's seam, nil without OIDC sign-in; the
	// flows under way and the configured redirect URI go with it.
	oidc         oidcseam.Provider
	oidcFlows    *oidcFlows
	oidcTickets  *oidcTickets
	oidcRedirect string

	// unforceAllChecked, when set (tests only), runs between clear-all's
	// check of the forced tags and its clearing of them.
	unforceAllChecked func()

	tls       *CertSource
	leaderTLS *http.Transport // forwards to the leader over TLS, pinned
	// tlsPin is the leaf last said served (noteTLSChange).
	tlsMu     sync.Mutex
	tlsPin    [32]byte
	proxies   []netip.Prefix
	pinMu     sync.Mutex
	pinRefuse bool // a pin mismatch is journalled (once, until a forward succeeds)
}

// New builds a Server over a runtime.
func New(rt *runtime.Runtime, opts ...Options) *Server {
	interval := 250 * time.Millisecond
	resync := defaultResync
	diag := defaultDiagnostics
	token := ""
	onlineEdits := false
	if len(opts) > 0 {
		if opts[0].Interval > 0 {
			interval = opts[0].Interval
		}
		if opts[0].ResyncInterval != 0 {
			resync = opts[0].ResyncInterval
		}
		if opts[0].DiagnosticsInterval != 0 {
			diag = opts[0].DiagnosticsInterval
		}
		token = opts[0].AuthToken
		onlineEdits = opts[0].OnlineEdits
	}
	s := &Server{
		inboundAll:  tries{window: inboundAllWindow},
		rt:          rt,
		interval:    interval,
		resync:      resync,
		diag:        diag,
		authToken:   token,
		allowHosts:  map[string]bool{},
		onlineEdits: onlineEdits,
		clients:     map[*client]struct{}{},
	}
	var configured []string
	if len(opts) > 0 {
		configured = opts[0].AllowedHosts
	}
	// NAUTILUS_ALLOWED_HOSTS applies to every embedder (naut run, the
	// serve commands, an SDK program), so the setting a 403 names always
	// takes effect.
	s.allowHosts = allowedHostSet(configured)
	if len(opts) > 0 {
		s.drivers = opts[0].Drivers
		s.auditCopy = opts[0].AuditCopy
		s.cluster = opts[0].Cluster
		s.historian = opts[0].HistorianURL
		s.historyFn = opts[0].History
		s.sourcesAt = opts[0].SourcesAt
		s.io = opts[0].IO
		s.paramWB = opts[0].ParamWriteBack
		if po := opts[0].Push; po != nil && po.Key != nil {
			if ps, err := newPushService(s, po); err == nil {
				s.push, s.pushKey = ps, po.Key.Public()
				rt.SetPush(ps)
			} else {
				slog.Error("push: not started", "error", err)
			}
		}
		s.params = opts[0].Params
		s.hmi = opts[0].HMI
		s.alarms = opts[0].Alarms
		s.shelveTimes = opts[0].AlarmShelveTimes
		s.tagWriter = opts[0].TagWriter
		s.subRules = opts[0].Substitutes
		s.subLog = opts[0].SubstituteLog
		s.alarmLog = opts[0].AlarmLog
		s.ackLinks, s.ackURL, s.ackLinkSignIn = opts[0].AckLinks, opts[0].AckURL, opts[0].AckLinkSignIn
		s.inbound = opts[0].Inbound
		s.notes, s.notesLog = opts[0].Notes, opts[0].NotesLog
		s.mounts = opts[0].Mounts
	}
	var rec audit.Recorder
	var unavailable error
	if len(opts) > 0 {
		rec, unavailable = opts[0].Audit, opts[0].AuditUnavailable
	}
	s.audit = newAuditor(rec)
	s.unknowns = newUnknowns(s.audit.record)
	if s.io != nil && s.io.OnRevert != nil {
		s.io.OnRevert(s.recordIORevert)
	}
	// The logs tell this server what they apply that it proposed, so an
	// "outcome unknown" is resolved on evidence, and an alarm lapse is
	// journalled (oplog_unknown.go, alarms_oplog.go).
	if s.subLog != nil {
		s.subLog.observe(s.unknowns.applied)
	}
	if s.alarmLog != nil {
		s.alarmLog.observe(s.unknowns.applied, s.audit.recordBatch)
	}
	if s.notesLog != nil {
		s.notesLog.observe(s.unknowns.applied)
	}
	if len(opts) > 0 {
		s.policy = opts[0].Auth
		s.proxies = opts[0].TrustedProxies
		s.leaderURLs = opts[0].LeaderURLs
		if c := opts[0].TLS; c != nil {
			s.tls, s.leaderTLS = c, c.leaderTransport()
			c.OnReload(s.journalTLSReload)
			s.tlsPin, _ = c.current()
			// A connection to the leader was verified against the leaves
			// accepted then; when they change, make it again. A change of
			// the leaf served is said and journalled (tls.changed).
			c.OnPinsChange(func() {
				s.leaderTLS.CloseIdleConnections()
				s.noteTLSChange()
			})
		}
		for name, secret := range opts[0].ServiceTokens {
			if secret == "" {
				continue // an empty secret must never match an empty credential
			}
			if s.services == nil {
				s.services = map[string]string{}
			}
			s.services[strings.ToLower(name)] = secret
		}
		if u := opts[0].Users; u != nil {
			s.users = u
			if s.policy != nil {
				s.sessions = newSessions(s.policy.Session())
				s.sessions.OnEnd(s.endSessionStreams)
				if sl := opts[0].SessionLog; sl != nil {
					sl.bind(s.sessions)
					sl.OnLocal(s.journalLocalSession)
				}
				s.signin = newSignInGate()
				if opts[0].OIDC != nil && s.policy.OIDC() {
					s.oidc, s.oidcFlows, s.oidcTickets, s.oidcRedirect = opts[0].OIDC, newOIDCFlows(), newOIDCTickets(), opts[0].OIDCRedirectURL
				}
			}
			u.OnChange(s.journalUsersChange)
			u.OnHealth(s.journalUsersHealth)
			// What changed while the controller was stopped.
			u.CatchUp()
		}
	}
	if unavailable != nil {
		s.audit.unavailable = unavailable.Error()
	}
	if len(s.shelveTimes) == 0 {
		s.shelveTimes = alarm.DefaultShelveTimes
	}
	// An expiry has no request to journal it: the server hears it from
	// the runtime and records it (substitute.expire, by system).
	rt.OnSubstituteEvent(s.auditSubstituteEvent)
	return s
}

// Run drives the SSE broadcast loop until ctx is cancelled. Without it the
// HTTP endpoints still work; /api/stream just never emits frames.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	// The audit journal's upkeep (entries kept while it could not be
	// written, the summaries of closed refusal windows) runs beside the
	// broadcast loop, not in it: a journal catching up fsyncs its backlog,
	// and the live frames must not wait on the disk. Run returns once it
	// has flushed.
	upkept := make(chan struct{})
	go func() {
		defer close(upkept)
		s.audit.upkeep(ctx)
	}()
	if s.users != nil {
		go s.users.Run(ctx)
	}
	if s.push != nil {
		go s.push.run(ctx)
	}
	defer func() { <-upkept }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.broadcast()
			if _, ok := s.standbyLeader(); ok {
				s.endSessionsAsStandby()
			}
		}
	}
}

// tagsFrame is the frame's Tags block, rebuilt only when the tag store has
// actually changed. The generation is read BEFORE the rendering, so it can
// only ever under-state what the map holds — one redundant rebuild, never a
// stale frame.
func (s *Server) tagsFrame() map[string]any {
	tags := s.rt.Tags()
	gen := tags.Generation()
	s.tagMu.Lock()
	defer s.tagMu.Unlock()
	if s.tagSnap == nil || gen != s.tagGen {
		s.tagSnap, s.tagGen = tags.All(), gen
	}
	return s.tagSnap
}

// frame builds one complete Frame over every tag — the whole-store
// observation /api/state answers with and a plain SSE client receives every
// tick. pats, when non-empty, keeps only the tags (and quality entries)
// matching one of those globs; see matchAny.
func (s *Server) frame(pats []string) Frame {
	stats := s.rt.Stats()
	now := time.Now()
	f := Frame{
		TS:      now.UnixMilli(),
		Scans:   stats.Count,
		Tags:    filterTags(s.tagsFrame(), pats),
		Locals:  s.rt.AllLocals(),
		Scan:    &stats,
		Quality: filterQuality(qualityJSON(s.rt.Quality()), pats),
		Drivers: s.driverStatus(now),
		Forces:  s.rt.Tags().ForcedValues(),
	}
	f.Alarms = s.alarmSummary()
	f.Config = s.ioConfigState()
	return f
}

// driverStatus polls the configured driver provider and stamps each status
// with the moment it was observed. The stamp is the server's job, not the
// adapter's, because it is what a client renders a metric's age against —
// see DriverStatus.AsOfMs, and hashDrivers for why the stamp is excluded
// from the block's change comparison.
func (s *Server) driverStatus(now time.Time) []DriverStatus {
	var ds []DriverStatus
	if s.drivers != nil {
		ds = s.drivers()
	}
	if j := s.audit.status(); j != nil {
		ds = append(ds, *j)
	}
	if s.auditCopy != nil {
		ds = append(ds, copyCard(s.auditCopy()))
	}
	if ds == nil {
		return nil
	}
	ms := now.UnixMilli()
	for i := range ds {
		if ds[i].AsOfMs == 0 {
			ds[i].AsOfMs = ms
		}
	}
	return ds
}

// Handler returns the API routes. Mount it directly or under your own mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/parameters", s.handleParameters)
	mux.HandleFunc("GET /api/parameters/patch", s.handleParametersPatch)
	mux.HandleFunc("GET /api/notes", s.handleNotesList)
	mux.HandleFunc("GET /api/notes/counts", s.handleNotesCounts)
	mux.HandleFunc("GET /api/notes/{id}", s.handleNoteGet)
	mux.Handle("POST /api/notes", s.audited("note.add", noteAddTarget, s.handleNoteAdd))
	mux.Handle("POST /api/notes/{id}/revise", s.audited("note.revise", noteChangeTarget, s.handleNoteRevise))
	mux.Handle("POST /api/notes/{id}/retract", s.audited("note.retract", noteChangeTarget, s.handleNoteRetract))
	mux.HandleFunc("GET /api/push/key", s.handlePushKey)
	mux.HandleFunc("GET /api/push/subscriptions", s.handlePushList)
	mux.HandleFunc("GET /api/push/health", s.handlePushHealth)
	mux.Handle("POST /api/push/subscribe", s.audited("push.subscribe", pushTarget, s.handlePushSubscribe))
	mux.Handle("POST /api/push/confirm-revocations", s.audited("push.confirm-revocations", pushConfirmTarget, s.handlePushConfirm))
	mux.Handle("DELETE /api/push/subscribe", s.audited("push.unsubscribe", pushTarget, s.handlePushUnsubscribe))
	mux.Handle("POST /api/parameters/import", s.audited("param.import", s.paramImportTarget, s.handleParametersImport))
	mux.HandleFunc("GET /api/drivers", s.handleDrivers)
	// Alarms: always registered, 404 when no engine — see server/alarms.go.
	// Registering them unconditionally is what makes "this controller has
	// no alarm engine" a message rather than a bare route-not-found.
	mux.HandleFunc("GET /api/alarms", s.handleAlarms)
	mux.HandleFunc("GET /api/alarms/journal", s.handleAlarmJournal)
	mux.Handle("POST /api/alarms/ack", s.audited("alarm.ack", ackTarget, s.handleAlarmAck))
	// Ack links (#715): GET is the confirm page and changes nothing (a
	// mail scanner that opens links acks nothing); POST acknowledges.
	// The paths are acklink.Path (TestAckLinkRoutesAreAcklinkPath).
	mux.HandleFunc("GET /alarm-ack/{token}", s.handleAckLinkPage)
	mux.Handle("POST /alarm-ack/{token}", s.audited("alarm.ack", linkAuditTarget, s.handleAckLink, s.checkLink))
	// Inbound acknowledgements (#715): the SMS and voice provider posts a
	// reply's code or a call's keypress, signed (InboundSMSPath,
	// InboundVoicePath; TestInboundRoutesAreTheirPaths).
	mux.Handle("POST /alarm-inbound/sms", s.audited("alarm.ack", inboundAuditTarget, s.handleInboundAck, s.checkInbound("sms")))
	mux.Handle("POST /alarm-inbound/voice/{token}", s.audited("alarm.ack", inboundAuditTarget, s.handleInboundAck, s.checkInbound("voice")))
	mux.Handle("POST /api/alarms/shelve", s.audited("alarm.shelve", shelveTarget, s.handleAlarmShelve))
	mux.Handle("POST /api/alarms/unshelve", s.audited("alarm.unshelve", unshelveTarget, s.handleAlarmUnshelve))
	// Equipment out of service (#715 PR 5): its alarms suppressed until a
	// deadline, a week at most, with a reason; back in service ends it.
	mux.HandleFunc("GET /api/alarms/out-of-service", s.handleOutOfServiceList)
	mux.Handle("POST /api/alarms/out-of-service", s.audited("alarm.out-of-service", oosTarget, s.handleOutOfService))
	mux.Handle("POST /api/alarms/in-service", s.audited("alarm.in-service", inServiceTarget, s.handleInService))
	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.Handle("POST /api/tags", s.audited("tag.write", s.tagTarget, s.handleWriteTag))
	mux.HandleFunc("GET /api/program", s.handleGetProgram)
	mux.Handle("PUT /api/program", s.audited("program.edit", s.programTarget, s.handlePutProgram))
	mux.Handle("POST /api/program/rollback", s.audited("program.rollback", s.rollbackTarget, s.handleRollback))
	mux.HandleFunc("GET /api/program/history", s.handleProgramHistory)
	mux.Handle("POST /api/program/activate", s.audited("program.activate", s.activateTarget, s.handleActivate))
	mux.HandleFunc("GET /api/io", s.handleIO)
	mux.HandleFunc("GET /api/iolink/{port}/params", s.handleParamList)
	mux.HandleFunc("GET /api/iolink/{port}/params/{ref}", s.handleParamRead)
	mux.Handle("PUT /api/iolink/{port}/params/{ref}", s.audited("param.write", s.paramTarget, s.handleParamWrite))
	mux.HandleFunc("GET /api/io/files", s.handleIOFiles)
	mux.HandleFunc("GET /api/config/schema", s.handleConfigSchema)
	mux.HandleFunc("GET /api/config/files", s.handleConfigFiles)
	mux.Handle("POST /api/io/staged", s.audited("config.stage", ioStageTarget, s.handleIOStage))
	mux.HandleFunc("GET /api/io/staged/{id}", s.handleIOStaged)
	mux.Handle("POST /api/io/staged/{id}/validate", s.audited("config.validate", ioStagedTarget, s.handleIOValidate))
	mux.Handle("POST /api/io/staged/{id}/apply", s.audited("config.apply", ioApplyTarget, s.handleIOApply))
	mux.HandleFunc("GET /api/config/patch", s.handleConfigPatch)
	mux.Handle("POST /api/config/commit", s.audited("config.commit", configCommitTarget, s.handleConfigCommit))
	mux.Handle("POST /api/io/revert/cancel", s.audited("config.revert.cancel", nil, s.handleIOCancelRevert))
	mux.HandleFunc("GET /api/forces", s.handleForces)
	mux.HandleFunc("GET /api/substitutes", s.handleSubstitutes)
	mux.Handle("POST /api/substitutes", s.audited("substitute.start", s.substituteStartTarget, s.handleSubstituteStart))
	mux.Handle("DELETE /api/substitutes/{id}", s.audited("substitute.stop", s.substituteStopTarget, s.handleSubstituteStop))
	mux.Handle("POST /api/substitutes/ack", s.audited("substitute.ack", s.substituteAckTarget, s.handleSubstituteAck))
	mux.Handle("POST /api/forces", s.audited("force.set", s.forceTarget, s.handleForce))
	mux.Handle("POST /api/forces/clear", s.audited("force.clear-all", s.unforceAllTarget, s.handleUnforceAll))
	mux.Handle("DELETE /api/forces/{name}", s.audited("force.clear", s.unforceTarget, s.handleUnforce))
	mux.HandleFunc("GET /api/sfc", s.handleSFC)
	mux.Handle("POST /api/sfc/step", s.audited("sfc.step", sfcTarget(true), s.handleSFCStep))
	mux.Handle("POST /api/sfc/transition", s.audited("sfc.transition", sfcTarget(false), s.handleSFCTransition))
	mux.HandleFunc("GET /api/cluster", s.handleCluster)
	mux.Handle("POST /api/cluster/clear-duplicate", s.audited("redundancy.clear-duplicate", s.clearDuplicateTarget, s.handleClearDuplicate))
	mux.Handle("POST /api/cluster/commission", s.audited("redundancy.commission", s.commissionTarget, s.handleCommission))
	mux.Handle("POST /api/cluster/recommission", s.audited("redundancy.recommission", s.recommissionTarget, s.handleRecommission))
	mux.Handle("POST /api/cluster/vouch", s.audited("redundancy.vouch", s.vouchTarget, s.handleVouch))
	mux.HandleFunc("GET /api/audit", s.handleAudit)
	mux.HandleFunc("GET /api/me", s.handleMe)
	// Sign-in and sign-out journal themselves (auth.signin, auth.signout),
	// outside the audited seam: TestEveryWriteRouteIsAudited names them.
	mux.HandleFunc("GET /signin", s.handleSignInPage)
	mux.HandleFunc("POST /api/signin", s.handleSignIn)
	mux.HandleFunc("POST /api/signout", s.handleSignOut)
	mux.HandleFunc("POST /api/elevate", s.handleElevate)
	mux.HandleFunc("GET /auth/oidc/start", s.handleOIDCStart)
	mux.HandleFunc("GET /auth/oidc/callback", s.handleOIDCCallback)
	mux.HandleFunc("GET /auth/oidc/elevated", s.handleOIDCElevated)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /api/history/", s.handleHistory)
	if s.hmi != nil {
		// The HMI owns "/" (and whatever paths its own build wants under
		// it, "/assets/" included); the built-in dashboard moves out of
		// the way to "/_nautilus/" so it stays reachable without a name
		// collision.
		mux.HandleFunc("GET /_nautilus/", s.handleNautilusIndex)
		mux.HandleFunc("GET /_nautilus/assets/", handleAsset)
		mux.Handle("GET /", s.handleHMI())
	} else {
		mux.HandleFunc("GET /assets/", handleAsset)
		mux.HandleFunc("GET /", s.handleIndex)
	}
	for _, m := range s.mounts {
		m(s, mux)
	}
	return s.GuardHost(s.withCORS(s.proxyStandby(mux, apiNotFound(mux))))
}

// apiNotFound makes every /api/ path no API route matches a JSON 404,
// whatever else owns "/": without it the HMI's SPA fallback ("GET /") answered
// a typo'd endpoint with index.html (200, text/html), and a GET on a POST-only
// route did the same. The SPA fallback stays for every non-/api path. It asks
// the mux which pattern would serve the request rather than registering an
// "/api/" catch-all, which would conflict with "GET /".
func apiNotFound(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if _, pattern := mux.Handler(r); pattern == "" || pattern == "GET /" {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such API endpoint: " + r.Method + " " + r.URL.Path})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// proxyStandby wraps mux so that on a standby replica, requests it cannot
// safely answer from its own tag store are transparently reverse-proxied to
// the leader — the point being that a load balancer can hit any replica and
// still reach live data and accept writes. It only ever changes behavior
// when Options.Cluster is set; with no Cluster configured every request
// falls straight through to mux, exactly as before redundancy existed.
//
// Everything outside "/api/" is always answered locally, even on a
// standby: the built-in dashboard, its assets, and — when server.hmi is
// configured — the whole HMI build, SPA-fallback routes included. None of
// it reads the tag store, so a load-balancer health probe or a browser
// always gets a page, whichever replica it lands on, and a client-side
// route two levels deep in the HMI doesn't need special-casing here (it
// was never a "/api/" path to begin with). Within "/api/", GET
// /api/cluster is also always local (each replica reports its own view of
// the cluster — showing that divergence is the entire point), and so is
// /api/history*: the archive lives in the historian, not the tag store,
// so a standby answers it as well as the leader and keeps answering it
// mid-failover.
//
// A write the standby answers itself (the closed-gate 503, a body it
// cannot read, a leader that failed or never answered: 502) is journalled
// through the route's audit seam, as the leader journals one it serves.
func (s *Server) proxyStandby(routes *http.ServeMux, mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cluster == nil {
			mux.ServeHTTP(w, r)
			return
		}
		// An ack link (#715) acts on the leader's alarms, as an API write
		// does: a standby forwards it (the leader checks it with the same
		// pair-derived key), and never answers it from its own engine.
		link := strings.HasPrefix(r.URL.Path, acklink.Path) && !mutServer("link_not_forwarded")
		// So does a provider's inbound reply or keypress: it carries its
		// signature, which the leader checks against the same configured
		// URL.
		inbound := strings.HasPrefix(r.URL.Path, InboundPath) && !mutServer("inbound_not_forwarded")
		// Answered as the provider's only on its two routes; anything else
		// under the prefix is a plain answer.
		inboundRoute := r.Method == http.MethodPost && (r.URL.Path == InboundSMSPath ||
			(strings.HasPrefix(r.URL.Path, InboundVoicePath) && len(r.URL.Path) > len(InboundVoicePath) && !strings.Contains(r.URL.Path[len(InboundVoicePath):], "/")))
		link = link || inbound
		if (!strings.HasPrefix(r.URL.Path, "/api/") && !link) ||
			r.URL.Path == "/api/cluster" ||
			// The pair's operator commands act on this controller and its
			// members, never on the leader's tag store, and are needed
			// exactly when nobody leads (#1029 3c-3a).
			(localClusterOps[r.URL.Path] && !mutServer("cluster_ops_proxied")) ||
			strings.HasPrefix(r.URL.Path, "/api/history") {
			mux.ServeHTTP(w, r)
			return
		}
		st := s.cluster.Status()
		if link && (noLeaderKnown(st) || heldHere(st)) {
			// Nobody here can act on the alarm: say so on the page, for a
			// GET too (this store's engine does not scan).
			const msg = "No controller is leading right now, so the alarm cannot be acknowledged from here. Try the link again in a minute, or open the HMI."
			if inbound && inboundRoute {
				auditedAnswer(routes, w, r, func(w http.ResponseWriter, _ *http.Request) {
					// A call hears why; an SMS gets nothing back: this
					// replica has not checked the sender, and a reply would
					// go to whatever number the request names.
					kind, reply := "sms", ""
					if strings.HasPrefix(r.URL.Path, InboundVoicePath) {
						kind, reply = "voice", "Not acknowledged: no controller is leading right now. Try again in a minute, or open the HMI."
					}
					s.inboundRefused(w, kind, http.StatusServiceUnavailable, "no controller is leading", reply, true)
				})
				return
			}
			if inbound {
				http.Error(w, "no controller is leading", http.StatusServiceUnavailable)
				return
			}
			if r.Method == http.MethodPost {
				auditedAnswer(routes, w, r, func(w http.ResponseWriter, _ *http.Request) {
					s.linkRefused(w, http.StatusServiceUnavailable, msg)
				})
				return
			}
			s.linkPage(w, http.StatusServiceUnavailable, linkPageData{Title: "Not acknowledged", Error: msg})
			return
		}
		if noLeaderKnown(st) {
			// A redundant pair's standby that knows no leader (an election
			// under way, the leader gone, a pair not yet commissioned): a
			// write here would land in a store that does not scan, so it
			// is refused, as a lease holder with its gate closed refuses
			// one (#621). Reads are answered from this store, and so is a
			// sign-out (it only revokes; sign-in waits for the election).
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.URL.Path == "/api/signout" {
				mux.ServeHTTP(w, r)
				return
			}
			auditedAnswer(routes, w, r, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"error": "no leader: this controller is standing by and no leader is known (an election is under way, or the pair cannot elect); try again shortly",
				})
			})
			return
		}
		if st.IsLeader || st.LeaderAddr == "" {
			// We're the leader, or nobody's address is known yet (a fresh
			// cluster still electing, or a standalone-mode elector that never
			// sets LeaderAddr) — handle locally rather than erroring a
			// request that has nowhere better to go.
			mux.ServeHTTP(w, r)
			return
		}
		if heldHere(st) {
			// The lease names this replica, but its gate is closed (its
			// scans are not completing, #621): the leader's address is our
			// own, and forwarding would send the request back here, again
			// and again. Reads are answered from this store; a write is
			// refused, since nothing here scans it. A sign-out is
			// answered: the sessions are this box's.
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.URL.Path == "/api/signout" {
				mux.ServeHTTP(w, r)
				return
			}
			auditedAnswer(routes, w, r, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"error": "this replica holds the lease but its write gate is closed (its scans are not completing); try again shortly",
				})
			})
			return
		}
		// Sign-in, sign-out and /api/me are decided at the box the client
		// reached, never forwarded: secure() at the leader would judge the
		// standby's hop, not the client's (#1043 review). Sessions live on
		// the leader (#708 decision (c)), so a sign-in here, a sign-out
		// (only the leader can revoke the session) and any request with
		// the sign-in cookie and no bearer credential are referred to it.
		s.endSessionsAsStandby()
		if standbyRefers(r) && !link {
			auditedAnswer(routes, w, r, func(w http.ResponseWriter, _ *http.Request) {
				s.referToLeader(w, s.leaderURLs[st.Leader])
			})
			return
		}
		if r.URL.Path == "/api/me" {
			mux.ServeHTTP(w, r)
			return
		}
		// The body is kept so a request the leader never received can be
		// replayed to its successor (forwardTo). API bodies are small; one
		// past the cap is forwarded once, without the replay.
		var body []byte
		if r.Body != nil && r.Body != http.NoBody {
			b, err := io.ReadAll(io.LimitReader(r.Body, maxReplayBody+1))
			if err != nil {
				auditedAnswer(routes, w, r, func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "reading request: "+err.Error(), http.StatusBadRequest)
				})
				return
			}
			if len(b) > maxReplayBody {
				r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(b), r.Body))
				s.forwardTo(w, r, st.LeaderAddr, routes, mux, nil, false)
				return
			}
			body = b
		}
		s.forwardTo(w, r, st.LeaderAddr, routes, mux, body, true)
	})
}

// standbyRefers reports a request a forwarding standby answers with the
// leader's address: sign-in, sign-out, and anything sent with the sign-in
// cookie and no bearer credential. The standby holds no sessions, so it
// goes by the cookie being there, valid or not.
func standbyRefers(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/signin", "/api/signout", "/api/elevate", oidcStartPath, oidcCallbackPath, oidcElevatedPath:
		return true
	}
	return hasSessionCookie(r) && presentedToken(r) == ""
}

// referToLeader is a forwarding standby's answer to a browser session's
// request: 503, and the leader's configured public URL (Location and
// "leader") for the HMI kit to follow. Never the peer address: that is
// for the pair (leader.Status keeps it out of JSON), and this answer goes
// to anyone. Without a configured URL, the 503 names none. The cookie is
// left as it is: only the leader can end the session it names.
func (s *Server) referToLeader(w http.ResponseWriter, leader string) {
	w.Header().Set("Cache-Control", "no-store")
	// standby is the field a page keys on (the HMI kit follows it to
	// leader); the error text is for a person.
	body := map[string]any{"error": "this controller is the standby; open the leader (browser sign-ins live on the leader)", "standby": true}
	if leader != "" {
		w.Header().Set("Location", leader+"/")
		body["leader"] = leader
	}
	writeJSON(w, http.StatusServiceUnavailable, body)
}

// endSessionsAsStandby ends the browser sessions this controller issued
// while it led, once it is a standby that forwards to another leader:
// sessions live on the leader, and one kept here would act again if this
// box led again (a failover signs browser users out), where a sign-out at
// the leader in between could not have revoked it. Run checks at every
// tick; a forwarded request checks too.
func (s *Server) endSessionsAsStandby() {
	if s.sessions == nil {
		return
	}
	if n := s.sessions.endAll(); n > 0 {
		slog.Info("auth: this controller is now a standby; its browser sessions are ended", "sessions", n)
		s.audit.record(audit.Entry{User: "system", Source: audit.SourceSystem, Action: "auth.sessions-ended",
			Detail: map[string]any{"reason": "this controller is now a standby: browser sessions live on the leader", "sessions": n},
			Result: audit.ResultOK})
	}
}

// journalLocalSession records a sign-in the operator log did not commit: it
// is valid on this controller only until the log comes back (#716).
func (s *Server) journalLocalSession(se session) {
	slog.Warn("auth: a session stays local to this controller: the operator log did not commit it; it is replicated when the log returns", "user", se.key())
	s.audit.record(audit.Entry{User: se.key(), Source: audit.SourceSystem, Action: "auth.session-local",
		Detail: map[string]any{"session": se.hash, "reason": "the operator log did not commit the sign-in: the session is valid on this controller only until it does"},
		Result: audit.ResultOK})
}

// standbyLeader reports whether this controller is a standby that
// forwards to a leader, and that leader's configured public URL ("" when
// none is).
func (s *Server) standbyLeader() (string, bool) {
	if s.cluster == nil {
		return "", false
	}
	st := s.cluster.Status()
	if st.IsLeader || st.LeaderAddr == "" || noLeaderKnown(st) || heldHere(st) {
		return "", false
	}
	return s.leaderURLs[st.Leader], true
}

// heldHere reports a replica that the lease names but that is not leading:
// its gate is closed while it still holds the lease (a hung scan, #621, or
// a renew older than the lease), so LeaderAddr is its own address. Never a
// proxy target: that is a request forwarding to itself without end.
// noLeaderKnown: a redundant pair member (quorum mode) that does not lead
// and knows no leader. The Kubernetes mode keeps serving locally then: its
// standalone elector never names a leader address.
func noLeaderKnown(st leader.Status) bool {
	return st.Mode == "quorum" && !st.IsLeader && st.LeaderAddr == ""
}

func heldHere(st leader.Status) bool {
	return !st.IsLeader && st.Leader != "" && st.Leader == st.Pod
}

// maxReplayBody is the largest request body a standby keeps for a replay.
const maxReplayBody = 1 << 20

// failoverWait is how long a standby waits for leadership to move when the
// leader refuses its connection: a leader shutting down closes its HTTP
// port first (#423) and hands the lease over last, after its drivers and
// Sparkplug stop, so for a few seconds the address it still advertises
// answers nothing (#483). Overridable in tests.
var failoverWait = 10 * time.Second

// forwardTo reverse-proxies r to the leader at addr. When the leader
// refuses the connection, the request never reached it, so replaying it
// cannot apply a write twice: with replay set, the standby waits for the
// lease to move and sends it again — to the new leader, or to its own mux
// if this replica is the new leader. Any other failure (the leader took
// the request and then failed) is a 502, as before; replaying that one
// could repeat a write.
func (s *Server) forwardTo(w http.ResponseWriter, r *http.Request, addr string, routes *http.ServeMux, mux http.Handler, body []byte, replay bool) {
	if body != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}
	scheme := "http"
	if s.leaderTLS != nil {
		scheme = "https"
	}
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: scheme, Host: addr})
	direct := proxy.Director
	proxy.Director = func(out *http.Request) {
		direct(out)
		s.forwardHeaders(out)
	}
	if s.leaderTLS != nil {
		proxy.Transport = s.leaderTLS
		proxy.ModifyResponse = func(*http.Response) error {
			s.pinMu.Lock()
			s.pinRefuse = false
			s.pinMu.Unlock()
			return nil
		}
	}
	// -1 disables buffering: an SSE frame must reach the client the
	// instant the leader emits it, not whenever the proxy's timer next
	// fires.
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		var op *net.OpError
		if replay && errors.As(err, &op) && op.Op == "dial" {
			if next, local, ok := s.awaitLeaderChange(r.Context(), addr); ok {
				if body != nil {
					r.Body = io.NopCloser(bytes.NewReader(body))
				}
				if local {
					mux.ServeHTTP(w, r)
					return
				}
				s.forwardTo(w, r, next, routes, mux, body, false)
				return
			}
		}
		if errors.Is(err, errNotOurCert) {
			s.journalPinMismatch(addr)
			auditedAnswer(routes, w, r, func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "leader unavailable: "+errNotOurCert.Error(), http.StatusBadGateway)
			})
			return
		}
		auditedAnswer(routes, w, r, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "leader unavailable: "+err.Error(), http.StatusBadGateway)
		})
	}
	proxy.ServeHTTP(w, r)
}

// awaitLeaderChange waits up to failoverWait for the cluster to name a
// leader other than the one at old: this replica (local) or another
// address. ok is false when nothing changed in time, or when the client
// gave up first (ctx): a write the client abandoned, and has likely
// retried, must not be applied late.
func (s *Server) awaitLeaderChange(ctx context.Context, old string) (addr string, local, ok bool) {
	deadline := time.NewTimer(failoverWait)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		st := s.cluster.Status()
		if st.IsLeader {
			return "", true, ctx.Err() == nil
		}
		if st.LeaderAddr != "" && st.LeaderAddr != old && !heldHere(st) {
			return st.LeaderAddr, false, ctx.Err() == nil
		}
		select {
		case <-ctx.Done():
			return "", false, false
		case <-deadline.C:
			return "", false, false
		case <-tick.C:
		}
	}
}

// withCORS lets a browser page on another origin read (and, with a token,
// write) the API only when that page cannot be someone else's site (#815):
// a page on this machine (a loopback origin, any port: a dev server), the
// controller's own host on another port (when that host is one #811
// trusts), a name in AllowedHosts, or a request that carries the token. It
// echoes that Origin, with Vary: Origin, and sends nothing for any other,
// so a website the operator happens to have open cannot read the tags,
// the alarms, the history or the program source. It once sent "*".
//
// A preflight (OPTIONS) reads nothing; with a token configured it is
// answered for any origin, so an authorized cross-origin client can send
// its token header, and the real request is then judged as above.
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always: a cache must not hand one origin's answer (with or
		// without the CORS header) to another.
		w.Header().Add("Vary", "Origin")
		if r.Method == http.MethodOptions {
			s.answerPreflight(w, r)
			return
		}
		s.allowOrigin(w, r)
		next.ServeHTTP(w, r)
	})
}

// allowOrigin sends the CORS answer for r's Origin when a page there may
// read the API (corsOrigin, or a request carrying a valid credential), and
// nothing for any other. withCORS sends it for every route; the read gate,
// which answers before withCORS is reached, sends it on its refusals, so
// an allowed dev page can read why it was refused instead of a bare
// network error. The caller has already added Vary: Origin.
func (s *Server) allowOrigin(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" && (s.corsOrigin(r, origin) || s.validCredential(r)) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		// A page may read whether its action was journalled to disk.
		w.Header().Set("Access-Control-Expose-Headers", auditJournalHeader)
	}
}

// answerPreflight answers an OPTIONS request: 204, no body, and for a
// trusted origin (any origin when a token is configured) the methods and
// headers a cross-origin page may use. It never reaches a route.
func (s *Server) answerPreflight(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && (s.corsOrigin(r, origin) || s.credentialsConfigured()) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		// Every method a route below answers: PUT is the online-edit
		// push (PUT /api/program), DELETE the per-tag unforce.
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		// The auth headers, so an authorized cross-origin writer's
		// preflight succeeds; Content-Type for JSON bodies.
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Nautilus-Token, X-Nautilus-Client")
	}
	w.WriteHeader(http.StatusNoContent)
}

// GuardHost answers only a request addressed to a Host no page can rebind
// (localhost, an IP address or an allowed name, the rule #811 applies to
// writes), or, when a token is configured, one that carries it (#819). A
// page on an attacker's domain rebound to this machine is same-origin with
// it, so CORS does not stop it reading; this does, for every route: the
// API, the live stream, the dashboard and the HMI. A page load and an
// EventSource cannot send a header, so a dashboard reached by a name needs
// that name allowed, token or not. A facade that serves routes of its own
// wraps its handler in it too.
func (s *Server) GuardHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Liveness and readiness carry one word and nothing else: a
		// probe may reach them by any Host, with no credential.
		if s.probe(w, r) {
			return
		}
		trusted := s.trustedHost(r.Host)
		// A browser's CORS preflight never carries the token, so on a token
		// server a genuine one is answered here (204, no data) and never
		// passed to a route of the handler this wraps; the request it asks
		// about still needs the token.
		if !trusted && s.credentialsConfigured() && r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Add("Vary", "Origin")
			s.answerPreflight(w, r)
			return
		}
		if !trusted && !s.validCredential(r) {
			refuseHost(w, r, "controller", s.credentialsConfigured())
			return
		}
		// With an auth: block, a read needs the read permission
		// (readgate.go). A facade wraps a handler that is already
		// wrapped: the second pass decides the same.
		if s.readRefused(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// corsOrigin reports a browser Origin whose page may read the API across
// origins without a token: loopback, this controller's own (trusted) host,
// or an allowed name. Any port.
func (s *Server) corsOrigin(r *http.Request, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false // "null" (a sandboxed frame, a file), or garbage
	}
	oh := strings.ToLower(hostOnly(u.Host))
	if oh == "localhost" || strings.HasSuffix(oh, ".localhost") {
		return true
	}
	if ip := net.ParseIP(oh); ip != nil && ip.IsLoopback() {
		return true
	}
	if s.allowHosts[oh] {
		return true
	}
	return oh == strings.ToLower(hostOnly(r.Host)) && s.trustedHost(r.Host)
}

// handleIndex serves the landing page at exactly "/". Because "GET /" is
// the catch-all pattern, anything not matched by a more specific route
// lands here; non-root paths get a real 404 rather than the page. Only
// mounted when no HMI is configured — see handleHMI.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// handleNautilusIndex is handleIndex's counterpart when an HMI has claimed
// "/": the same page, moved to "/_nautilus/" so it's still reachable
// (linking a build's operator screen back to the raw tag table is exactly
// the point of moving it rather than dropping it).
func (s *Server) handleNautilusIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/_nautilus/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// handleHMI serves a configured HMI build (Options.HMI) at "/": a real
// file in the tree is served as-is (content type, range requests, caching
// — all http.FileServer's usual behavior); anything else falls back to
// the bundle's own index.html, so client-side routing (SvelteKit, or any
// other SPA router) resolves a deep link like "/tanks/101" itself instead
// of getting a 404 from this server, which has no idea such a route
// exists.
// Caching: an embedded FS has no mod times, so http.FileServer's default
// stamps every file "Fri, 30 Nov 1979" — browsers heuristically cache such
// HTML for years and their If-Modified-Since revalidations 304 forever,
// which strands every open tab on the build BEFORE a redeploy (found live:
// a reviewed fix was deployed and the operator's browser kept rendering
// the old bundle). So the split every SPA build wants is made explicit
// here: content-hashed files (SvelteKit's _app/immutable/) are immutable
// for a year, and EVERYTHING else — index.html above all — is no-cache
// with a content ETag, served with a zero mod time so the only validator
// is the hash and a stale tab's next navigation always fetches the new
// HTML.
func (s *Server) handleHMI() http.Handler {
	fsrv := http.FileServer(http.FS(s.hmi))
	// The hash is cached per file, keyed by its size and mod time as well
	// as its path: an embedded bundle never changes under a path, but a
	// directory served by `naut run` is rebuilt in place, and a hash
	// cached by path alone would answer a stale tab's If-None-Match with
	// 304 forever after the rebuild — the new HTML, new models, never
	// seen until a hard reload.
	// One entry per path, replaced when the stamp moves, so a controller
	// rebuilt all day keeps one hash per file rather than one per build.
	type cached struct{ stamp, etag string }
	var mu sync.Mutex
	etags := map[string]cached{}
	etagFor := func(lookup string) string {
		stamp := ""
		if st, err := fs.Stat(s.hmi, lookup); err == nil {
			stamp = fmt.Sprintf("%d|%d", st.Size(), st.ModTime().UnixNano())
		}
		mu.Lock()
		defer mu.Unlock()
		if c, ok := etags[lookup]; ok && c.stamp == stamp {
			return c.etag
		}
		b, err := fs.ReadFile(s.hmi, lookup)
		if err != nil {
			return ""
		}
		sum := sha256.Sum256(b)
		e := `"` + hex.EncodeToString(sum[:8]) + `"`
		etags[lookup] = cached{stamp, e}
		return e
	}
	serve := func(w http.ResponseWriter, r *http.Request, lookup string) {
		if strings.HasPrefix(lookup, "_app/immutable/") {
			// The name carries the content hash; the bytes can never
			// change under it. The 1979 Last-Modified is harmless here.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			fsrv.ServeHTTP(w, r)
			return
		}
		// ReadFile, not Open: the HMI FS is whatever the build embedded,
		// and a bundle's files need not be seekable (a controller built by
		// `naut build` serves them from an archive — an earlier draft
		// that fell back to http.FileServer for non-seekable files served
		// the request's ORIGINAL path there and 404ed every deep link).
		// These files are small; a bytes.Reader gives ServeContent the
		// seeking it wants for ranges.
		b, err := fs.ReadFile(s.hmi, lookup)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		if strings.HasSuffix(lookup, ".webmanifest") {
			// the standard sniffing would call it text/plain
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		if e := etagFor(lookup); e != "" {
			w.Header().Set("Etag", e)
		}
		// Zero mod time: ServeContent then neither sends Last-Modified nor
		// honours If-Modified-Since — the ETag is the only validator, so a
		// redeploy (new hash) can never be answered with a stale 304.
		http.ServeContent(w, r, path.Base(lookup), time.Time{}, bytes.NewReader(b))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		lookup := path.Clean(p)
		if lookup == "." || strings.HasSuffix(p, "/") {
			// The root, or a directory-style path (trailing slash): the
			// file that answers it — if any — is its own index.html, same
			// as http.FileServer's own directory convention.
			lookup = path.Join(lookup, "index.html")
		}
		if st, err := fs.Stat(s.hmi, lookup); err == nil && !st.IsDir() {
			serve(w, r, lookup)
			return
		}
		// SPA fallback: the bundle's own index.html answers unknown paths
		// (client-side routing), with the same no-cache/ETag policy.
		serve(w, r, "index.html")
	})
}

// handleAsset serves the embedded logo, favicon and fonts. Paths are matched
// against the embedded tree, so nothing outside it is reachable; anything not
// in the tree (including a traversal attempt) is a plain 404.
//
// The cache lifetime is long because these bytes only change when the binary
// does, and a font re-fetched on every page load is a visible flash of
// fallback text on a wall-mounted dashboard that reloads all day.
//
// Mounted at both "/assets/" (no HMI configured) and "/_nautilus/assets/"
// (HMI configured, dashboard moved) — the "_nautilus/" prefix, if present,
// is stripped before the embedded-tree lookup so one handler serves both.
func handleAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	name = strings.TrimPrefix(name, "_nautilus/")
	b, err := staticFS.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ct := assetTypes[path.Ext(name)]; ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Write(b)
}

// handleState answers one whole-store Frame. `?tags=` narrows it to the
// matching tags — the same globs /api/stream takes, so a screen that
// subscribes to a subset fetches the same subset on load instead of pulling
// half a megabyte to read forty points.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	pats, err := parseTagFilter(r.URL.Query().Get("tags"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Encode before writing: a value JSON cannot render would otherwise
	// go out as a 200 with an empty body, which every client reads as
	// "no tags" and nobody can diagnose. A 500 with the encoder's reason
	// is a bug report; silence was a blank dashboard.
	b, err := json.Marshal(s.frame(pats))
	if err != nil {
		http.Error(w, "state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// handleDrivers serves the current field-driver / publisher status list.
// Empty (but 200) when no Drivers provider is configured.
func (s *Server) handleDrivers(w http.ResponseWriter, r *http.Request) {
	out := s.driverStatus(time.Now())
	if out == nil {
		out = []DriverStatus{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleCluster reports this replica's own view of redundancy: its pod
// name, who it believes holds the lease, and whether that's itself. It is
// never proxied (see proxyStandby) — a standby and the leader disagreeing
// briefly during a failover is the signal the dashboard exists to show, not
// a bug to paper over by asking the leader about itself.
//
// With no Cluster configured this still answers, as a standalone leader —
// so a dashboard can read /api/cluster unconditionally instead of special-
// casing "redundancy isn't wired up" as a 404.
func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	st := leader.Status{Mode: "standalone", IsLeader: true}
	if s.cluster != nil {
		st = s.cluster.Status()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(clusterBody{Status: st, Peers: s.clusterPeers(st)})
}

// clusterBody is GET /api/cluster: the replica's own status plus, for a
// pair with redundancy.public-urls, the controllers as a browser opens them.
type clusterBody struct {
	leader.Status
	Peers []clusterPeer `json:"peers,omitempty"`
}

// clusterPeer is one controller of the pair as a browser reaches it. URL is
// the operator-named public URL, never a pod or peer-link address (#716);
// Leader is this replica's own view and may disagree with the other's
// during a takeover, so a client confirms with the peer it picks.
type clusterPeer struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Leader bool   `json:"leader"`
}

func (s *Server) clusterPeers(st leader.Status) []clusterPeer {
	if len(s.leaderURLs) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.leaderURLs))
	for n := range s.leaderURLs {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]clusterPeer, 0, len(names))
	for _, n := range names {
		out = append(out, clusterPeer{Name: n, URL: s.leaderURLs[n], Leader: st.Leader == n})
	}
	return out
}

// handleHistory forwards /api/history* to the historian daemon, trimming
// the /api prefix (/api/history/span → {historian}/history/span). The HMI
// keeps one origin; where the archive actually lives is deployment detail.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if s.historian == "" {
		http.Error(w, "no historian configured (set server.historian in the manifest, or NAUTILUS_HISTORIAN_URL)", http.StatusServiceUnavailable)
		return
	}
	target, err := url.Parse(s.historian)
	if err != nil {
		http.Error(w, "bad historian url: "+err.Error(), http.StatusInternalServerError)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	orig := proxy.Director
	proxy.Director = func(req *http.Request) {
		orig(req)
		req.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
		// Addressed to the historian by the name it was configured as,
		// not the controller's: the historian answers by its own Host
		// rule (#831), and the controller's names are not its.
		req.Host = target.Host
		// The controller's token is for the controller; the historian
		// is another service and never sees it.
		req.Header.Del("Authorization")
		req.Header.Del("X-Nautilus-Token")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "historian unavailable: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// metaResponse is the static tag documentation for an HMI: descriptions and
// units (runtime.Options.Meta) plus which tags are driver-bound. A tag table
// derives quality from this + the frame: an input while the scan reports
// ioHealthy=false is showing a stale, held value.
type metaResponse struct {
	// Tags is each tag's documentation (desc, unit) and declared type
	// (type, and an enumeration's members as enum) — see tagMetaJSON.
	Tags map[string]tagMetaJSON `json:"tags"`
	// Locals is each streamed program local's declared type, the same
	// shape, so a client can show an enumerated local (whose value streams
	// as its member's name) unquoted, as it shows an enumerated tag.
	Locals       map[string]typeInfo `json:"locals"`
	Inputs       []string            `json:"inputs"`
	Outputs      []string            `json:"outputs"`
	ScanTargetMs float64             `json:"scanTargetMs"`
	// MemberWrites says this controller accepts POST /api/tags with a dotted
	// member path ("P101.Drive.Speed") or an object payload merging into a
	// struct tag. It is a capability flag, not a policy: writability itself
	// is still the root tag's (a member of an Input is refused like the
	// Input is). An HMI that binds UDT members needs it because the answer
	// used to be no — a dotted name silently created a junk tag — so a
	// faceplate built for a newer runtime must be able to tell, and disable
	// its controls against an older one instead of writing into the void.
	MemberWrites bool `json:"memberWrites"`
	// Alarms says this controller runs an alarm engine, so /api/alarms*
	// will answer. Same capability-flag reasoning as MemberWrites: an HMI
	// that renders an alarm banner needs to know whether to render one at
	// all, and finding out by taking a 404 makes an ordinary page load
	// look like an error.
	Alarms bool `json:"alarms"`
	// Quality says this controller can report per-tag data quality: it runs
	// a driver that implements io.QualityReporter, has driver-bound inputs
	// the runtime can mark Stale on a read failure, or is a redundant
	// replica, whose standby marks every scan-fed tag Stale.
	// The capability flag matters more here than anywhere else in this
	// response, because the FALSE case is invisible: an empty `quality` map
	// looks exactly like a healthy plant, and an HMI that cannot tell the
	// two apart would render a confident green badge on a controller that
	// has no idea. With this false, a screen shows no quality indication at
	// all rather than a reassuring one.
	Quality bool `json:"quality"`
	// Deltas says GET /api/stream understands `?delta=1` and `?tags=`. An
	// HMI kit newer than the controller must not ask for a delta stream
	// from a server that will ignore the parameter and send full frames
	// the client then merges as if they were partial — which happens to be
	// harmless (a full frame merged over a subset is the full state), but
	// the client would never see `full` and could not tell resync from
	// steady state. Cheaper to advertise.
	Deltas bool `json:"deltas"`
	// Forces says this controller has a force table (/api/forces, and the
	// frame's `forces`) and the online SFC commands (/api/sfc/*). An editor
	// offers Force… only against a controller that will hold one, rather
	// than finding out from a 404 mid-commissioning. False on a server that
	// fronts a controller it does not run (TagWriter set).
	Forces bool `json:"forces"`
	// BlockDeltas says GET /api/stream understands `?blocks=delta`: the
	// non-tag blocks (scan diagnostics, driver status, alarm counts) sent
	// only when they change, instead of on every frame. Advertised for the
	// same reason as Deltas — a client that merges partial blocks against
	// a controller that does not implement them is merely paying for
	// nothing, but a client that DOESN'T merge them against a controller
	// that does would blank a driver panel between changes, so the
	// capability is asked for explicitly rather than assumed.
	BlockDeltas bool `json:"blockDeltas"`
	// ShelveTimes is the shelf durations the operator screen's picker
	// offers, in SECONDS — the unit every other duration in this API's
	// JSON is not, but the one the HMI kit's DEFAULT_SHELVE_TIMES_S uses,
	// and a shelf of 4.5 milliseconds is not a thing anyone wants.
	ShelveTimes []int `json:"shelveTimes"`
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metaResponse{
		Tags:         metaTags(s.rt),
		Locals:       metaLocals(s.rt),
		Inputs:       nonNilStrings(s.rt.Inputs()),
		Outputs:      nonNilStrings(s.rt.Outputs()),
		ScanTargetMs: s.rt.Stats().TargetMs,
		MemberWrites: true,
		Alarms:       s.alarms != nil,
		Quality:      s.rt.ReportsQuality(),
		Deltas:       true,
		BlockDeltas:  true,
		Forces:       s.tagWriter == nil,
		ShelveTimes:  shelveSeconds(s.shelveTimes),
	})
}

// handleParameters is GET /api/parameters: every scalar setpoint with a
// manifest init, its live value, its range and whether the plant and the
// repo agree (runtime.Parameter). Read-only; drift is reported, not
// resolved.
func (s *Server) handleParameters(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ps := s.rt.Parameters()
	mode := "off"
	if wb := s.paramWB; wb != nil {
		if wb.Mode != "" {
			mode = wb.Mode
		}
		if wb.Unwritable != nil {
			why := wb.Unwritable()
			for i := range ps {
				if ps[i].Pending() {
					ps[i].Unwritable = why[ps[i].Name]
				}
			}
		}
	}
	json.NewEncoder(w).Encode(struct {
		WriteBack  string              `json:"writeBack"`
		Parameters []runtime.Parameter `json:"parameters"`
	}{mode, ps})
}

// ParamWriteBack is the controller's way of putting plant-ahead parameter
// values back in the repo (#718): the mode, the patch of what a commit
// would write, and why some parameters cannot be written.
type ParamWriteBack struct {
	Mode string // off | patch | commit
	// Patch is the plant-ahead values as a patch ("" when nothing would
	// change). An error wrapping ErrIOCommitRefused is a 409.
	Patch func() (string, error)
	// Unwritable is, by parameter name, why a pending one is left as drift.
	Unwritable func() map[string]string
}

// handleParametersPatch is GET /api/parameters/patch: what a commit would
// write, as a patch for `git apply`, for a controller with no repo. It
// returns whole files' lines, so it needs the edit permission.
func (s *Server) handleParametersPatch(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	wb := s.paramWB
	if wb == nil || wb.Patch == nil || wb.Mode == "" || wb.Mode == "off" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "parameters.writeBack is off: set it to patch or commit in nautilus.yaml to get tuned values back as a patch"})
		return
	}
	p, err := wb.Patch()
	switch {
	case errors.Is(err, ErrIOCommitRefused):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	case p == "":
		writeJSON(w, http.StatusConflict, map[string]string{"error": "nothing to patch: no parameter is ahead of the repo (or the ones that are cannot be written: GET /api/parameters says why)"})
		return
	}
	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	_, _ = w.Write([]byte(p))
}

func shelveSeconds(ds []time.Duration) []int {
	out := make([]int, 0, len(ds))
	for _, d := range ds {
		out = append(out, int(d.Seconds()))
	}
	return out
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// authorizeWrite decides whether a tag-write request may proceed, returning
// (0, "") to allow or an (HTTP status, message) to reject.
//
// Base layer — no AuthToken configured: writes must be same-origin. A
// browser page from another origin (the drive-by CSRF case) carries an
// Origin header that won't match the host and is refused; non-browser
// clients (curl, the LSP, server-to-server) send no Origin and are allowed.
// A same-origin browser write must also be addressed to a Host no page can
// rebind: localhost, an IP address, or a name in AllowedHosts (#811).
// This costs nothing to run and needs no setup on localhost or by IP.
//
// Progressive enhancement — AuthToken set: the request must present the
// token, and a valid token authorizes the write from any origin (an
// attacker's page can't read or guess it, so CORS is irrelevant to safety).
// AuthorizeWrite applies the server's write policy — the auth token, else
// same-origin — to a request another handler serves, so a facade that adds
// an endpoint keeps the same rule for it. 0 means allowed. With an auth:
// block it needs write over every tag; an endpoint that does something
// else asks Authorize for its own permission.
func (s *Server) AuthorizeWrite(r *http.Request) (int, string) { return s.authorizeWrite(r) }

func (s *Server) authorizeWrite(r *http.Request) (int, string) {
	if s.policy != nil {
		// The audit seam has checked the route's own permission (and a
		// handler then checks its target); anything else needs write over
		// every tag.
		if decidedBySeam(r) {
			return 0, ""
		}
		return s.authorize(r, PermWrite, auth.All)
	}
	if _, ok := linkOf(r); ok && !mutServer("link_needs_write_rule") {
		// An ack link (#715, acklink.go) is its own credential: checked
		// (signature, expiry) before the request got here, and good for one
		// occurrence of one alarm. The token-or-same-origin rule would
		// refuse a phone that holds no NAUTILUS_TOKEN.
		return 0, ""
	}
	if s.authToken != "" {
		if tokenMatches(r, s.authToken) {
			return 0, ""
		}
		return http.StatusUnauthorized, "missing or invalid auth token"
	}
	if !sameOrigin(r) {
		return http.StatusForbidden, "cross-origin writes require an auth token (start nautilus with one, e.g. NAUTILUS_TOKEN)"
	}
	// A browser request whose Origin matches its Host can still come from
	// an attacker's page: rebind that page's name to this machine (DNS
	// rebinding) and Host and Origin are both the attacker's name. Without a
	// token, trust only a Host no one can rebind (#811).
	if r.Header.Get("Origin") != "" && !s.trustedHost(r.Host) {
		return http.StatusForbidden, fmt.Sprintf("a browser write addressed to %q is refused without an auth token: only localhost, an IP address, or a name in NAUTILUS_ALLOWED_HOSTS is trusted (a page on another site could rebind %q to this machine); set NAUTILUS_TOKEN, or add the name to NAUTILUS_ALLOWED_HOSTS", r.Host, hostOnly(r.Host))
	}
	return 0, ""
}

// trustedHost reports a request Host a page cannot have been rebound to:
// localhost (and *.localhost, which browsers keep on loopback), an IP
// literal (a browser sends one only for a page it loaded from that very
// address), or a name configured in AllowedHosts.
func (s *Server) trustedHost(host string) bool {
	return hostTrusted(s.allowHosts, host)
}

// EnvAllowedHosts is NAUTILUS_ALLOWED_HOSTS: host names, comma-separated,
// the server answers by without the token besides localhost and an IP.
func EnvAllowedHosts() []string {
	var out []string
	for _, h := range strings.Split(os.Getenv("NAUTILUS_ALLOWED_HOSTS"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// hostOnly strips the port (and an IPv6 address's brackets) from a Host.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

// sameOrigin reports whether a request is safe from a CSRF standpoint: it
// either carries no Origin header (a non-browser client) or an Origin whose
// host matches the request's Host.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // not a browser-issued cross-origin request
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

// writeTagRequest is the POST /api/tags payload.
//
// Name addresses a whole tag ("TempSP") or one member of a struct tag by a
// dotted path ("P101.Drive.Speed") — the same paths a test manifest's
// `given:`/`expect:` and an HMI's tag bindings already use.
//
// Value is a JSON number or boolean for a scalar; for a struct tag it may
// also be an OBJECT of member → value, which merges (members it names are
// set, every other member keeps its current value).
type writeTagRequest struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

func (s *Server) handleWriteTag(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.authorizeWrite(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	var req writeTagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, `expected {"name": ..., "value": ...}`, http.StatusBadRequest)
		return
	}
	if code, msg := s.authorize(r, PermWrite, req.Name); code != 0 {
		http.Error(w, msg, code)
		return
	}
	if s.tagWriter == nil {
		// A forced address holds its forced value against every writer,
		// an operator's included; answering 204 for a write the force will
		// swallow would be a lie. Checked first, so a forced input says
		// "forced" rather than "input"; the front door checks again under
		// its lock.
		if f := s.rt.Tags().ForcedOverlap(req.Name); f != "" {
			http.Error(w, (&runtime.ForcedError{Path: req.Name, Force: f}).Error(), http.StatusConflict)
			return
		}
	}
	if s.tagWriter != nil {
		if err := s.tagWriter(req.Name, req.Value); err != nil {
			code := http.StatusBadGateway
			var hs interface{ HTTPStatus() int }
			if errors.As(err, &hs) {
				code = hs.HTTPStatus()
			}
			http.Error(w, err.Error(), code)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// A member write — a dotted name, or an object payload merging into a
	// struct tag — resolves through the tag's own StructDef and is
	// read-modify-written whole. It must address something that EXISTS: a
	// flat assignment of "P101.Spede" would otherwise create a top-level tag
	// under that literal name, which no program reads and a Sparkplug edge
	// would publish as a bogus metric. That is a 400 with the reason.
	// An enumerated tag (#238) takes a member name or integer, whole or by
	// path, through the same rule, and a name that is no member is a 400
	// that lists the members (#247).
	_, isObject := req.Value.(map[string]any)
	_, isList := req.Value.([]any)
	enum := false
	if t := s.rt.Tags().TypeOf(req.Name); t != nil && t.Enum != nil {
		enum = true
	}
	member := isObject || isList || strings.ContainsAny(req.Name, ".[")
	root, _ := ir.SplitAddress(req.Name)
	if msg := s.refuseInputWrite(root, member); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if member || enum {
		s.frontDoor(w, req, false, s.principalOf(r))
		return
	}
	// Whole-tag scalar write. JSON numbers decode to float64; booleans to
	// bool; a string is taken only by a tag that already holds a STRING (a
	// recipe name, a scenario), so a typo cannot turn a REAL into text, or
	// by a TIME, which reads it as a duration literal (T#1m30s).
	// Anything else is refused here rather than answered 204 for a write
	// that didn't happen.
	// The name must be a declared tag. A write to anything else would create
	// a top-level tag no program reads and a Sparkplug edge would publish as
	// a bogus metric, so it is a 400 that names the tag. A CONSTANT is not a
	// tag, so it lands here too: a project constant cannot be written.
	if !s.declaredTag(req.Name) {
		http.Error(w, "undefined tag "+req.Name+": not a declared tag, so there is nothing to write (a CONSTANT is not a tag and cannot be written)", http.StatusBadRequest)
		return
	}
	switch req.Value.(type) {
	case float64, bool:
	case string:
		kind := ir.TypeVoid
		if cur, err := s.rt.Tags().ReadGlobal(req.Name); err == nil {
			kind = cur.Kind
		} else if t := s.rt.Tags().TypeOf(req.Name); t != nil {
			kind = t.Kind
		}
		// A TIME takes its IEC literal (T#1m30s) as well as milliseconds;
		// the front door parses it and answers a malformed one with a 400.
		if kind != ir.TypeString && kind != ir.TypeTime {
			http.Error(w, "value must be a number or boolean (a string only to a STRING tag, or a T# literal to a TIME)", http.StatusUnprocessableEntity)
			return
		}
	default:
		http.Error(w, "value must be a number or boolean", http.StatusUnprocessableEntity)
		return
	}
	s.frontDoor(w, req, true, s.principalOf(r))
}

// frontDoor applies one write through the runtime's front door
// (runtime.Tags.Write), which converts to the declared type, checks the
// declared width and refuses a forced address, and answers it: 204 when it
// landed; 409 for a force; for a whole-tag scalar (whole) 422 when the
// value is the wrong kind, as before the front door; 400 for everything
// else, an out-of-range value included (#421: an INT took 70000).
//
// A declared tag the store has not seen yet (an output no scan has written)
// is written onto a zero of its declared type, by the same strict rule, so
// it cannot be born retyped (#421 on the output a Sparkplug host commands
// through). Only a tag with no declared type at all, which has nothing to
// convert against, is written as it comes.
func (s *Server) frontDoor(w http.ResponseWriter, req writeTagRequest, whole bool, by string) {
	err := s.rt.Tags().WriteAs([]runtime.WriteOp{{Path: req.Name, Value: req.Value}}, by)[0]
	if whole && errors.Is(err, runtime.ErrUndefined) {
		if t := s.rt.Tags().TypeOf(req.Name); t != nil {
			var nv ir.Value
			if nv, err = ir.SetFieldTypedStrict(ir.Zero(t), t, nil, req.Value, "tag "+req.Name); err == nil {
				s.rt.Tags().Set(s.rt.Tags().DeclaredOutputName(req.Name), nv)
			}
		} else {
			s.rt.Tags().Set(req.Name, req.Value)
			err = nil
		}
	}
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, runtime.ErrForced):
		http.Error(w, err.Error(), http.StatusConflict)
	case whole && errors.Is(err, ir.ErrTypeMismatch):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

// declaredTag reports whether name is a tag the controller declares: one in
// the store, one the manifest types or a program binds (VAR_EXTERNAL or the
// project's tag table), or a driver input or output not yet delivered. A
// CONSTANT is none of these.
func (s *Server) declaredTag(name string) bool {
	if _, ok := s.rt.Tags().Canonical(name); ok || s.rt.Tags().TypeOf(name) != nil {
		return true
	}
	for _, list := range [][]string{s.rt.Inputs(), s.rt.Outputs()} {
		for _, n := range list {
			if strings.EqualFold(n, name) {
				return true
			}
		}
	}
	key := ir.NameKey(name)
	for g := range s.rt.Globals() {
		if ir.NameKey(g) == key {
			return true
		}
	}
	return false
}

// refuseInputWrite reports why a write to this root tag must not proceed,
// or "" to allow it. The role rule is applied to the ROOT tag, not to the
// member: the store holds whole tags, so writing P101.Cmd rewrites P101.
//
// Only a driver-owned INPUT is refused, whole or by member. Its value is
// the field's: a communications driver replaces it at its next delivery,
// so the write is a lost update dressed up as a command, and a driver that
// does not deliver every scan (the memory driver, a stalled device) lets
// it stick, so the logic acts on a reading no instrument made while the
// screen shows it as the field's (F-10). Holding an input is what a force
// is for: visible in the force table, and released by name. Everything
// else stays writable — setpoints and state because they are the
// operator's, and OUTPUTS because that is exactly how a Sparkplug host
// commands an edge node: the operator writes the output tag and the host
// driver publishes it as a command.
func (s *Server) refuseInputWrite(root string, member bool) string {
	for _, in := range s.rt.Inputs() {
		if !strings.EqualFold(in, root) {
			continue
		}
		if member {
			return "tag " + root + " is a driver-owned input: the driver replaces its whole " +
				"value before every scan, so a member write would be discarded unread — " +
				"write the setpoint or command tag the logic reads instead"
		}
		return "tag " + root + " is a driver-owned input: its value is the field's, " +
			"so a write is refused — force it (POST /api/forces) to hold a value during " +
			"bring-up, or write the setpoint or command tag the logic reads instead"
	}
	return ""
}
