// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
)

// A capture must collect only what was logged while it was running, and the log must
// reach the underlying writer either way.
//
// The pass-through half is not decoration: the sink is process-wide and installed for
// the whole test binary, so if capturing diverted output instead of copying it, every
// other test in that package would silently lose its logs for the duration.
func TestSinkCapturesOnlyWhileCapturing(t *testing.T) {
	var through bytes.Buffer
	sink := NewLogSink(&through)
	logger := zerolog.New(sink)

	logger.Info().Msg("before")
	logs := sink.Capture(t)
	logger.Info().Msg("during")

	if got := logs.String(); !strings.Contains(got, "during") {
		t.Errorf("the capture missed a line logged while it was running: %q", got)
	}
	if got := logs.String(); strings.Contains(got, "before") {
		t.Errorf("the capture collected a line logged before it started: %q", got)
	}
	for _, want := range []string{"before", "during"} {
		if !strings.Contains(through.String(), want) {
			t.Errorf("%q never reached the writer underneath the sink: %q", want, through.String())
		}
	}
}

// A capture stops when its test ends, so a later log line is not attributed to it.
//
// t.Cleanup runs when the subtest finishes, which is what this exercises: without the
// cleanup the flag would still be set and the assertion below would see "after".
func TestCaptureStopsWithItsTest(t *testing.T) {
	sink := NewLogSink(&bytes.Buffer{})
	logger := zerolog.New(sink)

	var logs *LogSink
	t.Run("inner", func(t *testing.T) {
		logs = sink.Capture(t)
		logger.Info().Msg("inside")
	})
	logger.Info().Msg("after")

	if got := logs.String(); strings.Contains(got, "after") {
		t.Errorf("the capture was still collecting after its test ended: %q", got)
	}
}

// Concurrent writers must not race the capture switch.
//
// This is the property the whole sink exists for, and it is only meaningful under
// -race: the writers here are the stand-in for the NATS client's callback goroutine,
// which logs on its own schedule while a test starts and stops collecting. The
// pass-through writer is deliberately a bytes.Buffer rather than a file — passing the
// line through outside the lock is safe only in front of an os.File, and this is what
// catches that mistake.
func TestSinkIsSafeUnderConcurrentWriters(t *testing.T) {
	sink := NewLogSink(&bytes.Buffer{})
	logger := zerolog.New(sink)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					logger.Info().Msg("from a goroutine the test does not own")
				}
			}
		}()
	}
	for i := range 50 {
		t.Run(fmt.Sprintf("capture-%d", i), func(t *testing.T) {
			logs := sink.Capture(t)
			_ = logs.String()
		})
	}
	close(stop)
	wg.Wait()
}

