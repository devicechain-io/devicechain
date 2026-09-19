// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// 🔴 THE REGRESSION THIS PINS BROKE THE DOCUMENTED DEVELOPER PATH AND BOTH
// VALIDATION RIGS AT ONCE, and nothing noticed.
//
// `dcctl install` gained an image source, which means it now resolves one — and a
// dcctl built with `make build` carries no pinned image version, deliberately. So
// the first command in the documented local bring-up, `dcctl install local --dev`,
// went from working to being refused before it touched anything. The preset said
// only "--yes"; it had to say "--build" as well, exactly as bootstrap's does.
//
// The assertion is the resolution, not the flag: what matters is that the source a
// --dev install settles on is one that can actually be built and pulled.
func TestTheDevPresetCanStillPrepareADevelopersCluster(t *testing.T) {
	res, err := resolveInstallDevMode(neverChanged, false, "")
	if err != nil {
		t.Fatalf("the --dev preset refused a plain `dcctl install local --dev`: %v", err)
	}
	if !res.Build {
		t.Fatal("--dev does not imply --build, so `dcctl install local --dev` resolves a " +
			"PUBLISHED image source — and a dcctl built from source has no published tag " +
			"to name, so the documented first command of the local bring-up is refused")
	}
	if !res.Yes {
		t.Error("--dev no longer implies --yes, so the developer preset prompts")
	}

	// And the source that preset settles on must be one that can be built and pulled.
	img, err := bootstrap.ResolveImageSource("", "", res.Build)
	if err != nil {
		t.Fatalf("`dcctl install local --dev` cannot settle an image source: %v\n"+
			"The documented local bring-up starts with this command, so it is refused "+
			"before a developer reaches anything else.", err)
	}
	if img.Registry == "" || img.Version == "" {
		t.Fatalf("the --dev image source is incomplete (%+v); the operator Deployment would "+
			"name a reference that pulls nothing", img)
	}

	// The counterweight: WITHOUT --build the same source-built dcctl must still be
	// refused. If this ever passes, the refusal that protects the published path
	// has gone, and a dev build would deploy an operator tag that was never pushed.
	if _, err := bootstrap.ResolveImageSource("", "", false); err == nil {
		t.Fatal("a dcctl with no pinned image version resolved a published operator image; " +
			"the install would report success over an ImagePullBackOff")
	}
}

// The contradictions are refused rather than silently resolved, because either
// answer would be wrong in a way the operator could not see: honouring --dev would
// ignore a --version they typed, and honouring --version would build nothing while
// --dev said it had.
func TestTheDevPresetRefusesFlagsThatContradictIt(t *testing.T) {
	for _, tc := range []struct {
		what    string
		changed func(string) bool
		build   bool
		version string
		says    string
	}{
		{"--build=false", changedOnly("build"), false, "", "--build=false"},
		{"--version", changedOnly("version"), false, "v0.17.0", "v0.17.0"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			_, err := resolveInstallDevMode(tc.changed, tc.build, tc.version)
			if err == nil {
				t.Fatalf("--dev with %s was accepted; one of the two was going to be "+
					"silently ignored", tc.what)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not name what to remove: %v", err)
			}
		})
	}

	// And an explicit --build=true alongside --dev is agreement, not contradiction.
	if _, err := resolveInstallDevMode(changedOnly("build"), true, ""); err != nil {
		t.Errorf("--dev --build was refused although the two agree: %v", err)
	}
}

func neverChanged(string) bool { return false }

func changedOnly(name string) func(string) bool {
	return func(n string) bool { return n == name }
}
