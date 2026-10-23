// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command apicompat is the API-stability gate for the surfaces beyond GraphQL (which
// cmd/sdlcompat covers): the dcctl command tree, the device wire contract, the device
// topic layout, and - report only - the Helm values schema and the Instance CRDs.
//
//	apicompat cli      -baseline vX.Y.Z (-base <tree> | -base-json <file>) [-head <tree> | -head-json <file>]
//	apicompat topics   -baseline vX.Y.Z (-base <tree> | -base-json <file>) [-head <tree> | -head-json <file>]
//	apicompat contract -baseline vX.Y.Z -base <tree> [-head <tree>]
//	apicompat reports  -base <tree> [-head <tree>]
//	apicompat export-cli    -tree <tree> -out <file>
//	apicompat export-topics -tree <tree> -out <file>
//
// A <tree> is a directory laid out like the repository (hack/check-cli-compat.sh and
// hack/check-schema-compat.sh extract the baseline release into one with git archive);
// -head defaults to the repository root the command runs under. Exit status: 0 the
// surface is compatible (or, for reports, always), 1 a break not declared (or a stale
// or invalid declaration), 2 the check could not run - the wrappers keep the two apart.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/devicechain-io/dc-docsgql/apicompat/clitree"
	"github.com/devicechain-io/dc-docsgql/apicompat/decl"
	"github.com/devicechain-io/dc-docsgql/apicompat/export"
	"github.com/devicechain-io/dc-docsgql/apicompat/jsonschema"
	"github.com/devicechain-io/dc-docsgql/apicompat/topics"
)

const (
	cliBreaks      = "backend/tools/docsgql/apicompat/clitree/breaks.txt"
	contractBreaks = "backend/tools/docsgql/apicompat/jsonschema/breaks.txt"
	topicsBreaks   = "backend/tools/docsgql/apicompat/topics/breaks.txt"

	valuesSchema = "deploy/helm/devicechain/values.schema.json"
	crdDir       = "backend/k8s/config/crd/bases"
)

func main() {
	if len(os.Args) < 2 {
		fail(fmt.Errorf("usage: apicompat cli|topics|contract|reports|export-cli|export-topics ..."))
	}
	sub, args := os.Args[1], os.Args[2:]
	switch sub {
	case "cli":
		runTree(args, "dcctl command tree", cliBreaks, clitree.Kinds, export.CommandTree,
			func(base, head string) ([]decl.Change, error) {
				b, err := clitree.Read(base)
				if err != nil {
					return nil, fmt.Errorf("baseline: %w", err)
				}
				h, err := clitree.Read(head)
				if err != nil {
					return nil, fmt.Errorf("working tree: %w", err)
				}
				return clitree.Compare(b, h), nil
			})
	case "topics":
		runTree(args, "device topic layout", topicsBreaks, topics.Kinds, export.DeviceTopics,
			func(base, head string) ([]decl.Change, error) {
				b, err := topics.Read(base)
				if err != nil {
					return nil, fmt.Errorf("baseline: %w", err)
				}
				h, err := topics.Read(head)
				if err != nil {
					return nil, fmt.Errorf("working tree: %w", err)
				}
				return topics.Compare(b, h), nil
			})
	case "contract":
		runContract(args)
	case "reports":
		runReports(args)
	case "export-cli", "export-topics":
		fs := flag.NewFlagSet(sub, flag.ExitOnError)
		tree := fs.String("tree", "", "repository tree to export from")
		out := fs.String("out", "", "file to write")
		_ = fs.Parse(args)
		if *tree == "" || *out == "" {
			fail(fmt.Errorf("-tree and -out are both required"))
		}
		fn := export.CommandTree
		if sub == "export-topics" {
			fn = export.DeviceTopics
		}
		if err := fn(*tree, *out); err != nil {
			fail(err)
		}
	default:
		fail(fmt.Errorf("unknown subcommand %q", sub))
	}
}

