// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// No test ANYWHERE in this repository may write the global zerolog logger.
//
// The race a swap reintroduces is only visible under `go test -race`, which the
// required CI gates do not run — so without this check the next swap would be caught
// by nobody until someone ran the race detector by hand while chasing something else,
// and its report would name whichever test happened to be running when a callback
// goroutine fired rather than the test that did the swapping.
//
// 🔴 IT IS DELIBERATELY NOT A PER-PACKAGE OPT-IN. The first version of this guard took
// a directory and was called from the one package that had just been fixed, which left
// every other package outside its view — and three of them went on swapping the logger
// for exactly as long as that was true. A guard a package has to be pointed at only
// ever covers the packages someone remembered, and the one it needs to cover next is by
// definition the one nobody has thought about yet.
//
// Two consequences worth knowing before this fails on you:
//
//   - It reads files outside this module, so `-count=1` is load-bearing. Go's test cache
//     does not track them, and a cached PASS would survive a swap added elsewhere. CI
//     passes it on every module.
//   - A swap added in a service module is reported by THIS module's test run. That reads
//     oddly the first time; the message names the offending file and line.
func TestNoTestInTheRepositorySwapsTheGlobalLogger(t *testing.T) {
	root := workspaceRoot(t)
	AssertNoGlobalLoggerSwapUnder(t, root, workspaceModuleDirs(t, root))
}

// workspaceRoot returns the directory holding go.work, found by ascending from this
// package. It FAILS rather than falling back to the working directory: a guard that
// quietly scanned one package instead of the workspace would report clean for the same
// reason a correct scan does, which is the failure this whole check exists to avoid.
func workspaceRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("locating the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.work found above %s, so there is no workspace to scan and this "+
				"check would assert nothing", dir)
		}
		dir = parent
	}
}

// workspaceModuleDirs returns the module directories go.work names, as absolute paths.
//
// It is the SECOND, INDEPENDENT authority the scan is cross-checked against, and that
// is its whole purpose: a walk that reached nothing returns the same empty result as a
// walk that found nothing wrong, so the set of places the walk must have got to is read
// from the file that defines what this workspace is rather than from the walk itself. A
// module added to go.work is covered without anyone editing this test.
func workspaceModuleDirs(t *testing.T, root string) []string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		t.Fatalf("reading go.work: %v", err)
	}
	var dirs []string
	inBlock := false
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "":
		case inBlock && line == ")":
			inBlock = false
		case inBlock:
			dirs = append(dirs, filepath.Join(root, filepath.FromSlash(line)))
		case line == "use (":
			inBlock = true
		case strings.HasPrefix(line, "use "):
			dirs = append(dirs, filepath.Join(root, filepath.FromSlash(strings.TrimSpace(line[len("use "):]))))
		}
	}
	if len(dirs) == 0 {
		t.Fatal("go.work names no modules, so the cross-check on where the scan reached " +
			"would be satisfied by a scan that reached nowhere")
	}
	return dirs
}
