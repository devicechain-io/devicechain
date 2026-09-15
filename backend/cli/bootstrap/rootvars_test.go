// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
)

// 🔴🔴 THE ONE THAT MATTERS: every value dcctl computes must reach a root that
// declares it.
//
// splitVars routes each `-var` by which root declares the name, so a variable dcctl
// passes that NO root declares would otherwise be dropped from both applies — and the
// apply then runs with that root's DEFAULT instead of the value dcctl computed. The
// instance comes up built to a setting nobody chose: a --compact install taking
// full-size volumes, a restore flag that restores nothing, a node port that never
// opens. None of those fail; they just come out wrong.
//
// 🔑 IT IS DRIVEN THROUGH infraVars RATHER THAN A LIST OF NAMES, so it covers the
// variables dcctl emits CONDITIONALLY — the ones a fixed list would omit precisely
// because they are only reachable behind a flag. Rename a variable in either root's
// variables.tf and this fails naming it.
func TestEveryVariableDcctlPassesIsDeclaredBySomeRoot(t *testing.T) {
	for _, tc := range []struct {
		what string
		st   *State
	}{
		{"defaults", &State{KubeContext: "kind-dc", Instance: "a"}},
		{"ha", &State{KubeContext: "kind-dc", Instance: "a", HA: true}},
		{"compact", &State{KubeContext: "kind-dc", Instance: "a", Compact: true}},
		{"compact and no TLS", &State{KubeContext: "kind-dc", Instance: "a", Compact: true, NoTLS: true}},
		{"no monitoring", &State{KubeContext: "kind-dc", Instance: "a", NoMonitoring: true}},
		{"no CNPG", &State{KubeContext: "kind-dc", Instance: "a", NoCNPG: true}},
		{"legacy removal allowed", &State{KubeContext: "kind-dc", Instance: "a", AllowLegacyDbRemoval: true}},
		{"local context", &State{KubeContext: "kind-local", Instance: "a"}},
		{
			"restoring both stores",
			&State{KubeContext: "kind-dc", Instance: "a", Restore: RestorePlan{
				RdbFrom: "dc-rdb", RdbTargetTime: "2026-07-28 03:00:00+00",
				TsdbFrom: "dc-tsdb", TsdbTargetTime: "2026-07-26 01:02:03+00",
			}},
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			if tc.st.Values == nil {
				tc.st.Values = map[string]string{}
			}
			if _, _, err := splitVars(infraVars(tc.st)); err != nil {
				t.Errorf("dcctl passes a variable no OpenTofu root declares: %v", err)
			}
		})
	}
}

// 🔴 AND THE COUNTERWEIGHT, because the test above passes just as well against a
// splitVars that never returns an error at all. An unrecognised name must STOP the
// run rather than be filtered out silently.
func TestAVariableNeitherRootDeclaresStopsTheRun(t *testing.T) {
	_, _, err := splitVars([]string{"namespace=dc-system", "not_a_real_variable=1"})
	if err == nil {
		t.Fatal("a variable no root declares was accepted; it would be dropped from both " +
			"applies and the apply would silently use a default nobody chose")
	}
	for _, want := range []string{
		"not_a_real_variable", // which one
		"DEFAULT",             // why silence is the danger
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	// The valid name must not be blamed alongside it.
	if strings.Contains(err.Error(), "namespace") {
		t.Errorf("the refusal names a variable that IS declared: %v", err)
	}
}

func TestAMalformedAssignmentIsRefused(t *testing.T) {
	if _, _, err := splitVars([]string{"no_equals_sign"}); err == nil {
		t.Error("an assignment with no '=' was accepted; tofu would reject it far later, " +
			"after the cluster apply had already run")
	}
}

// A variable BOTH roots declare goes to both, deliberately — they are two halves of
// one instance, and a value reaching only one of them means the halves disagree.
func TestAVariableBothRootsDeclareReachesBoth(t *testing.T) {
	cluster, instance, err := splitVars([]string{"namespace=dc-system"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		root string
		got  []string
	}{{"cluster", cluster}, {"instance", instance}} {
		if len(tc.got) != 1 || tc.got[0] != "namespace=dc-system" {
			t.Errorf("the %s root was passed %v, want the shared namespace; the two halves "+
				"of one instance would disagree about where the infrastructure is", tc.root, tc.got)
		}
	}
}

// ...and the half that proves the routing is a routing rather than a broadcast.
//
// 🔑 The two names are chosen for what they OWN: the broker is per-instance, the
// CloudNativePG operator is a cluster prerequisite. Passing either to the wrong root
// is "Value for undeclared variable" at apply time, in the middle of a bootstrap.
func TestEachRootIsPassedOnlyWhatItDeclares(t *testing.T) {
	cluster, instance, err := splitVars([]string{
		"nats_chart_version=2.14.4",
		"cnpg_chart_version=0.29.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cluster, ","); got != "cnpg_chart_version=0.29.0" {
		t.Errorf("the cluster root was passed %q; the broker's chart pin is not its to receive", got)
	}
	if got := strings.Join(instance, ","); got != "nats_chart_version=2.14.4" {
		t.Errorf("the instance root was passed %q; the operator's chart pin is not its to receive", got)
	}
}

// The parser's own control, and the reason it is a separate test: rootDeclaredVars
// returning an EMPTY set would make splitVars refuse every variable — loudly, so it
// would be caught — but it would also make the two tests above pass for the wrong
// reason if only one root parsed. Assert each root parses something on its own.
func TestBothRootsDeclareVariablesThatCanBeRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		root string
	}{
		{"cluster", assets.ClusterRootDir},
		{"instance", assets.InstanceRootDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fsys = assets.OpenTofuCluster()
			if tc.root == assets.InstanceRootDir {
				fsys = assets.OpenTofuInstance()
			}
			names, err := rootDeclaredVars(fsys)
			if err != nil {
				t.Fatalf("reading the %s root's variables: %v", tc.name, err)
			}
			if len(names) == 0 {
				t.Fatalf("the %s root parsed zero variable declarations", tc.name)
			}
			// kubeconfig_context is the one variable BOTH roots must declare: each
			// configures its own provider from it, and a root that lost it would target
			// whatever context happened to be current.
			if !names["kubeconfig_context"] {
				t.Errorf("the %s root does not declare kubeconfig_context, so its provider "+
					"would target whichever context is current rather than the one dcctl chose",
					tc.name)
			}
		})
	}
}