// runTree is the shape cli and topics share: export (or read) both sides, compare,
// judge against the declarations.
func runTree(args []string, what, breaksRel string, kinds map[string]decl.Severity,
	exporter func(root, out string) error, compare func(base, head string) ([]decl.Change, error)) {
	fs := flag.NewFlagSet("tree", flag.ExitOnError)
	baseline := fs.String("baseline", "", "the baseline release tag, e.g. v0.24.0")
	base := fs.String("base", "", "the baseline release's tree")
	baseJSON := fs.String("base-json", "", "an already exported baseline (instead of -base)")
	head := fs.String("head", "../../..", "the working tree (default: the repository root)")
	headJSON := fs.String("head-json", "", "an already exported working tree (instead of -head)")
	breaks := fs.String("breaks", "", "the declarations file (default <head>/"+breaksRel+")")
	verbose := fs.Bool("v", false, "also list safe changes")
	_ = fs.Parse(args)
	if *baseline == "" || (*base == "") == (*baseJSON == "") {
		fail(fmt.Errorf("-baseline and exactly one of -base / -base-json are required"))
	}

	scratch, err := os.MkdirTemp("", "apicompat-")
	if err != nil {
		fail(err)
	}
	defer os.RemoveAll(scratch)

	side := func(label, tree, jsonFile string) string {
		if jsonFile != "" {
			return jsonFile
		}
		out := filepath.Join(scratch, label+".json")
		fmt.Printf("exporting the %s %s ...\n", label, what)
		if err := exporter(tree, out); err != nil {
			fail(fmt.Errorf("%s: %w", label, err))
		}
		return out
	}
	baseFile := side("baseline", *base, *baseJSON)
	headFile := side("working-tree", *head, *headJSON)

	changes, err := compare(baseFile, headFile)
	if err != nil {
		fail(err)
	}
	judge(what, *baseline, *head, breaksRel, *breaks, kinds, changes, *verbose)
}

// judge evaluates changes against the declarations and exits 1 on any failure.
func judge(what, baseline, root, breaksRel, breaksOverride string, kinds map[string]decl.Severity, changes []decl.Change, verbose bool) {
	path := breaksOverride
	if path == "" {
		path = filepath.Join(root, filepath.FromSlash(breaksRel))
	}
	f, err := os.Open(path)
	if err != nil {
		// A missing declarations file is an error, not an empty list: deleting it must
		// not be a way to change what a declaration means.
		fail(fmt.Errorf("read the declarations: %w", err))
	}
	decls, bad := decl.Parse(f, breaksRel, kinds)
	f.Close()
	res := decl.Evaluate(baseline, changes, decls)
	failed := decl.Print(os.Stdout, what, baseline, breaksRel, bad, res, verbose)
	fmt.Printf("apicompat: %s against %s: %d breaking (%d declared), %d dangerous, %d safe; %d failed\n",
		what, baseline, len(res.Undeclared)+len(res.Declared), len(res.Declared), len(res.Dangerous), len(res.Safe), failed)
	// The verdict is the Result's, the one the unit tests pin; Print's count is what the
	// summary line says. Two tallies that each decided would be two ways to disagree.
	if res.Failed() || len(bad) > 0 {
		os.Exit(1)
	}
}

func runContract(args []string) {
	fs := flag.NewFlagSet("contract", flag.ExitOnError)
	baseline := fs.String("baseline", "", "the baseline release tag")
	base := fs.String("base", "", "the baseline release's tree")
	head := fs.String("head", "../../..", "the working tree")
	breaks := fs.String("breaks", "", "the declarations file (default <head>/"+contractBreaks+")")
	verbose := fs.Bool("v", false, "also list safe changes")
	_ = fs.Parse(args)
	if *baseline == "" || *base == "" {
		fail(fmt.Errorf("-baseline and -base are both required"))
	}
	bs, err := discoverContracts(*base)
	if err != nil {
		fail(fmt.Errorf("baseline %s: %w", *baseline, err))
	}
	hs, err := discoverContracts(*head)
	if err != nil {
		fail(fmt.Errorf("working tree: %w", err))
	}
	var changes []decl.Change
	for _, name := range sortedNames(bs) {
		h, ok := hs[name]
		if !ok {
			changes = append(changes, decl.Change{Surface: name, Coord: "$", Kind: "schema-removed", Severity: decl.Breaking,
				Detail: "the contract file is gone (moved or renamed counts: the published $id and its consumers key on the file)"})
			continue
		}
		bdoc, err := jsonschema.Load(bs[name])
		if err != nil {
			fail(err)
		}
		hdoc, err := jsonschema.Load(h)
		if err != nil {
			fail(err)
		}
		changes = append(changes, jsonschema.Compare(name, bdoc, hdoc)...)
	}
	for _, name := range sortedNames(hs) {
		if _, ok := bs[name]; !ok {
			changes = append(changes, decl.Change{Surface: name, Coord: "$", Kind: "schema-added", Severity: decl.Safe})
		}
	}
	fmt.Printf("device contract: %d schema files at %s, %d now\n", len(bs), *baseline, len(hs))
	judge("device wire contract", *baseline, *head, contractBreaks, *breaks, jsonschema.Kinds, changes, *verbose)
}

