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

// 🔴 THE NAME IS THE CONTRACT, so it is asserted as a LITERAL and not through the
// constant. Every other test here reaches the environment via dockerBuildNetEnv --
// a fixture built from the thing under test, which cannot notice that thing moving.
// Rename the constant and they all still pass, while dcctl and the two shell scripts
// silently stop sharing a variable, which IS the defect this file exists to prevent.
func TestDockerBuildNetEnvIsTheNameTheShellScriptsRead(t *testing.T) {
	if dockerBuildNetEnv != "DOCKER_BUILD_NET" {
		t.Fatalf("dockerBuildNetEnv = %q; deploy/local/build-images.sh and deploy/local/bounce.sh "+
			"read DOCKER_BUILD_NET, and one spelling covering all three is the point of the change",
			dockerBuildNetEnv)
	}
}

// The other half of the same contract, read from the script itself rather than
// restated here -- a constant asserted against a constant would agree forever while
// the script drifted. This reads OUTSIDE the module, which go's test cache does not
// track, so it is only honest under -count=1 (CI passes it; so does the documented
// full sweep).
func TestTheShellScriptsStillReadTheSameVariableAndDefault(t *testing.T) {
	for _, script := range []string{"build-images.sh", "bounce.sh"} {
		path := filepath.Join("..", "..", "..", "deploy", "local", script)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read %s: %v", path, err)
		}
		text := string(body)
		if !strings.Contains(text, dockerBuildNetEnv) {
			t.Fatalf("%s no longer mentions %s: the paths have split again", script, dockerBuildNetEnv)
		}
		// Both spell the default the shell way; dockerBuildNetwork must agree with it.
		if !strings.Contains(text, "${"+dockerBuildNetEnv+":-host}") &&
			!strings.Contains(text, dockerBuildNetEnv+"=${"+dockerBuildNetEnv+":-host}") {
			t.Fatalf("%s no longer defaults %s to host; dockerBuildNetwork still does",
				script, dockerBuildNetEnv)
		}
	}
}

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

// buildImages is buildFrontend's only caller today, which is what makes the claim
// above true for every path. This is the second line of defence for the day that
// stops being so -- and it is tested, because a check nothing exercises drifts.
func TestBuildFrontendRefusesABadNetworkOnItsOwn(t *testing.T) {
	t.Setenv(dockerBuildNetEnv, "bridge")
	// No docker shim on PATH: if the check were skipped, this would fail with
	// something about docker instead, which the assertion below distinguishes.
	err := buildFrontend(context.Background(), t.TempDir(), &State{})
	if err == nil {
		t.Fatal("buildFrontend built with an unusable network mode")
	}
	if !strings.Contains(err.Error(), dockerBuildNetEnv) {
		t.Fatalf("failed for some other reason, so its own check did not run: %v", err)
	}
}

// dockerShim writes a fake `docker` onto PATH that records each invocation's argv
// into a file named for the SUBCOMMAND, plus the order the subcommands ran in.
//
// 🔴 PER-SUBCOMMAND IS LOAD-BEARING. An earlier version appended every invocation to
// ONE file and asserted the flag appeared somewhere in it. That assertion holds when
// --network is passed to `docker push` instead of `docker build` (real docker would
// reject it and the image would still be built on the wrong network), when push runs
// before build, and when push never runs at all -- three defects the test read as if
// it covered. Which command carried the flag is the whole question.
func dockerShim(t *testing.T, failOn string) string {
	t.Helper()
	dir := t.TempDir()
	fail := ""
	if failOn != "" {
		fail = "if [ \"$1\" = " + failOn + " ]; then echo 'simulated docker failure' >&2; exit 1; fi\n"
	}
	shim := "#!/bin/sh\n" +
		"printf '%s\\n' \"$1\" >> '" + filepath.Join(dir, "order") + "'\n" +
		"printf '%s\\n' \"$@\" >> '" + filepath.Join(dir, "argv.") + "'\"$1\"\n" +
		fail +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func readShimFile(t *testing.T, dir, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("docker was never invoked for %q: %v", name, err)
	}
	return string(body)
}

