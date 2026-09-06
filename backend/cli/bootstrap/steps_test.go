// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerBuildNetworkDefaultsToHost(t *testing.T) {
	t.Setenv(dockerBuildNetEnv, "")
	got, err := dockerBuildNetwork()
	if err != nil {
		t.Fatalf("unset %s: %v", dockerBuildNetEnv, err)
	}
	if got != "host" {
		// Not a style preference: bridge networking fails `npm ci` reproducibly on
		// at least one WSL2 host, which is why build-images.sh has defaulted to host
		// since it was written. See dockerBuildNetwork.
		t.Fatalf("default network = %q, want host", got)
	}
}

func TestDockerBuildNetworkAcceptsWhatBuildkitAccepts(t *testing.T) {
	for _, mode := range []string{"default", "host", "none"} {
		t.Setenv(dockerBuildNetEnv, mode)
		got, err := dockerBuildNetwork()
		if err != nil {
			t.Fatalf("%s=%s: %v", dockerBuildNetEnv, mode, err)
		}
		if got != mode {
			t.Fatalf("%s=%s resolved to %q", dockerBuildNetEnv, mode, got)
		}
	}
}

func TestDockerBuildNetworkRefusesBridgeByName(t *testing.T) {
	// `bridge` is the tempting wrong answer -- it is what docker's ordinary
	// networking is called everywhere except buildkit's --network, which rejects
	// it. An error that only says "invalid" leaves the reader to guess `default`.
	t.Setenv(dockerBuildNetEnv, "bridge")
	_, err := dockerBuildNetwork()
	if err == nil {
		t.Fatal("bridge was accepted; buildkit rejects it outright")
	}
	if !strings.Contains(err.Error(), "default") {
		t.Fatalf("error does not name the working spelling: %v", err)
	}
}

func TestDockerBuildNetworkRefusesAnUnknownMode(t *testing.T) {
	t.Setenv(dockerBuildNetEnv, "hsot")
	if _, err := dockerBuildNetwork(); err == nil {
		t.Fatal("a typo was accepted")
	}
}

// The frontend is the LAST image built, so a value docker will refuse must be
// caught before the ko builds rather than ten minutes into them.
func TestBuildImagesRejectsABadNetworkBeforeBuildingAnything(t *testing.T) {
	t.Setenv(dockerBuildNetEnv, "bridge")
	// A root that does not exist: reaching any build step would fail differently,
	// so the expected error text is itself evidence nothing was built.
	err := buildImages(context.Background(), filepath.Join(t.TempDir(), "absent"), &State{})
	if err == nil {
		t.Fatal("buildImages accepted an unusable network mode")
	}
	if !strings.Contains(err.Error(), dockerBuildNetEnv) {
		t.Fatalf("failed for some other reason, so the check did not run first: %v", err)
	}
}

// 🔑 THE DISCRIMINATOR. Every test above passes against a buildFrontend that
// ignores dockerBuildNetwork entirely -- that is the shape where a helper is
// certified and the call site is not. This one reads the argv docker was actually
// invoked with, and the `default` case is what separates "the flag is passed" from
// "the flag is hardcoded to host".
func TestBuildFrontendPassesTheResolvedNetworkToDocker(t *testing.T) {
	for _, mode := range []string{"host", "default", "none"} {
		t.Run(mode, func(t *testing.T) {
			bin := t.TempDir()
			argvFile := filepath.Join(bin, "argv")
			shim := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argvFile + "\nexit 0\n"
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(shim), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(dockerBuildNetEnv, mode)

			st := &State{ImageRegistry: "localhost:5000", ImageVersion: "test-tag"}
			if err := buildFrontend(context.Background(), t.TempDir(), st); err != nil {
				t.Fatalf("buildFrontend: %v", err)
			}

			argv, err := os.ReadFile(argvFile)
			if err != nil {
				t.Fatalf("docker was never invoked: %v", err)
			}
			want := "--network=" + mode
			if !strings.Contains(string(argv), want) {
				t.Fatalf("docker argv missing %q:\n%s", want, argv)
			}
		})
	}
}

// The counterweight to the test above: a build the shim FAILS must surface as an
// error rather than being reported as a completed image. Without this, the argv
// assertion would hold just as well against a buildFrontend that ignored exit
// codes -- and a bootstrap would then push a tag it never built.
func TestBuildFrontendReportsADockerFailure(t *testing.T) {
	bin := t.TempDir()
	shim := "#!/bin/sh\necho 'simulated docker failure' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(dockerBuildNetEnv, "host")

	st := &State{ImageRegistry: "localhost:5000", ImageVersion: "test-tag"}
	if err := buildFrontend(context.Background(), t.TempDir(), st); err == nil {
		t.Fatal("a failing docker build was reported as success")
	}
}
