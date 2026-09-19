// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dcctl/operator"
)

// stubOperatorCheck replaces the operator guard for tests about the refusals AROUND
// it. It records what it was asked, so a test that wants to can assert the check ran
// at all rather than only that it was satisfied.
//
// 🔴 EVERY TEST THAT HYDRATES AN UPGRADE NEEDS THIS, and that is a fact about the
// change rather than test scaffolding: hydrateUpgradeState now contacts the cluster
// before it reads anything else, so without a stub every one of those tests would be
// asserting a connection failure instead of the refusal it was written for.
func stubOperatorCheck(t *testing.T, err error) *int {
	t.Helper()
	prev := requireOperatorFor
	t.Cleanup(func() { requireOperatorFor = prev })
	calls := 0
	requireOperatorFor = func(context.Context, string, string, string) error {
		calls++
		return err
	}
	return &calls
}

// 🔴 THREE ANSWERS, THREE REMEDIES, AND GETTING THE PAIRING WRONG IS THE FAILURE
// THIS TEST EXISTS FOR. Each row states what an operator is holding and what they
// must be told; a wrong pairing is not a cosmetic slip, it sends them to the wrong
// action — to reinstall over a deliberate `make deploy`, or onwards onto a schema
// their instance was not built for.
func TestTheClusterIsToldWhichOfTheThreeThingsIsWrong(t *testing.T) {
	const want = "the-identity-this-build-carries"
	const install = "dcctl install local"

	t.Run("no operator refuses and names the install command", func(t *testing.T) {
		note, err := judgeOperator(operator.Installed{Missing: []string{"instances.core.devicechain.io"}},
			want, "a bootstrap", install)
		if err == nil {
			t.Fatal("a cluster with no operator was accepted")
		}
		if !strings.Contains(err.Error(), install) {
			t.Errorf("the refusal does not say what to run: %v", err)
		}
		if !strings.Contains(err.Error(), "instances.core.devicechain.io") {
			t.Errorf("the refusal does not name what is missing: %v", err)
		}
		if note != "" {
			t.Errorf("a refusal also printed a note: %q", note)
		}
	})

	t.Run("an unstamped operator is allowed through with a note", func(t *testing.T) {
		note, err := judgeOperator(operator.Installed{Present: true}, want, "a bootstrap", install)
		if err != nil {
			t.Fatalf("a hand-installed operator was REFUSED: %v\n"+
				"`make deploy` never stamps, so this refuses the documented maintainer path "+
				"and overrules a choice dcctl has no better information about.", err)
		}
		if note == "" {
			t.Fatal("a cluster dcctl cannot vouch for said nothing; the one check protecting " +
				"an instance from the wrong schema did not run and nobody was told")
		}
		if !strings.Contains(note, "make deploy") {
			t.Errorf("the note does not say what this state usually is: %q", note)
		}
	})

	t.Run("a different operator refuses and shows both sides", func(t *testing.T) {
		_, err := judgeOperator(operator.Installed{Present: true, Identity: "something-else"},
			want, "an upgrade", install)
		if err == nil {
			t.Fatal("a cluster carrying a different operator was accepted")
		}
		for _, s := range []string{short("something-else"), short(want), install, "an upgrade"} {
			if !strings.Contains(err.Error(), s) {
				t.Errorf("the refusal does not contain %q: %v", s, err)
			}
		}
	})

	t.Run("the matching case is silent", func(t *testing.T) {
		note, err := judgeOperator(operator.Installed{Present: true, Identity: want},
			want, "a bootstrap", install)
		if err != nil || note != "" {
			t.Fatalf("a cluster carrying exactly the right operator was not waved through: %v / %q", err, note)
		}
	})
}

// 🔴 THE OPERATOR CHECK RUNS BEFORE THE DECLARATION IS READ, AND THE ORDER IS THE
// WHOLE VALUE OF THE REFUSAL RATHER THAN A PREFERENCE.
//
// With no Instance CRD installed there can BE no declaration, so the read below it
// comes back empty and hydrateUpgradeState reports "instance %q is not declared —
// check the name". The name is correct. The cluster has no operator. An operator
// mid-upgrade would be sent to re-check something that is fine, and the one piece of
// advice that would have helped — run `dcctl install` — is never printed.
//
// Asserting the ORDER needs the declaration read to be observable, so it is stubbed
// with a counter: if it ran, the guard did not come first.
func TestTheOperatorIsCheckedBeforeTheInstanceIsLookedFor(t *testing.T) {
	provider, err := Get("local")
	if err != nil {
		t.Fatal(err)
	}
	binding := ClusterBinding{KubeContext: "kind-c", Cluster: "c"}
	opts := UpgradeOptions{Options: Options{Instance: "prod"}}

	refused := errors.New("this cluster has no DeviceChain operator")
	checks := stubOperatorCheck(t, refused)

	reads := 0
	prev := readInstanceDeclaration
	t.Cleanup(func() { readInstanceDeclaration = prev })
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		reads++
		return nil, nil
	}

	_, err = hydrateUpgradeState(t.Context(), nil, provider, binding, opts)
	if !errors.Is(err, refused) {
		t.Fatalf("an upgrade against a cluster with no operator returned %v, not the "+
			"operator refusal", err)
	}
	if *checks != 1 {
		t.Errorf("the operator was checked %d times", *checks)
	}
	if reads != 0 {
		t.Fatal("the declaration was read before the operator was checked, so a cluster with " +
			"no operator is reported as an instance that is not declared — advice about a name " +
			"that is correct, during an upgrade")
	}
}

// 🔴 AND THE COUNTERWEIGHT: the guard must not swallow the refusals that come after
// it. A check wired to refuse everything would satisfy the test above and break every
// other upgrade refusal in the suite.
func TestASatisfiedOperatorCheckLetsTheLaterRefusalsThrough(t *testing.T) {
	provider, err := Get("local")
	if err != nil {
		t.Fatal(err)
	}
	binding := ClusterBinding{KubeContext: "kind-c", Cluster: "c"}
	opts := UpgradeOptions{Options: Options{Instance: "prod"}}

	stubOperatorCheck(t, nil)
	prev := readInstanceDeclaration
	t.Cleanup(func() { readInstanceDeclaration = prev })
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		return nil, nil
	}
	stubClusterInstances(t, clusterInstances{}, nil)

	_, err = hydrateUpgradeState(t.Context(), nil, provider, binding, opts)
	if err == nil {
		t.Fatal("an upgrade of an instance that is not declared was accepted")
	}
	if strings.Contains(err.Error(), "no DeviceChain operator") {
		t.Fatalf("the operator refusal fired on a cluster whose operator was fine: %v", err)
	}
}