// Every way of writing the global logger must be reported, and code that only reads it
// must not be.
//
// The dirty cases are not a list of exotica. Each one reaches the same unsynchronized
// variable and so carries the same race, and an earlier version of this scanner —
// which matched only an assignment whose left-hand side was a bare selector — caught
// none of them. UpdateContext is the one most likely to be written by accident, since
// adding a field to the global logger does not look like swapping it.
//
// The clean cases are the counterweight: a scanner that reported everything would pass
// every dirty case and be useless.
func TestGlobalLoggerSwapScanner(t *testing.T) {
	const header = `package fixture

import (
	"testing"

	"github.com/rs/zerolog"
	%s
)

var _ = zerolog.Nop
var _ = testing.Verbose

`
	dirty := map[string]string{
		"a plain assignment": fmt.Sprintf(header, `"github.com/rs/zerolog/log"`) + `
func TestSwap(t *testing.T) {
	log.Logger = zerolog.New(nil)
}
`,
		"an assignment through an import alias": fmt.Sprintf(header, `zl "github.com/rs/zerolog/log"`) + `
func TestSwap(t *testing.T) {
	prev := zl.Logger
	zl.Logger = zerolog.New(nil)
	t.Cleanup(func() { zl.Logger = prev })
}
`,
		"a dot-import, reported on its own": fmt.Sprintf(header, `. "github.com/rs/zerolog/log"`) + `
func TestSwap(t *testing.T) {
	Logger = zerolog.New(nil)
}
`,
		"a mutating method call": fmt.Sprintf(header, `"github.com/rs/zerolog/log"`) + `
func TestSwap(t *testing.T) {
	log.Logger.UpdateContext(func(c zerolog.Context) zerolog.Context { return c })
}
`,
		"a parenthesized assignment": fmt.Sprintf(header, `"github.com/rs/zerolog/log"`) + `
func TestSwap(t *testing.T) {
	(log.Logger) = zerolog.New(nil)
}
`,
		"a write through a pointer": fmt.Sprintf(header, `"github.com/rs/zerolog/log"`) + `
func TestSwap(t *testing.T) {
	p := &log.Logger
	*p = zerolog.New(nil)
}
`,
		"the address handed to a setter": fmt.Sprintf(header, `"github.com/rs/zerolog/log"`) + `
func set(dst *zerolog.Logger, v zerolog.Logger) { *dst = v }

func TestSwap(t *testing.T) {
	set(&log.Logger, zerolog.New(nil))
}
`,
		"a range clause assigning into it": fmt.Sprintf(header, `"github.com/rs/zerolog/log"`) + `
func TestSwap(t *testing.T) {
	var i int
	for i, log.Logger = range []zerolog.Logger{zerolog.New(nil)} {
	}
	_ = i
}
`,
	}
	clean := map[string]string{
		"a package that only reads the logger": fmt.Sprintf(header, `"github.com/rs/zerolog/log"`) + `
// log.Logger = zerolog.New(nil) in a comment must not count.
func TestReads(t *testing.T) {
	_ = log.Logger
	log.Info().Msg("hello")
}
`,
		"a field that happens to be called Logger": fmt.Sprintf(header, `"github.com/rs/zerolog/log"`) + `
type other struct{ Logger int }

func TestOther(t *testing.T) {
	var o other
	o.Logger = 1
	log.Info().Msg("hello")
}
`,
		"a blank import": fmt.Sprintf(header, `_ "github.com/rs/zerolog/log"`) + `
func TestBlank(t *testing.T) {
	_ = zerolog.New(nil)
}
`,
		"a derived logger, which copies rather than writes": fmt.Sprintf(header, `"github.com/rs/zerolog/log"`) + `
func TestDerives(t *testing.T) {
	local := log.Logger.Output(nil).With().Str("k", "v").Logger()
	local.Info().Msg("hello")
}
`,
	}

	for name, source := range dirty {
		t.Run("caught: "+name, func(t *testing.T) {
			if got := scanFixture(t, source); len(got) == 0 {
				t.Errorf("the scanner found nothing in a fixture that writes the global logger:\n%s", source)
			}
		})
	}
	for name, source := range clean {
		t.Run("not flagged: "+name, func(t *testing.T) {
			if got := scanFixture(t, source); len(got) != 0 {
				t.Errorf("the scanner reported %v in a fixture that writes nothing:\n%s", got, source)
			}
		})
	}
}

// scanFixture writes source as the only _test.go file of a temp directory and returns
// what the scanner found. It parses the fixture first, so a fixture with a syntax
// error fails as a broken fixture rather than passing as a clean scan.
func scanFixture(t *testing.T, source string) []loggerWrite {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture_test.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	found, err := globalLoggerSwaps(dir)
	if err != nil {
		t.Fatalf("scanning the fixture: %v", err)
	}
	return found
}

