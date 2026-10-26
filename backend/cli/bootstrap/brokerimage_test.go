// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestBrokerImageReachesTheInstanceRoot: every infrastructure apply names the broker build
// at the registry and release tag the services run, and the variables land in the INSTANCE
// root, which installs the broker. A variable the root does not declare would be refused by
// splitVars, and one routed to the cluster root would never reach the nats module.
func TestBrokerImageReachesTheInstanceRoot(t *testing.T) {
	for _, st := range []*State{
		compactState(false),
		compactState(true),
		{Instance: "local", ImageRegistry: "localhost:5001", ImageVersion: "dev", Values: map[string]string{}},
	} {
		wantRepo := "nats_server_image_repository=" + st.ImageRegistry + "/dc-nats-server"
		wantTag := "nats_server_image_tag=" + st.ImageVersion
		all := infraVars(st)
		if !slices.Contains(all, wantRepo) || !slices.Contains(all, wantTag) {
			t.Fatalf("infraVars for %q lacks %q / %q:\n%s", st.Instance, wantRepo, wantTag, strings.Join(all, "\n"))
		}
		cluster, instance, err := splitVars(all)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(instance, wantRepo) || !slices.Contains(instance, wantTag) {
			t.Fatalf("the broker image variables do not reach the instance root (instance vars: %v)", instance)
		}
		for _, v := range cluster {
			if strings.HasPrefix(v, "nats_server_image_") {
				t.Fatalf("a broker image variable was routed to the cluster root: %s", v)
			}
		}
	}
}

// TestUnresolvedImageSourceEmitsNoBrokerImage: a half-made reference is never passed; the
// module's own refusal of a missing tag is what such a run reaches.
func TestUnresolvedImageSourceEmitsNoBrokerImage(t *testing.T) {
	for _, st := range []*State{
		{ImageRegistry: "", ImageVersion: "v0.26.0"},
		{ImageRegistry: DefaultImageRegistry, ImageVersion: ""},
	} {
		if got := brokerImageVars(st); got != nil {
			t.Fatalf("brokerImageVars(%q, %q) = %v, want nothing", st.ImageRegistry, st.ImageVersion, got)
		}
	}
}

// TestBrokerModuleIsWhereTheBuildLooks: --build ko-builds brokerModuleDir, and the
// module's path must end in /nats-server, because ko names the binary after it and the nats
// chart's preStop hook runs `nats-server -sl=ldm=...`. A binary by any other name would make
// every broker shutdown skip lame duck mode, with nothing reporting it.
func TestBrokerModuleIsWhereTheBuildLooks(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, brokerModuleDir)
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Fatalf("%s has no main.go: %v", brokerModuleDir, err)
	}
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(mod), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
			if !strings.HasSuffix(f[1], "/nats-server") {
				t.Fatalf("module path %q does not end in /nats-server: ko would name the binary %q, which the chart's preStop does not run",
					f[1], filepath.Base(f[1]))
			}
			return
		}
	}
	t.Fatal("no module line in the broker's go.mod")
}

// TestTheBrokerIsPinnedByDigestOnlyForItsOwnRelease: the stamped digest names exactly one image,
// the one this dcctl's release pushed, so it is passed only for the published registry at the
// release's own tag, and never for a --build, another registry or another version (which would
// pin a broker that source never built). Each refusal is paired with the case that passes.
func TestTheBrokerIsPinnedByDigestOnlyForItsOwnRelease(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	savedDigest, savedVersion := DefaultBrokerImageDigest, DefaultImageVersion
	t.Cleanup(func() { DefaultBrokerImageDigest, DefaultImageVersion = savedDigest, savedVersion })
	DefaultBrokerImageDigest, DefaultImageVersion = digest, "v0.26.0"

	pinned := func(st *State) bool {
		return slices.Contains(brokerImageVars(st), "nats_server_image_digest="+digest)
	}
	release := &State{ImageRegistry: DefaultImageRegistry, ImageVersion: "v0.26.0"}
	if !pinned(release) {
		t.Fatalf("the release's own image is not pinned by digest: %v", brokerImageVars(release))
	}
	for name, st := range map[string]*State{
		"another version":  {ImageRegistry: DefaultImageRegistry, ImageVersion: "v0.25.2"},
		"another registry": {ImageRegistry: "registry.example.com/dc", ImageVersion: "v0.26.0"},
		"a --build":        {ImageRegistry: DefaultImageRegistry, ImageVersion: "v0.26.0", BuildImages: true},
	} {
		if pinned(st) {
			t.Errorf("%s: pinned by the release's digest, which names a different image", name)
		}
	}
	DefaultBrokerImageDigest = ""
	if got := brokerImageVars(release); slices.ContainsFunc(got, func(v string) bool { return strings.HasPrefix(v, "nats_server_image_digest=") }) {
		t.Fatalf("an unstamped dcctl passed a digest: %v", got)
	}
	// And the variable reaches the instance root, which declares it.
	DefaultBrokerImageDigest = digest
	_, instance, err := splitVars(brokerImageVars(release))
	if err != nil || !slices.Contains(instance, "nats_server_image_digest="+digest) {
		t.Fatalf("the digest does not reach the instance root: %v %v", instance, err)
	}
}

// TestTheReleaseStampsTheBrokerDigest: goreleaser stamps DefaultBrokerImageDigest from the
// release's own push, and the release refuses a dcctl that does not report it. A Go test cannot
// run goreleaser, so this pins the three places the stamp travels through.
func TestTheReleaseStampsTheBrokerDigest(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if !strings.Contains(read("backend/cli/.goreleaser.yaml"),
		`-X github.com/devicechain-io/dcctl/bootstrap.DefaultBrokerImageDigest={{ index .Env "DC_BROKER_IMAGE_DIGEST" }}`) {
		t.Error(".goreleaser.yaml no longer stamps bootstrap.DefaultBrokerImageDigest from DC_BROKER_IMAGE_DIGEST")
	}
	wf := read(".github/workflows/release.yml")
	for _, want := range []string{
		"name: ref-dc-nats-server",
		`echo "DC_BROKER_IMAGE_DIGEST=$digest" >> "$GITHUB_ENV"`,
		`broker="$("$binary" version | awk '$1 == "broker:" { print $2 }')"`,
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("release.yml lacks %q: the broker digest no longer reaches, or is no longer checked in, the released dcctl", want)
		}
	}
}
