// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryBuildPathBuildsReproducibly: the broker image must be the same for the same source, so
// that a release which did not change the broker does not roll it (hack/broker-image-reproducible.sh
// proves the two settings achieve that). Each place that builds the image has to set both, and
// they are four different files in three languages, so a fifth path, or an edit to one of these,
// would quietly bring back a per-commit image. This pins each.
//
// It reads files outside this module, which Go's test cache does not track: run it with -count=1,
// as CI does.
func TestEveryBuildPathBuildsReproducibly(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}
	conditional := []string{
		`GOWORK: ${{ matrix.module == 'backend/broker/dc-nats-server' && 'off' || '' }}`,
		`GOFLAGS: ${{ matrix.module == 'backend/broker/dc-nats-server' && '-buildvcs=false' || '' }}`,
	}
	for _, c := range []struct {
		file  string
		want  []string
		count int
	}{
		// The release's ko build: the image that is published.
		{".github/workflows/release.yml", conditional, 1},
		// CI's generic image build, so it builds what the release builds.
		{".github/workflows/ci.yml", conditional, 1},
		// CI's broker smoke step, which runs the image it builds.
		{".github/workflows/ci.yml", []string{"GOWORK: \"off\"\n          GOFLAGS: -buildvcs=false\n"}, 1},
		// The local registry build the upgrade drill and every --build bring-up use.
		{"deploy/local/build-images.sh", []string{`build_one "$ROOT/backend/broker/dc-nats-server" "$REGISTRY/dc-nats-server" GOWORK=off GOFLAGS=-buildvcs=false`}, 1},
		// dcctl --build.
		{"backend/cli/bootstrap/brokerimage.go", []string{`var brokerBuildEnv = []string{"GOWORK=off", "GOFLAGS=-buildvcs=false"}`}, 1},
		{"backend/cli/bootstrap/steps.go", []string{`brokerImageName), brokerBuildEnv...)`}, 1},
	} {
		body := read(c.file)
		for _, w := range c.want {
			if n := strings.Count(body, w); n < c.count {
				t.Errorf("%s does not build the broker reproducibly: it lacks %q", c.file, w)
			}
		}
	}
}

// TestToolchainMatchesTheWorkspace: built with GOWORK=off, the broker takes its compiler from its
// own go.mod's toolchain line, not go.work's. The workspace's is the one that moves when a Go patch
// release fixes the standard library (crypto/tls, crypto/x509, net/http), so the broker's must move
// with it, or the one binary every device talks to is the one left on the old compiler. Measured
// before this line existed: a GOWORK=off build on a machine whose default Go was older reported
// dc_broker_info{build="go1.26.0"}.
func TestToolchainMatchesTheWorkspace(t *testing.T) {
	toolchain := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "..", "..", rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) == 2 && f[0] == "toolchain" {
				return f[1]
			}
		}
		return ""
	}
	work, mod := toolchain("go.work"), toolchain("backend/broker/dc-nats-server/go.mod")
	if work == "" {
		t.Fatal("go.work declares no toolchain; this test has nothing to compare against")
	}
	if mod != work {
		t.Fatalf("the broker's go.mod toolchain is %q, go.work's is %q: move them together", mod, work)
	}
}
