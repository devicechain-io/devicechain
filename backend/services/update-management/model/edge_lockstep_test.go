// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-update-management/config"
)

// The edge controller's read timeout is derived from this service's upload window, and the
// two live in different languages and different modules. Saying "keep these in lockstep" in
// both places is the whole mechanism until something enforces it, and a drift is not
// loud: the controller's timeout bounds the WHOLE request body, so one that is too short
// cuts a legitimate OTA upload at exactly that many seconds, long after anything was
// deployed, and one that is far too long is an unbounded slowloris window.
//
// So this reads the arithmetic back out of the Traefik module and holds it against the
// service's own UploadWindow and claim grace.
//
// 🔴 THE FILES READ LIVE OUTSIDE THIS MODULE and Go's test cache does not track them, so a
// plain `go test` serves a stale PASS over an edit there. CI runs every module with -count=1
// and so does the pre-commit sweep in CLAUDE.md.
const (
	traefikModuleMain  = "../../../../deploy/opentofu/modules/traefik/main.tf"
	traefikModuleTests = "../../../../deploy/opentofu/modules/traefik/tests/chart_values.tftest.hcl"
)

func readOutsideModule(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func hclInt(t *testing.T, src, name string) int64 {
	t.Helper()
	m := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s*=\s*(\d+)\s*$`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("local %q not found in the Traefik module: the parse is broken, or the local was renamed", name)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The three constants the module restates must be the service's.
func TestTraefikModuleRestatesTheUploadWindowArithmetic(t *testing.T) {
	src := readOutsideModule(t, traefikModuleMain)

	fixed := hclInt(t, src, "upload_fixed_allowance_seconds")
	rate := hclInt(t, src, "upload_min_throughput_bytes")
	grace := hclInt(t, src, "upload_claim_grace_seconds")

	// UploadWindow(0) is the fixed allowance alone.
	if got := UploadWindow(0); got != time.Duration(fixed)*time.Second {
		t.Errorf("module upload_fixed_allowance_seconds = %d, but UploadWindow(0) = %v", fixed, got)
	}
	if got := claimGrace; got != time.Duration(grace)*time.Second {
		t.Errorf("module upload_claim_grace_seconds = %d, but claimGrace = %v", grace, got)
	}
	// The rate: UploadWindow grows by exactly one second per `rate` bytes, floored. Probe the
	// edges of the division, where a different rate or a rounding change shows.
	for _, size := range []int64{rate - 1, rate, rate + 1, 2*rate - 1, 2 * rate, 1 << 30, 1<<30 + rate/2} {
		want := time.Duration(fixed+size/rate) * time.Second
		if got := UploadWindow(size); got != want {
			t.Errorf("UploadWindow(%d) = %v, but the module's arithmetic gives %v", size, got, want)
		}
	}
}

// The module's default artifact ceiling must be the service's default, or a stock install
// derives its read timeout from a size nothing accepts.
func TestTraefikModuleDefaultsToTheServicesArtifactCeiling(t *testing.T) {
	src := readOutsideModule(t, traefikModuleMain)
	m := regexp.MustCompile(`(?s)variable "max_artifact_bytes" \{.*?default\s*=\s*(\d+)`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("variable max_artifact_bytes (or its default) not found in the Traefik module")
	}
	def, _ := strconv.ParseInt(m[1], 10, 64)
	if def != config.DefaultMaxArtifactBytes {
		t.Errorf("module max_artifact_bytes defaults to %d, but update-management's DefaultMaxArtifactBytes is %d",
			def, config.DefaultMaxArtifactBytes)
	}
}

// Each expectation the module's own tests make is recomputed here from the service.
//
// 🔴 A parse that finds nothing would pass trivially, so it must find the default case
// and at least the cases pinned today; a rewrite of the test file that the regex no longer
// matches fails the floor rather than reading as "nothing to check".
func TestTraefikModuleTestsExpectTheServicesReadTimeout(t *testing.T) {
	src := readOutsideModule(t, traefikModuleTests)
	defSize := config.DefaultMaxArtifactBytes

	runs := regexp.MustCompile(`(?m)^run "`).Split(src, -1)[1:]
	sizeRe := regexp.MustCompile(`max_artifact_bytes\s*=\s*(\d+)`)
	wantRe := regexp.MustCompile(`output\.read_timeout\s*==\s*"(\d+)s"`)

	checked := 0
	for _, run := range runs {
		w := wantRe.FindStringSubmatch(run)
		if w == nil {
			continue
		}
		size := defSize
		if s := sizeRe.FindStringSubmatch(run); s != nil {
			size, _ = strconv.ParseInt(s[1], 10, 64)
		}
		secs, _ := strconv.ParseInt(w[1], 10, 64)
		name := run[:strings.Index(run, "\"")]
		want := UploadWindow(size) + claimGrace
		if time.Duration(secs)*time.Second != want {
			t.Errorf("run %q expects a read timeout of %ds for %d bytes, but UploadWindow + claim grace is %v",
				name, secs, size, want)
		}
		checked++
	}
	if checked < 4 {
		t.Fatalf("only %d read-timeout expectations were found in %s; the parse is broken or the cases were removed",
			checked, traefikModuleTests)
	}
}
