package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/l5x"
)

const logixUsage = `naut logix — Allen-Bradley Logix project tools

Reads L5X exports: the XML Logix Designer writes on File → Export. Pure Go,
so none of this needs Studio 5000, a licence, or Windows.

Usage:
  naut logix import <file.L5X>     Generate logix_types.st (the project's
                                       UDTs as IEC types) and a nautilus tag
                                       file WITH the controller's own tag
                                       descriptions — which a live CIP browse
                                       cannot recover.
  naut logix graph <file.L5X|-> [routine]
                                       Emit an RLL routine's ladder render
                                       model as JSON: the same shape
                                       "naut ld graph" emits for a .ld
                                       file, so the ladder preview and the
                                       revision diff work on Logix rungs.
                                       "-" reads the export from stdin,
                                       which is how the diff graphs a git
                                       revision.
  naut logix normalize <file.L5X>  Pin the attributes that move on every
                                       export (ExportDate and friends), so two
                                       exports of unchanged code compare equal.
                                       The basis of drift detection.
  naut logix info <file.L5X>       Summarize what the export contains.

Everything above is pure Go and works on an L5X already on disk. The verbs
below drive a Logix PROJECT, which needs the Studio 5000 SDK — so they talk
to a logixd agent on the licensed Windows machine (tools/logixd):

  naut logix probe                 Is the SDK usable, and if not, which
                                       licensing gate failed?
  naut logix agent                 Agent health and open sessions.
  naut logix browse                Controllers FactoryTalk Linx can reach,
                                       and the comm path for each.
  naut logix convert <in> <out>    ACD <-> L5X <-> L5K, either direction.
  naut logix build <project.ACD>   Compile the logic. No controller, no
                                       risk — this is CI for control code.
  naut logix push <project> <rungs.L5X>
                                       Import rungs into a routine offline.
  naut logix push --comm-path <path> <rungs.L5X>
                                       ONLINE EDIT: import rungs into the
                                       program the controller is RUNNING.
                                       Takes no project file — it uploads
                                       what is running, edits that, and with
                                       --accept/--finalize sends it back.
  naut logix download <proj.ACD>   Download a project to a controller.
                                       STOPS it and resets tags; needs --yes.
  naut logix drift <repo.L5X>      Does the controller still match the
                                       repo? --comm-path names the controller.

Import flags:
  --out         Output directory (default ".")
  --types-out   Type declarations file (default "logix_types.st")
  --tags-out    Tag file to emit (default "tags/logix.yaml"); compose it
                with tag-files:
  --scope       Tags to emit: "" for controller-scoped (default), a program
                name, or "*" for every scope
  --skip        Comma-separated globs to leave OUT of the tag file, for tags
                the project declares by hand
  --constants   Include Logix Constant tags (left out by default: the
                controller will not let anything write them)
  --all-types   Also emit the module-defined and product-defined shapes the
                export carries. There are hundreds and none is project code.

Graph flags:
  --aois        Include routines inside Add-On Instruction definitions

Normalize flags:
  -o            Write here instead of stdout
  --pin-ids     Also pin DataExchangeId and ProjectSN. Both are real
                identity and stable across a re-export, so they survive by
                default.
  --drop-l5k    Drop the redundant <Data Format="L5K"> copy of every value.
                Halves the diff of a value change; the result no longer
                imports, so it is for comparison, not round-tripping.
  --check       Compare against a second export and exit 1 if they differ
                once normalized. Drift detection, in one command.
`

func runLogix(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, logixUsage)
		return 2
	}
	switch args[0] {
	case "import":
		return runLogixImport(args[1:])
	case "graph":
		return runLogixGraph(args[1:])
	case "normalize":
		return runLogixNormalize(args[1:])
	case "info":
		return runLogixInfo(args[1:])
	case "probe":
		return runLogixProbe(args[1:])
	case "agent":
		return runLogixAgent(args[1:])
	case "browse":
		return runLogixBrowse(args[1:])
	case "convert":
		return runLogixConvert(args[1:])
	case "build":
		return runLogixBuild(args[1:])
	case "push":
		return runLogixPush(args[1:])
	case "download":
		return runLogixDownload(args[1:])
	case "drift":
		return runLogixDrift(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "naut logix: unknown subcommand %q\n\n%s\n%s",
			args[0], logixUsage, logixAgentUsage)
		return 2
	}
}

