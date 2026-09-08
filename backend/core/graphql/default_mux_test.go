// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// servicesDir is this module's view of the sibling service modules.
//
// 🔴 READING OUTSIDE THE MODULE MEANS THE TEST CACHE CANNOT SEE THESE FILES. Go's cache
// keys on inputs it knows about, and another module's source is not one of them — so a
// cached PASS survives a change that must fail. This test is only honest under
// -count=1, which CI passes. There is precedent for the technique and the caveat
// (cli/sim/handshake_lockstep_test.go reads the other module's source the same way).
const servicesDir = "../../services"

// minServiceCount is a floor, not the exact count, so adding a service does not fail
// this test for the wrong reason. It exists to make the scan REFUSE TO PASS VACUOUSLY:
// a wrong path, a renamed directory or a read error would otherwise leave the loop with
// nothing to look at and report every service clean.
const minServiceCount = 12

// defaultMuxRegistration matches the shapes that put a route on http.DefaultServeMux.
//
// It matches broadly on purpose. http.Handle and http.HandleFunc are the obvious two;
// naming DefaultServeMux at all is the third, and it is the one a grep for the first
// two misses — three registrars in this repo took it as a *http.ServeMux argument,
// which is how those routes stayed invisible in the first enumeration of this work.
//
// ⚠️ WHAT IT CANNOT SEE, stated plainly rather than left for a reader to discover, and
// all three of these have been demonstrated to compile and slip past:
//
//   - an alias: `var reg = http.Handle` and then `reg("/x", h)`
//   - a line split: `http.` on one line, `HandleFunc(` on the next, which gofmt keeps
//   - anything outside services/, since the walk covers only that tree — a helper added
//     under core/ would not be scanned at all
//
// It is a regexp over source text, not a type-checked analysis; a dot-import of
// net/http would evade it too. This is a cheap net for the shapes that actually occur,
// not a proof, and it is deliberately the FAST LOCAL SIGNAL rather than the authority.
// A repository-wide guard that handles the three evasions above does not exist yet —
// it is the next piece of this work — so until it lands this test is the only automated
// coverage there is, which is a reason to keep its limits visible rather than to trust
// its silence.
var defaultMuxRegistration = regexp.MustCompile(`\bhttp\.Handle(Func)?\(|\bhttp\.DefaultServeMux\b`)

// No service may register a route on http.DefaultServeMux.
//
// 🔴 EVERY SERVER IN THIS REPOSITORY NOW SERVES AN EXPLICIT HANDLER, WHICH MEANS THE
// DEFAULT MUX IS SERVED BY NOTHING. A registration on it still compiles, still runs and
// still returns — onto a mux no listener consults. The route simply is not there, with
// no error and no log line, and on these services it answers 404.
//
// That is why this is a test and not a convention. The nine services that add no routes
// of their own are safe today by accident of not having any; the day a tenth adds one
// with http.Handle, nothing else in the build would notice.
func TestNoServiceRegistersOnTheDefaultMux(t *testing.T) {
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		t.Fatalf("reading %s: %v — the scan cannot see the services, which is a broken "+
			"test rather than a clean tree", servicesDir, err)
	}

	// The detector must be able to detect. Without this, a regexp that had been broken
	// into matching nothing would report every service clean and read as good news.
	for _, probe := range []string{
		`http.Handle("/x", h)`,
		`http.HandleFunc("/x", f)`,
		`server.Routes(http.DefaultServeMux, a, b, c)`,
	} {
		if !defaultMuxRegistration.MatchString(probe) {
			t.Fatalf("the detector does not match %q; it would report every service clean", probe)
		}
	}
	// And must not fire on the replacement shape, or the scan reports the fix as the bug.
	if defaultMuxRegistration.MatchString(`Microservice.Mux().Handle("/x", h)`) {
		t.Fatal("the detector fires on Microservice.Mux().Handle; it cannot tell the fix from the defect")
	}

	scanned := map[string]int{}
	var offenders []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		svc := e.Name()
		err := filepath.WalkDir(filepath.Join(servicesDir, svc), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned[svc]++
			for i, line := range strings.Split(string(src), "\n") {
				// Comments discuss these shapes at length in this repository; a
				// registration is code, so skip anything that is only prose.
				if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "//") {
					continue
				}
				if defaultMuxRegistration.MatchString(line) {
					offenders = append(offenders, filepath.Join(path)+":"+itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking service %q: %v — a read error must not be mistaken for a clean service", svc, err)
		}
	}

	// 🔴 THE VACUITY GUARDS. Everything above passes trivially when the scan saw
	// nothing, which is precisely how an absence gets read as an answer.
	if len(scanned) < minServiceCount {
		t.Fatalf("scanned %d service directories, want at least %d — the scan is not seeing the tree, "+
			"so its clean result means nothing", len(scanned), minServiceCount)
	}
	for _, must := range []string{"user-management", "ai-inference", "mcp"} {
		if scanned[must] == 0 {
			t.Fatalf("scanned no Go files under services/%s; the walk is not reaching service source", must)
		}
	}

	for _, o := range offenders {
		t.Errorf("registers on http.DefaultServeMux, which no server serves: %s", o)
	}
}

// itoa avoids pulling strconv in for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
