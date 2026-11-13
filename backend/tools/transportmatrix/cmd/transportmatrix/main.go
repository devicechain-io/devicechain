// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command transportmatrix renders the transport capability matrix's symbol cells from the conformance
// rig's results, or checks that the published pages already match.
//
//	transportmatrix check   -spec S -results R -docs a.md,b.md,c.md [-max-age 1080h] [-repo DIR]
//	transportmatrix render  (same flags; rewrites the pages in place)
//	transportmatrix digest  -spec S         (prints the digest the rig records in the results)
//	transportmatrix verify  -spec S -results R [-max-age] [-repo DIR]   (the results file alone)
//
// Exit status: 0 consistent, 1 the pages or the results are wrong, 2 usage or an unreadable input.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tm "github.com/devicechain-io/dc-transportmatrix"
)

// The three pages: the English one and its two translations. The symbols are the same in all three, so
// the generator owns them in all three and a translator never has to.
const defaultDocs = "docs/docs/reference/transport-matrix.md," +
	"docs/i18n/es/docusaurus-plugin-content-docs/current/reference/transport-matrix.md," +
	"docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/reference/transport-matrix.md"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, out, errw interface{ Write([]byte) (int, error) }) int {
	if len(args) == 0 {
		fmt.Fprintln(errw, "usage: transportmatrix check|render|digest|verify [flags]")
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(errw)
	specPath := fs.String("spec", "hack/conformance/matrix.json", "the spec")
	resultsPath := fs.String("results", "hack/conformance/results.json", "the rig's results")
	docs := fs.String("docs", defaultDocs, "comma-separated pages to check or render")
	maxAge := fs.Duration("max-age", 0, "refuse results older than this (0 skips the check; the release gate sets it)")
	repo := fs.String("repo", "", "a git checkout: refuse results whose commit is not an ancestor of HEAD there")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	spec, err := tm.LoadSpec(*specPath)
	if err != nil {
		fmt.Fprintln(errw, err)
		return 2
	}
	if cmd == "digest" {
		fmt.Fprintln(out, spec.Digest())
		return 0
	}
	res, err := tm.LoadResults(*resultsPath)
	if err != nil {
		fmt.Fprintln(errw, err)
		return 2
	}
	opts := tm.Options{MaxAge: *maxAge}
	if *repo != "" {
		opts.IsAncestor = func(commit string) (bool, error) { return gitIsAncestor(*repo, commit) }
	}
	passed, err := res.Verify(spec, opts)
	if err != nil {
		fmt.Fprintln(errw, err)
		return 1
	}
	if err := tm.CheckCells(spec, passed); err != nil {
		fmt.Fprintln(errw, err)
		return 1
	}
	if cmd == "verify" {
		fmt.Fprintf(out, "results ok: commit %.12s, %s, %d checks, %d controls red\n", res.Commit, res.GeneratedAt, len(res.Checks), len(res.Controls))
		return 0
	}
	if cmd != "check" && cmd != "render" {
		fmt.Fprintf(errw, "unknown command %q\n", cmd)
		return 2
	}

	status := 0
	for _, path := range strings.Split(*docs, ",") {
		path = strings.TrimSpace(path)
		raw, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(errw, err)
			return 2
		}
		have := string(raw)
		want, err := tm.Render(have, spec)
		if err != nil {
			fmt.Fprintf(errw, "%s: %v\n", path, err)
			status = 1
			continue
		}
		if want == have {
			fmt.Fprintf(out, "%s: matches the results\n", path)
			continue
		}
		if cmd == "render" {
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				fmt.Fprintln(errw, err)
				return 2
			}
			fmt.Fprintf(out, "%s: rewritten\n", path)
			continue
		}
		fmt.Fprintf(errw, "%s: the symbol cells do not match what the conformance spec and results say. "+
			"They are generated; do not edit them by hand. Run `hack/check-transport-matrix.sh render` to rewrite them.\n", path)
		for _, d := range tm.Diff(have, want) {
			fmt.Fprintln(errw, "  "+d)
		}
		status = 1
	}
	return status
}

func gitIsAncestor(repo, commit string) (bool, error) {
	cmd := exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", commit, "HEAD")
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}
