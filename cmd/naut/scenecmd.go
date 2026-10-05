// `naut scene init`: the generator for a 3D HMI scene document, so the
// first *.scene.json of a project is derived from its tags rather than
// typed. The tag model already says which tags are UDT instances and of
// what type; the built-in kinds say which UDTs they draw; the rest is a
// grid and a camera. What it cannot do is know where things ARE — that
// is the one edit left to a person (or, in Milestone 2, a drag).

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/scene"
	"github.com/joyautomation/nautilus/runtime"
)

const sceneUsage = `naut scene — 3D HMI scene tools (@joyautomation/nautilus-hmi-3d)

Usage:
  naut scene init [flags] [dir]   Generate <name>.scene.json from the
                                  manifest's struct tags: one node per UDT
                                  tag a kind can draw, on a grid, camera
                                  fitted. Then move things; nothing to type.
    -m <manifest>     load one other than nautilus.yaml
    -o <file>         output path (default <project name>.scene.json in dir)
    --kind Type=kind  draw UDT Type with kind (repeatable). The built-ins
                      draw Tank=tank, Motor=pump, Valve=valve by default;
                      a project whose pump UDT is VfdPump says
                      --kind VfdPump=pump, and the file records it under
                      kinds: so naut check holds the nodes to it.
    --spacing <m>     grid pitch in metres (default 0.8)
    --force           overwrite an existing file

  naut check holds every *.scene.json at the project root to the tags and
  UDTs the manifest declares — unknown kinds, wrong types, missing members.
`

func runScene(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, sceneUsage)
		return 2
	}
	switch args[0] {
	case "init":
		return runSceneInit(args[1:])
	case "help", "--help", "-h":
		fmt.Print(sceneUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "naut scene: unknown command %q\n\n%s", args[0], sceneUsage)
		return 2
	}
}

func runSceneInit(args []string) int {
	fs := flag.NewFlagSet("scene init", flag.ContinueOnError)
	manifest := fs.String("m", "", manifestFlagUsage)
	out := fs.String("o", "", "output path (default <project name>.scene.json)")
	var kinds stringList
	fs.Var(&kinds, "kind", "Type=kind: draw UDT Type with kind (repeatable)")
	spacing := fs.Float64("spacing", 0.8, "grid pitch, metres")
	force := fs.Bool("force", false, "overwrite an existing file")
	fs.Usage = func() { fmt.Fprint(os.Stderr, sceneUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	name := *manifest
	if name == "" {
		name = "nautilus.yaml"
	}

	byType := map[string]string{}
	for _, kv := range kinds {
		t, k, ok := strings.Cut(kv, "=")
		if !ok || t == "" || k == "" {
			fmt.Fprintf(os.Stderr, "naut scene init: --kind wants Type=kind, not %q\n", kv)
			return 2
		}
		byType[t] = k
	}

	proj, err := project.Load(os.DirFS(dir), name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut scene init: %s\n", err)
		return 1
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut scene init: %s\n", err)
		return 1
	}
	tags := proj.SceneTagInfo(rt)

	doc, unplaced, warns := scene.Generate(tags, scene.GenOptions{Name: proj.Name, KindByType: byType, Spacing: *spacing})
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "naut scene init: warning: %s\n", w)
	}

	path := *out
	if path == "" {
		path = filepath.Join(dir, proj.Name+".scene.json")
	}
	if _, err := os.Stat(path); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "naut scene init: %s exists — edit it, or --force to regenerate\n", path)
		return 1
	}
	data, err := scene.Encode(doc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut scene init: %s\n", err)
		return 1
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "naut scene init: %s\n", err)
		return 1
	}

	counts := map[string]int{}
	for _, n := range doc.Nodes {
		counts[n.Kind]++
	}
	kindNames := make([]string, 0, len(counts))
	for k := range counts {
		kindNames = append(kindNames, k)
	}
	sort.Strings(kindNames)
	parts := make([]string, 0, len(kindNames))
	for _, k := range kindNames {
		parts = append(parts, fmt.Sprintf("%s ×%d", k, counts[k]))
	}
	fmt.Printf("wrote %s: %d nodes", path, len(doc.Nodes))
	if len(parts) > 0 {
		fmt.Printf(" (%s)", strings.Join(parts, ", "))
	}
	fmt.Println()
	if len(unplaced) > 0 {
		byT := map[string][]string{}
		for _, t := range unplaced {
			byT[t.TypeName] = append(byT[t.TypeName], t.Name)
		}
		types := make([]string, 0, len(byT))
		for t := range byT {
			types = append(types, t)
		}
		sort.Strings(types)
		fmt.Printf("%d struct tag(s) have no kind to draw them:\n", len(unplaced))
		for _, t := range types {
			fmt.Printf("  %-16s %s\n", t, strings.Join(byT[t], ", "))
		}
		fmt.Printf("place them with --kind <Type>=tank|pump|valve, or declare a kind of your own under kinds: (docs/design/spatial-hmi.md §3b)\n")
	}
	// The generated file is held to the same check as a hand-written one,
	// so a --kind that points a UDT at a kind whose members it lacks is
	// reported now, not at the first naut check.
	if errs, _ := scene.Check(doc, tags); len(errs) > 0 {
		for _, m := range errs {
			fmt.Printf("error: %s\n", m)
		}
		return 1
	}
	return 0
}
