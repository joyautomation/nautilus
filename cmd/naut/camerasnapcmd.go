package main

// naut camera snapshots purge (#724): drop the pictures a running controller
// kept when alarms activated. They are pictures of people, so how long they are
// kept is the camera-snapshots: policy's (keep-days, 30 by default) and this is
// the way to drop them sooner. The purge is journalled (camera.snapshot-purge)
// and needs the config permission and a fresh password (NAUTILUS_TOKEN or a
// token is always fresh).

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/internal/apiclient"
)

const cameraUsage = `naut camera — the plant's cameras on a running controller

Usage:
  naut camera snapshots purge --host <controller> (--all | --older-than-days N)

Drops the pictures kept when alarms activated (camera-snapshots: in the
manifest says how long they are kept: keep-days, 30 by default). The purge is
journalled and needs the config permission and a fresh password.

Flags:
  --host HOST            the controller (required)
  --port N               its tag API port (default 8080)
  --all                  every picture
  --older-than-days N    pictures older than N days

The credential is NAUTILUS_TOKEN, sent as a bearer token when set (there is no
--token flag, so a token never sits in the shell history).
`

func runCamera(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(cameraUsage)
		return 0
	}
	if args[0] != "snapshots" || len(args) < 2 || args[1] != "purge" {
		fmt.Fprintf(os.Stderr, "naut camera: unknown command %q\n\n%s", strings.Join(args, " "), cameraUsage)
		return 2
	}
	fs := flag.NewFlagSet("camera snapshots purge", flag.ContinueOnError)
	host := fs.String("host", "", "controller hostname or IP")
	port := fs.Int("port", 8080, "tag API port")
	all := fs.Bool("all", false, "every picture")
	days := fs.Int("older-than-days", 0, "pictures older than N days")
	fs.Usage = func() { fmt.Fprint(os.Stderr, cameraUsage) }
	if err := fs.Parse(args[2:]); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "naut camera snapshots purge: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *host == "" {
		fmt.Fprintln(os.Stderr, "naut camera snapshots purge: --host is required")
		return 2
	}
	if *all == (*days > 0) {
		fmt.Fprintln(os.Stderr, "naut camera snapshots purge: say what to purge: --all, or --older-than-days N (one of them)")
		return 2
	}
	base, err := apiclient.BaseURL(*host, *port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut camera snapshots purge: %v\n", err)
		return 2
	}
	body, _ := json.Marshal(map[string]any{"all": *all, "olderThanDays": *days})
	req, err := http.NewRequest(http.MethodPost, base+"/api/camera-snapshots/purge", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut camera snapshots purge: %v\n", err)
		return 2
	}
	req.Header.Set("Content-Type", "application/json")
	apiclient.Authorize(req)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut camera snapshots purge: no controller at %s: %v\n", base, err)
		return 1
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "naut camera snapshots purge: %s returned %s: %s%s\n", base, resp.Status, strings.TrimSpace(string(out)), apiclient.RefusalHint(resp.StatusCode))
		return 1
	}
	var r struct {
		Purged int `json:"purged"`
	}
	_ = json.Unmarshal(out, &r)
	fmt.Printf("%d picture(s) purged\n", r.Purged)
	return 0
}
