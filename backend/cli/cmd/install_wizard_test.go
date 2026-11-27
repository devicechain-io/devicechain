// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// The welcome screen states the request as the command line resolved it, naming the flag
// behind each choice that was given.
func TestTheWizardStatesTheResolvedRequest(t *testing.T) {
	saveChoiceFlags(t)
	c := parseChoices(t, []string{"local", "--kube-context", "prod", "--compact", "--ha", "--yes", "--version", "v1.12.0"})
	prepared, err := c.Prepare(Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := bootstrap.Get("local")
	if err != nil {
		t.Fatal(err)
	}
	f := installWizardFor(provider, c, prepared).Facts
	if f.Provider != "local" || f.KubeContext != "prod" || !f.Compact || f.FootprintFlag != "--compact" ||
		!f.HA || f.TopologyFlag != "--ha" || f.Version != Version || len(f.Providers) == 0 {
		t.Fatalf("facts %+v", f)
	}
	if !strings.HasPrefix(f.Images, "published images v1.12.0 from ") {
		t.Errorf("images %q", f.Images)
	}

	c = parseChoices(t, []string{"local", "--size", "base", "--version", "v1.12.0"})
	if prepared, err = c.Prepare(Version, nil); err != nil {
		t.Fatal(err)
	}
	if f := installWizardFor(provider, c, prepared).Facts; f.Compact || f.FootprintFlag != "--size base" || f.TopologyFlag != "" {
		t.Fatalf("facts %+v", f)
	}
}

// A footprint change on the screen is re-resolved through the command line's own
// resolver: the run it returns installs the footprint chosen, and a flag that pinned the
// other one (--size base) gives way to the explicit choice rather than contradicting it.
func TestTheWizardReplansTheFootprintThroughTheResolver(t *testing.T) {
	saveChoiceFlags(t)
	provider, err := bootstrap.Get("local")
	if err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][]string{nil, {"--size", "base"}, {"--compact"}} {
		args := append([]string{"local", "--version", "v1.12.0"}, flags...)
		c := parseChoices(t, args)
		prepared, err := c.Prepare(Version, nil)
		if err != nil {
			t.Fatal(err)
		}
		w := installWizardFor(provider, c, prepared)
		for _, want := range []bool{true, false} {
			e, pf, err := w.Replan(want)
			if err != nil {
				t.Fatalf("%v -> compact %v: %v", args, want, err)
			}
			ie, ok := e.(*bootstrap.InstallEngine)
			if !ok || pf == nil {
				t.Fatalf("%v: replan returned %T and preflight %v", args, e, pf)
			}
			opts, err := ie.ResolveOptions()
			if err != nil {
				t.Fatal(err)
			}
			if opts.Compact != want {
				t.Errorf("%v -> compact %v: the run installs compact=%v", args, want, opts.Compact)
			}
		}
		// The command line's own choices are untouched by a replan.
		if c.Given["compact"] != (len(flags) == 1) || c.Given["size"] != (len(flags) == 2) {
			t.Errorf("%v: a replan changed the parsed choices: %+v", args, c.Given)
		}
	}
}
