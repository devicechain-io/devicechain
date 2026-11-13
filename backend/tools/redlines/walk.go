// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package redlines holds the static guards behind two platform invariants:
//
//   - declarative-only: there is no tenant code runtime. A scripting engine or a
//     WebAssembly runtime anywhere in the dependency graph is the first step toward
//     running tenant-supplied code, so the repository refuses to name one (ScanDeps).
//   - agent-visible text is platform-authored: the remediation text an error gives an
//     agent is a compile-time constant, never formatted from runtime data
//     (ScanRemediation).
//
// Both read source rather than run it, so a guard cannot be satisfied by code that never
// executes in CI.
package redlines

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// skipAlways names directories no guard reads: version control, installed JS packages,
// the archived pre-migration tree and the private planning workspace.
func skipAlways(name string) bool {
	switch name {
	case ".git", "node_modules", "_legacy", ".agent-os":
		return true
	}
	return false
}

// skipFixtures also skips testdata, which exists to hold forbidden shapes for Go tests.
// Only Go source is read with it: a go.mod or go.sum inside testdata is still read,
// because a go.work can `use` such a directory and then it is part of the build.
func skipFixtures(name string) bool { return skipAlways(name) || name == "testdata" }

// walk calls fn for every regular file under root that is not inside a skipped
// directory. Paths passed to fn are slash-separated and relative to the process working
// directory when root is.
func walk(root string, skip func(string) bool, fn func(path string) error) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skip(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return fn(filepath.ToSlash(p))
	})
}

// moduleOf returns the module path declared by a go.mod, or "" if there is none.
func moduleOf(gomod string) string {
	b, err := os.ReadFile(gomod)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(stripComment(line))
		if len(f) == 2 && f[0] == "module" {
			return strings.Trim(f[1], "\"`")
		}
	}
	return ""
}

func stripComment(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		return line[:i]
	}
	return line
}