// discoverContracts finds every device/command contract schema under a tree, keyed by
// file name. Finding none is an error: a glob that matches nothing - because the
// directory was renamed - would otherwise compare nothing and pass.
func discoverContracts(root string) (map[string]string, error) {
	files, err := filepath.Glob(filepath.Join(root, "backend", "services", "*", "contract", "*.schema.json"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no backend/services/*/contract/*.schema.json under %s; nothing would be compared", root)
	}
	m := map[string]string{}
	for _, f := range files {
		name := filepath.Base(f)
		if prev, dup := m[name]; dup {
			return nil, fmt.Errorf("two contract files are both named %s (%s and %s); a declaration names the file", name, prev, f)
		}
		m[name] = f
	}
	return m, nil
}

func runReports(args []string) {
	fs := flag.NewFlagSet("reports", flag.ExitOnError)
	base := fs.String("base", "", "the baseline release's tree")
	head := fs.String("head", "../../..", "the working tree")
	_ = fs.Parse(args)
	if *base == "" {
		fail(fmt.Errorf("-base is required"))
	}
	total := 0
	show := func(label string, cs []decl.Change) {
		decl.Sort(cs)
		counts := map[decl.Severity]int{}
		for _, c := range cs {
			counts[c.Severity]++
			if c.Severity != decl.Safe {
				fmt.Printf("REPORT %-9s %s\n", c.Severity, c)
			}
		}
		total += counts[decl.Breaking] + counts[decl.Dangerous]
		fmt.Printf("report-only: %s: %d breaking, %d dangerous, %d safe\n", label, counts[decl.Breaking], counts[decl.Dangerous], counts[decl.Safe])
	}

	// Helm values schema.
	bv, bvErr := jsonschema.Load(filepath.Join(*base, filepath.FromSlash(valuesSchema)))
	hv, hvErr := jsonschema.Load(filepath.Join(*head, filepath.FromSlash(valuesSchema)))
	switch {
	case hvErr != nil && os.IsNotExist(hvErr):
		fmt.Printf("REPORT breaking  %s: the file no longer exists\n", valuesSchema)
	case bvErr != nil && os.IsNotExist(bvErr):
		fmt.Printf("report-only: %s: new since the baseline\n", valuesSchema)
	case bvErr != nil:
		fail(bvErr)
	case hvErr != nil:
		fail(hvErr)
	default:
		show("Helm values schema", jsonschema.Compare("values.schema.json", bv, hv))
	}

	// Instance CRDs, one report per file and version.
	bfiles, _ := filepath.Glob(filepath.Join(*base, filepath.FromSlash(crdDir), "*.yaml"))
	hfiles, _ := filepath.Glob(filepath.Join(*head, filepath.FromSlash(crdDir), "*.yaml"))
	if len(hfiles) == 0 {
		fail(fmt.Errorf("no CRDs under %s in the working tree; nothing would be compared", crdDir))
	}
	have := map[string]string{}
	for _, f := range hfiles {
		have[filepath.Base(f)] = f
	}
	for _, bf := range bfiles {
		name := filepath.Base(bf)
		hf, ok := have[name]
		if !ok {
			fmt.Printf("REPORT breaking  %s: the CRD no longer exists\n", name)
			continue
		}
		bcrd, err := jsonschema.LoadCRD(bf)
		if err != nil {
			fail(fmt.Errorf("baseline: %w", err))
		}
		hcrd, err := jsonschema.LoadCRD(hf)
		if err != nil {
			fail(fmt.Errorf("working tree: %w", err))
		}
		for _, ver := range jsonschema.SortedVersions(bcrd) {
			h, ok := hcrd[ver]
			if !ok {
				fmt.Printf("REPORT breaking  %s: version %s is gone\n", name, ver)
				continue
			}
			show("CRD "+name+" "+ver, jsonschema.Compare(name+"#"+ver, bcrd[ver], h))
		}
	}
	fmt.Printf("apicompat: report-only surfaces (not covered by the versioning policy): %d change(s) to mention in the release highlights; never fails\n", total)
}

func sortedNames(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "apicompat:", err)
	os.Exit(2)
}