func runLogixImport(args []string) int {
	fs := flag.NewFlagSet("logix import", flag.ContinueOnError)
	outDir := fs.String("out", ".", "output directory")
	typesOut := fs.String("types-out", "logix_types.st", "type declarations file")
	tagsOut := fs.String("tags-out", "tags/logix.yaml", "tag file to emit")
	scope := fs.String("scope", "", `tags to emit: "" controller-scoped, a program name, or "*"`)
	skip := fs.String("skip", "", "comma-separated globs to leave OUT of the tag file")
	constants := fs.Bool("constants", false, "include Logix Constant tags")
	allTypes := fs.Bool("all-types", false, "also emit module- and product-defined shapes")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: naut logix import [flags] <file.L5X>")
		return 2
	}
	f, err := l5x.ParseFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut logix import:", err)
		return 1
	}

	// The types the tags actually bind are the roots, so the generated
	// file declares what the tag file refers to and not a controller's
	// worth of shapes nothing uses.
	opts := l5x.TypesOptions{All: *allTypes}
	if !*allTypes {
		opts.Roots = typeRoots(f, *scope)
	}
	src, unresolved, err := l5x.Types(f, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut logix import:", err)
		return 1
	}
	// A tag may only claim a type the generated file declares. One that
	// cannot be rendered — an opaque firmware structure — leaves its tag
	// untyped rather than pointing at something that does not exist.
	known := declaredTypes(src)
	raw, err := l5x.TagsYAML(f, l5x.TagsOptions{
		Scope:      *scope,
		Skip:       splitPatterns(*skip),
		KnownTypes: known,
		Constants:  *constants,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut logix import:", err)
		return 1
	}

	typesPath := filepath.Join(*outDir, *typesOut)
	tagsPath := filepath.Join(*outDir, *tagsOut)
	if err := writeUnder(typesPath, []byte(logixTypesHeader(f)+src)); err != nil {
		fmt.Fprintln(os.Stderr, "naut logix import:", err)
		return 1
	}
	if err := writeUnder(tagsPath, raw); err != nil {
		fmt.Fprintln(os.Stderr, "naut logix import:", err)
		return 1
	}

	for _, u := range unresolved {
		fmt.Fprintf(os.Stderr, "  unresolved type: %s (members of it are omitted, not guessed)\n", u)
	}
	fmt.Printf("wrote %s (%d types) and %s (%d tags)\n",
		typesPath, len(known), tagsPath, countTags(raw))
	fmt.Printf("compose the tags with `tag-files: [%s]`\n", *tagsOut)
	return 0
}

// countTags counts the entries in a rendered tag file.
func countTags(raw []byte) int {
	n := 0
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("- { name:")) {
			n++
		}
	}
	return n
}

// typeRoots collects the types the emitted tags actually bind.
func typeRoots(f *l5x.File, scope string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(t *l5x.Tag) {
		if t.DataType == "" || seen[t.DataType] {
			return
		}
		seen[t.DataType] = true
		out = append(out, t.DataType)
	}
	if f.Controller == nil {
		return nil
	}
	if scope == "" || scope == "*" {
		for _, t := range f.Controller.Tags {
			add(t)
		}
	}
	for _, p := range f.Controller.Programs {
		if scope == "*" || scope == p.Name {
			for _, t := range p.Tags {
				add(t)
			}
		}
	}
	// Every hand-authored UDT comes along regardless: they are the
	// project's own vocabulary, and a project usually wants the type even
	// where no tag of it happens to be declared yet.
	for _, dt := range f.Controller.DataTypes {
		if dt.User() && !seen[dt.Name] {
			seen[dt.Name] = true
			out = append(out, dt.Name)
		}
	}
	sort.Strings(out)
	return out
}

// declaredTypes reads back the names the generated ST actually declares.
func declaredTypes(src string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(src, "\n") {
		name, rest, ok := strings.Cut(strings.TrimSpace(line), " : ")
		if ok && strings.HasPrefix(rest, "STRUCT") {
			out[name] = true
		}
	}
	return out
}

func logixTypesHeader(f *l5x.File) string {
	name := f.TargetName
	if name == "" && f.Controller != nil {
		name = f.Controller.Name
	}
	return fmt.Sprintf(`(* Generated by `+"`naut logix import`"+` from the %s L5X export.
   Do not edit — re-run the import.

   These are the controller's own UDTs as IEC types. A Logix UDT's BOOL
   members are exported as BIT overlays on a hidden byte host; the host is
   dropped and the bits are declared as the BOOLs they were authored as. *)
`, name)
}

