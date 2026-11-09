// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"reflect"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/egress"
)

func TestParseDevAllowEgress(t *testing.T) {
	got, err := ParseDevAllowEgress([]string{"192.168.1.20", "172.18.0.0/16", "192.168.1.20", " 10.0.0.0/8 "})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "172.18.0.0/16", "192.168.1.20/32"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	if got, err := ParseDevAllowEgress(nil); got != nil || err != nil {
		t.Errorf("not given must be nil, got %v %v", got, err)
	}
	clear, err := ParseDevAllowEgress([]string{"none"})
	if err != nil || clear == nil || len(clear) != 0 {
		t.Errorf("none must be an empty non-nil list, got %#v %v", clear, err)
	}
}

func TestParseDevAllowEgressRefusals(t *testing.T) {
	for _, bad := range [][]string{
		{"169.254.169.254"}, {"169.254.0.0/16"}, {"0.0.0.0/0"}, {"127.0.0.1"}, {"100.100.100.200"},
		{"168.63.129.16"}, {"fd00:ec2::254"}, {"::/0"}, {"192.168.1.5/24"}, {"example.com"},
		{"host.docker.internal:8080"}, {"100.64.0.0/10"}, {"none", "10.0.0.1"}, {""}, {"224.0.0.1"},
	} {
		if got, err := ParseDevAllowEgress(bad); err == nil {
			t.Errorf("%v must be refused, got %v", bad, got)
		}
	}
}