// The tree walk must find a swap nested any distance below the root, must refuse to
// descend into the directories the go tool itself does not treat as packages, and must
// report where it actually got to.
//
// The nesting case is the one that matters. The scan this replaced took a single
// directory, which is why three packages went on swapping the logger unnoticed — nothing
// pointed it at them — so a version that walked only the top level would reproduce the
// same blindness while looking like a fix.
//
// The skip case is not tidiness either. This repository carries an archived pre-migration
// tree under _legacy that is deliberately not maintained, and a maintainer's git
// worktrees live under a dot-directory — those hold a DIFFERENT commit of this
// repository, so scanning them would report another branch's code as a finding against
// this one.
func TestTreeScanWalksNestedPackagesAndSkipsNonPackageDirectories(t *testing.T) {
	root := t.TempDir()
	const swap = `package fixture

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func TestSwap(t *testing.T) {
	log.Logger = zerolog.New(nil)
}
`
	nested := filepath.Join(root, "a", "b", "c")
	for _, dir := range []string{nested, filepath.Join(root, "_legacy"), filepath.Join(root, ".worktree"), filepath.Join(root, "testdata")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("building the fixture tree: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fixture_test.go"), []byte(swap), 0o600); err != nil {
			t.Fatalf("writing the fixture: %v", err)
		}
	}

	found, visited, err := globalLoggerSwapsUnder(root)
	if err != nil {
		t.Fatalf("scanning the fixture tree: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("got %d findings, want exactly 1 — the nested swap and nothing from the "+
			"skipped directories: %v", len(found), found)
	}
	if !strings.Contains(found[0].pos, filepath.Join("a", "b", "c")) {
		t.Errorf("the one finding is %s, want the nested one; a walk that reports only the "+
			"top level is the blindness this replaced", found[0].pos)
	}
	if !visited[nested] {
		t.Errorf("the walk did not record reaching %s, so the caller's cross-check on where "+
			"it got to would be satisfied by a walk that went nowhere", nested)
	}
	for _, skipped := range []string{"_legacy", ".worktree", "testdata"} {
		if visited[filepath.Join(root, skipped)] {
			t.Errorf("the walk descended into %s, which is not a package directory", skipped)
		}
	}
}

// A tree with no tests under it at all must be an error, not a clean scan — the same
// rule as the single-directory form, and for the same reason: a walk that reached
// nothing otherwise reports exactly what a walk that found nothing wrong reports.
func TestTreeScanRefusesATreeWithNoTests(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o750); err != nil {
		t.Fatalf("building the fixture tree: %v", err)
	}
	if _, _, err := globalLoggerSwapsUnder(root); err == nil {
		t.Error("scanning a tree with no _test.go files returned no error, so a workspace " +
			"the guard never examined would report clean")
	}
}

// Installing the sink a second time must be refused.
//
// 🔑 THIS IS THE HALF OF THE CONTRACT THE SOURCE SCAN CANNOT ENFORCE. That scan reads
// _test.go files, so a swap performed on a test's behalf by a helper in some other
// package is invisible to it — and this function IS such a helper. Left unguarded it
// would be the obvious route back to the pattern it replaced: a captureLogs that called
// InstallLogSink per test would write the global logger once per test, pass every
// source check, and race exactly as the swap-and-restore it replaced did. Refusing the
// second install closes that from the other side, so the helper cannot be turned into
// the standard way of reaching the global logger.
//
// The first call may or may not panic depending on whether this package has already
// installed one; only what happens AFTER an install is the contract, so the first call
// is made tolerantly and the assertion is on the one after it.
func TestInstallLogSinkRefusesASecondInstall(t *testing.T) {
	recoverInstall()
	if r := recoverInstall(); r == nil {
		t.Fatal("InstallLogSink returned a second sink instead of panicking. Every call " +
			"assigns zerolog's global logger, which is safe only before any test has " +
			"started, so a second one is the per-test swap this package exists to remove")
	} else if msg := fmt.Sprint(r); !strings.Contains(msg, "already installed") {
		t.Errorf("the refusal panicked with %q, which does not say what went wrong", msg)
	}
}

// recoverInstall calls InstallLogSink and returns whatever it panicked with, or nil.
func recoverInstall() (recovered any) {
	defer func() { recovered = recover() }()
	InstallLogSink()
	return nil
}

// Pointing the guard at a directory with no tests must be an error, not a pass.
// Otherwise a package that renamed or moved its test files would report clean.
func TestScannerRefusesADirectoryWithNoTests(t *testing.T) {
	if _, err := globalLoggerSwaps(t.TempDir()); err == nil {
		t.Error("scanning a directory with no _test.go files returned no error, so the " +
			"guard would report a package it never examined as clean")
	}
}