func writeUnder(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

// runLogixGraph mirrors `naut ld graph`: the render model as JSON on
// stdout, {"error": "..."} and exit 1 on failure, so an editor can consume
// either without special-casing the source language.
func runLogixGraph(args []string) int {
	fs := flag.NewFlagSet("logix graph", flag.ContinueOnError)
	aois := fs.Bool("aois", false, "include Add-On Instruction routines")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var err error
	enc := json.NewEncoder(os.Stdout)
	if fs.NArg() < 1 || fs.NArg() > 2 {
		fmt.Fprint(os.Stderr, logixUsage)
		return 2
	}
	// "-" reads the export from stdin, exactly as `naut ld graph -`
	// does. That is not a convenience: the revision diff graphs a git blob,
	// which has no path, so without stdin the diff cannot work on L5X.
	var f *l5x.File
	if fs.Arg(0) == "-" {
		raw, rerr := io.ReadAll(os.Stdin)
		if rerr != nil {
			_ = enc.Encode(map[string]string{"error": rerr.Error()})
			return 2
		}
		f, err = l5x.Parse(raw)
	} else {
		f, err = l5x.ParseFile(fs.Arg(0))
	}
	if err != nil {
		_ = enc.Encode(map[string]string{"error": err.Error()})
		return 2
	}
	routine := ""
	if fs.NArg() == 2 {
		routine = fs.Arg(1)
	}
	m, err := l5x.Ladder(f, l5x.LadderOptions{Routine: routine, AOIs: *aois})
	if err != nil {
		_ = enc.Encode(map[string]string{"error": err.Error()})
		return 1
	}
	_ = enc.Encode(m)
	return 0
}

func runLogixNormalize(args []string) int {
	fs := flag.NewFlagSet("logix normalize", flag.ContinueOnError)
	out := fs.String("o", "", "write here instead of stdout")
	pinIDs := fs.Bool("pin-ids", false, "also pin DataExchangeId and ProjectSN")
	dropL5K := fs.Bool("drop-l5k", false, "drop the redundant L5K copy of every value")
	check := fs.String("check", "", "compare against this export and exit 1 if they differ")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: naut logix normalize [flags] <file.L5X>")
		return 2
	}
	opts := l5x.NormalizeOptions{PinIDs: *pinIDs, DropL5K: *dropL5K}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut logix normalize:", err)
		return 1
	}
	if *check != "" {
		other, err := os.ReadFile(*check)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut logix normalize:", err)
			return 1
		}
		if l5x.Equivalent(raw, other, opts) {
			fmt.Printf("%s and %s are the same project\n", fs.Arg(0), *check)
			return 0
		}
		fmt.Printf("%s and %s differ\n", fs.Arg(0), *check)
		return 1
	}
	norm := l5x.Normalize(raw, opts)
	if *out == "" {
		os.Stdout.Write(norm)
		return 0
	}
	if err := writeUnder(*out, norm); err != nil {
		fmt.Fprintln(os.Stderr, "naut logix normalize:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
	return 0
}

func runLogixInfo(args []string) int {
	fs := flag.NewFlagSet("logix info", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: naut logix info <file.L5X>")
		return 2
	}
	f, err := l5x.ParseFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut logix info:", err)
		return 1
	}
	c := f.Controller
	kind := "whole project"
	if f.Partial() {
		kind = "partial export of a " + strings.ToLower(f.TargetType)
	}
	fmt.Printf("%s — %s, Studio 5000 %s\n", f.TargetName, kind, f.SoftwareRevision)
	fmt.Printf("controller %s (%s, v%s.%s), exported %s\n",
		c.Name, c.ProcessorType, c.MajorRev, c.MinorRev, f.ExportDate)

	user, other := 0, 0
	for _, dt := range c.DataTypes {
		if dt.User() {
			user++
		} else {
			other++
		}
	}
	documented := 0
	for _, t := range c.Tags {
		if t.Description != "" {
			documented++
		}
	}
	fmt.Printf("%d user UDTs (%d module- and product-defined), %d AOIs\n", user, other, len(c.AOIs))
	fmt.Printf("%d controller tags, %d documented\n", len(c.Tags), documented)
	for _, p := range c.Programs {
		byType := map[string]int{}
		rungs := 0
		for _, r := range p.Routines {
			byType[r.Type]++
			rungs += len(r.Rungs)
		}
		kinds := make([]string, 0, len(byType))
		for k, n := range byType {
			kinds = append(kinds, fmt.Sprintf("%d %s", n, k))
		}
		sort.Strings(kinds)
		fmt.Printf("program %s: %d tags, %s, %d rungs\n",
			p.Name, len(p.Tags), strings.Join(kinds, ", "), rungs)
	}
	return 0
}