// Whatever dcctl accepts, the guard the services build from it must accept too: a flag that
// passes here and crash-loops the services at startup is the worst place to find out.
func TestWhatDcctlAcceptsTheGuardAccepts(t *testing.T) {
	list, err := ParseDevAllowEgress([]string{"192.168.0.0/16", "172.16.0.0/12", "10.0.0.0/8", "fd12::/16", "100.64.0.0/11", "100.64.7.9"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := egress.ParseDevPrefixes(list); err != nil {
		t.Errorf("the guard refuses what dcctl accepted: %v", err)
	}
}

func TestDevAllowEgressIsLocalClustersOnly(t *testing.T) {
	list := []string{"192.168.1.0/24"}
	for _, ok := range []string{"kind-devicechain", "kind-x", "minikube", "k3d-dev", "docker-desktop", "rancher-desktop", ""} {
		if err := CheckDevAllowEgressContext(ok, list); err != nil {
			t.Errorf("%q should be allowed: %v", ok, err)
		}
	}
	// Substring matches (looksLocal's heuristic) must NOT unlock it.
	for _, no := range []string{"gke_proj_zone_prod", "prod-kind-x", "arn:aws:eks:us-east-1:1:cluster/minikube-prod", "my-docker-desktop"} {
		err := CheckDevAllowEgressContext(no, list)
		if err == nil || !strings.Contains(err.Error(), "local development cluster") {
			t.Errorf("%q must be refused, got %v", no, err)
		}
	}
	// Clearing is always fine.
	if err := CheckDevAllowEgressContext("gke_proj_zone_prod", []string{}); err != nil {
		t.Error(err)
	}
}

func TestSettleDevAllowEgress(t *testing.T) {
	rec := []string{"10.0.0.0/8"}
	t.Run("a flagless run keeps what is recorded", func(t *testing.T) {
		st := &State{Instance: "i", KubeContext: "kind-x"}
		if err := settleDevAllowEgress(st, rec, nil); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(st.DevAllowEgress, rec) || st.DevAllowEgressChange {
			t.Errorf("%+v", st)
		}
	})
	t.Run("a flag replaces it and is recorded", func(t *testing.T) {
		st := &State{Instance: "i", KubeContext: "kind-x"}
		if err := settleDevAllowEgress(st, rec, []string{"192.168.1.0/24"}); err != nil {
			t.Fatal(err)
		}
		if !st.DevAllowEgressChange || st.DevAllowEgress[0] != "192.168.1.0/24" {
			t.Errorf("%+v", st)
		}
	})
	t.Run("none clears it", func(t *testing.T) {
		st := &State{Instance: "i", KubeContext: "kind-x"}
		if err := settleDevAllowEgress(st, rec, []string{}); err != nil {
			t.Fatal(err)
		}
		if len(st.DevAllowEgress) != 0 || !st.DevAllowEgressChange {
			t.Errorf("%+v", st)
		}
	})
	t.Run("a recorded allowance on a real cluster is refused", func(t *testing.T) {
		st := &State{Instance: "i", KubeContext: "gke_prod"}
		if err := settleDevAllowEgress(st, rec, nil); err == nil {
			t.Error("a hand-edited declaration must not smuggle the allowance onto a real cluster")
		}
	})
	t.Run("a recorded range this release refuses is refused by name", func(t *testing.T) {
		st := &State{Instance: "i", KubeContext: "kind-x"}
		err := settleDevAllowEgress(st, []string{"169.254.0.0/16"}, nil)
		if err == nil || !strings.Contains(err.Error(), "not accepted by this release") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("never given, never recorded is nothing", func(t *testing.T) {
		st := &State{Instance: "i", KubeContext: "gke_prod"}
		if err := settleDevAllowEgress(st, nil, nil); err != nil || st.DevAllowEgressChange {
			t.Errorf("%v %+v", err, st)
		}
	})
}

func TestDevAllowEgressRendersOnlyWhenSet(t *testing.T) {
	path := []string{"instance", "config", "infrastructure", "egress"}
	st := persistenceState(true, false, "default")
	if _, ok := lookupPath(helmValues(st), path); ok {
		t.Error("an instance that never asked renders no egress key")
	}
	st.DevAllowEgress = []string{"192.168.1.0/24"}
	v, ok := lookupPath(helmValues(st), append(path, "devAllowedDestinations"))
	if !ok || !reflect.DeepEqual(v, []interface{}{"192.168.1.0/24"}) {
		t.Errorf("got %v %v", v, ok)
	}
	// And it is NOT the operator's key.
	if _, ok := lookupPath(helmValues(st), append(path, "allowedDestinations")); ok {
		t.Error("dcctl must never write the operator's allowedDestinations")
	}
}

// The overlay must still refuse the egress keys: the dev flag is the ONLY dcctl door.
func TestTheOverlayStillRefusesAllowedDestinations(t *testing.T) {
	for _, k := range []string{"allowedDestinations", "devAllowedDestinations"} {
		why := refuseOverlayKey([]string{"instance", "config", "infrastructure", "egress", k})
		if why == "" {
			t.Errorf("an overlay must not set egress.%s", k)
		}
	}
}

func TestDevAllowEgressRecordRoundTrip(t *testing.T) {
	dyn := declarationClient(declaredInstance(t, "prod", nil))
	if err := recordDevAllowEgress(t.Context(), dyn, "prod", []string{"10.0.0.0/8", "192.168.1.20/32"}); err != nil {
		t.Fatal(err)
	}
	back := readBack(t, dyn, "prod")
	if got := DevAllowEgressOf(back); !reflect.DeepEqual(got, []string{"10.0.0.0/8", "192.168.1.20/32"}) {
		t.Errorf("recorded %v", got)
	}
	if err := recordDevAllowEgress(t.Context(), dyn, "prod", nil); err != nil {
		t.Fatal(err)
	}
	if got := DevAllowEgressOf(readBack(t, dyn, "prod")); len(got) != 0 {
		t.Errorf("not cleared: %v", got)
	}
}

// The verbs' calls are held by the source, as the overlay's are: Upgrade and the declaration
// step need a cluster to drive.
func TestDevAllowEgressIsSettledAndRecordedByBothVerbs(t *testing.T) {
	up, _ := overlayCalls(t, "upgrade.go", "Upgrade")
	if len(up["recordDevAllowEgress"]) == 0 {
		t.Error("Upgrade no longer records the development egress allowance")
	}
	ap, _ := overlayCalls(t, "upgradestate.go", "applyUpgradeDeclaration")
	if len(ap["settleDevAllowEgress"]) == 0 || len(ap["sayDevAllowEgress"]) == 0 {
		t.Error("an upgrade no longer settles and reports the development egress allowance")
	}
	st, _ := overlayCalls(t, "step_claim.go", "stepDeclareInstance")
	for _, want := range []string{"settleDevAllowEgress", "sayDevAllowEgress", "recordDevAllowEgressFor"} {
		if len(st[want]) == 0 {
			t.Errorf("stepDeclareInstance no longer calls %s", want)
		}
	}
	if len(st["settleDevAllowEgress"]) > 0 && len(st["writeInstanceDeclaration"]) > 0 &&
		st["settleDevAllowEgress"][0] > st["writeInstanceDeclaration"][0] {
		t.Error("the allowance is judged after the declaration is written")
	}
}
