// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command vocabgen turns pinned upstream vocabulary files into the generated
// tables of backend/core/semantic. It is a maintainer tool and is not shipped:
// it is the one place in the repository allowed to parse RDF, so that nothing on
// a request path ever does.
//
// Inputs are listed in hack/vocab/sources.lock as `name sha256 url`. Every input
// is verified against its digest before it is read, and a mismatch stops the run
// -- an upstream file that changed under a pinned URL is a decision for a person
// to read, never something to absorb. Two runs over the same inputs write
// byte-identical files.
//
//	cd backend/tools/vocabgen
//	go run . -root ../../.. -fetch          # regenerate
//	go run . -root ../../.. -check          # fail if the committed files differ
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "vocabgen:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("vocabgen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root")
	lockPath := fs.String("lock", "", "source lock (default <root>/hack/vocab/sources.lock)")
	cache := fs.String("cache", filepath.Join(os.TempDir(), "dc-vocabgen-cache"), "directory holding downloaded inputs")
	fetch := fs.Bool("fetch", false, "download inputs missing from the cache (still hash-verified)")
	check := fs.Bool("check", false, "write nothing; fail if the committed files differ from what the inputs generate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *lockPath == "" {
		*lockPath = filepath.Join(*root, "hack", "vocab", "sources.lock")
	}
	semDir := filepath.Join(*root, "backend", "core", "semantic")

	f, err := os.Open(*lockPath)
	if err != nil {
		return err
	}
	lock, err := parseLock(f)
	f.Close()
	if err != nil {
		return err
	}
	var lockNames []string
	for n := range lock {
		lockNames = append(lockNames, n)
	}
	l := &loader{lock: lock, cache: *cache, fetch: *fetch}
	in := map[string]source{}
	for _, name := range requiredInputs(lockNames) {
		s, err := l.get(name)
		if err != nil {
			return err
		}
		in[name] = s
	}

	files, report, err := generate(in)
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, report)

	var rels []string
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	if *check {
		var differ []string
		for _, rel := range rels {
			disk, err := os.ReadFile(filepath.Join(semDir, filepath.FromSlash(rel)))
			if err != nil || !bytes.Equal(disk, files[rel]) {
				differ = append(differ, rel)
			}
		}
		stale, err := staleGenerated(semDir, files)
		if err != nil {
			return err
		}
		differ = append(differ, stale...)
		if len(differ) > 0 {
			return fmt.Errorf("generated files are out of date with the pinned inputs: %s", strings.Join(differ, ", "))
		}
		fmt.Fprintln(stdout, "generated files are current")
		return nil
	}

	stale, err := staleGenerated(semDir, files)
	if err != nil {
		return err
	}
	for _, rel := range stale {
		if err := os.Remove(filepath.Join(semDir, filepath.FromSlash(rel))); err != nil {
			return err
		}
	}
	for _, rel := range rels {
		path := filepath.Join(semDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, files[rel], 0o644); err != nil {
			return err
		}
	}
	return nil
}

// staleGenerated lists generated files on disk that this run no longer
// produces (a pack that was dropped), as paths relative to the semantic dir.
func staleGenerated(semDir string, files map[string][]byte) ([]string, error) {
	var stale []string
	for _, pattern := range []string{"packs/*_gen.go", "contexts/*.jsonld"} {
		matches, err := filepath.Glob(filepath.Join(semDir, filepath.FromSlash(pattern)))
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			rel, err := filepath.Rel(semDir, m)
			if err != nil {
				return nil, err
			}
			rel = filepath.ToSlash(rel)
			if _, ok := files[rel]; !ok {
				stale = append(stale, rel)
			}
		}
	}
	sort.Strings(stale)
	return stale, nil
}