// hasNetworkFlag accepts either docker spelling -- `--network=host` or `--network
// host` -- because the two are equivalent to docker and pinning one would fail an
// honest refactor while catching no real defect. What is pinned is that the flag is
// on THIS argv carrying THIS value.
func hasNetworkFlag(argv, mode string) bool {
	args := strings.Split(strings.TrimSpace(argv), "\n")
	for i, a := range args {
		if a == "--network="+mode {
			return true
		}
		if a == "--network" && i+1 < len(args) && args[i+1] == mode {
			return true
		}
	}
	return false
}

// 🔑 THE DISCRIMINATOR. Every test above passes against a buildFrontend that ignores
// dockerBuildNetwork entirely -- the shape where a helper is certified and the call
// site is not. This reads the argv `docker build` was actually invoked with, and the
// `default` and `none` cases are what separate "the flag is passed" from "the flag is
// hardcoded to host".
func TestBuildFrontendPassesTheResolvedNetworkToDockerBuild(t *testing.T) {
	for _, mode := range []string{"host", "default", "none"} {
		t.Run(mode, func(t *testing.T) {
			dir := dockerShim(t, "")
			t.Setenv(dockerBuildNetEnv, mode)

			st := &State{ImageRegistry: "localhost:5000", ImageVersion: "test-tag"}
			if err := buildFrontend(context.Background(), t.TempDir(), st); err != nil {
				t.Fatalf("buildFrontend: %v", err)
			}

			build := readShimFile(t, dir, "argv.build")
			if !hasNetworkFlag(build, mode) {
				t.Fatalf("`docker build` argv carries no --network %s:\n%s", mode, build)
			}
		})
	}
}

// The image must actually be PUSHED, and after it is built. The registry is what the
// chart pulls from, so a build that never pushes -- or pushes before it builds --
// leaves the chart resolving a tag that is absent or stale, with every step reporting
// success. Asserting only on the build argv cannot see either.
func TestBuildFrontendPushesTheImageItJustBuilt(t *testing.T) {
	dir := dockerShim(t, "")
	t.Setenv(dockerBuildNetEnv, "host")

	st := &State{ImageRegistry: "localhost:5000", ImageVersion: "test-tag"}
	if err := buildFrontend(context.Background(), t.TempDir(), st); err != nil {
		t.Fatalf("buildFrontend: %v", err)
	}

	const image = "localhost:5000/frontend:test-tag"
	if build := readShimFile(t, dir, "argv.build"); !strings.Contains(build, image) {
		t.Fatalf("`docker build` did not tag %s:\n%s", image, build)
	}
	if push := readShimFile(t, dir, "argv.push"); !strings.Contains(push, image) {
		t.Fatalf("`docker push` did not push %s:\n%s", image, push)
	}
	order := strings.Fields(readShimFile(t, dir, "order"))
	if len(order) != 2 || order[0] != "build" || order[1] != "push" {
		t.Fatalf("docker subcommands ran in the order %v, want [build push]", order)
	}
}

// The counterweight. Without it the argv assertions would hold just as well against a
// buildFrontend that ignored exit codes, and a bootstrap would push a tag it never
// built.
//
// 🔴 THE SHIM FAILS ONLY `build`. An earlier version failed EVERY invocation, which
// made the test pass even when the build's error was discarded -- the push then
// failed and supplied the error the test credited to the build. A control that fails
// everything cannot say which thing was checked.
func TestBuildFrontendReportsAFailedBuildEvenThoughThePushWouldSucceed(t *testing.T) {
	dockerShim(t, "build")
	t.Setenv(dockerBuildNetEnv, "host")

	st := &State{ImageRegistry: "localhost:5000", ImageVersion: "test-tag"}
	if err := buildFrontend(context.Background(), t.TempDir(), st); err == nil {
		t.Fatal("a failing docker build was reported as success")
	}
}

// And the same for the push: a build that succeeds and a push that does not must not
// report an image into a registry that never received it.
func TestBuildFrontendReportsAFailedPush(t *testing.T) {
	dockerShim(t, "push")
	t.Setenv(dockerBuildNetEnv, "host")

	st := &State{ImageRegistry: "localhost:5000", ImageVersion: "test-tag"}
	if err := buildFrontend(context.Background(), t.TempDir(), st); err == nil {
		t.Fatal("a failing docker push was reported as success")
	}
}
