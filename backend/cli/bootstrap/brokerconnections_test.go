// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"slices"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

func TestAdoptBrokerMaxConnections(t *testing.T) {
	cases := []struct {
		name       string
		recorded   int
		given      *int
		want       int
		wantChange bool
		wantErr    string
	}{
		{"never set, not given", 0, nil, 0, false, ""},
		{"set by the flag", 0, intp(20000), 20000, true, ""},
		{"a flagless run keeps the recorded cap", 20000, nil, 20000, false, ""},
		{"the same value is no change", 20000, intp(20000), 20000, false, ""},
		{"0 clears it", 20000, intp(0), 0, true, ""},
		{"negative is refused", 0, intp(-1), 0, false, "want 0"},
		{"absurd is refused", 0, intp(maxBrokerConnections + 1), 0, false, "want 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &State{}
			err := adoptBrokerMaxConnections(st, tc.recorded, tc.given)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || st.BrokerMaxConnections != tc.want || st.BrokerMaxConnectionsChange != tc.wantChange {
				t.Fatalf("got %d change=%v err=%v, want %d change=%v", st.BrokerMaxConnections, st.BrokerMaxConnectionsChange, err, tc.want, tc.wantChange)
			}
		})
	}
}

// The cap is recorded, read back, and handed to the apply only when set.
func TestBrokerMaxConnectionsIsRecordedAndHandedToTheApplyOnlyWhenSet(t *testing.T) {
	spec := InstanceSpecFrom(&State{Profile: "default", BrokerMaxConnections: 20000}, ClusterBinding{}, "local")
	if spec.BrokerMaxConnections != 20000 {
		t.Fatalf("declaration = %+v", spec)
	}
	st := &State{}
	applyDeclaration(st, dcv1beta1.InstanceSpec{BrokerMaxConnections: 20000})
	if st.BrokerMaxConnections != 20000 {
		t.Fatal("a flagless upgrade does not read the recorded cap back")
	}

	has := func(vars []string, want string) bool { return slices.Contains(vars, want) }
	on := &State{BrokerMaxConnections: 20000, Values: map[string]string{"natsCalloutIssuerPublic": "AI"}}
	if !has(infraVars(on), "nats_max_connections=20000") {
		t.Errorf("the apply is not handed the cap: %v", infraVars(on))
	}
	off := &State{Values: map[string]string{"natsCalloutIssuerPublic": "AI"}}
	for _, v := range infraVars(off) {
		if strings.HasPrefix(v, "nats_max_connections") {
			t.Errorf("an instance that never set a cap was handed %q", v)
		}
	}
}

// Every static login is held to the NATS protocol except event-sources' (and the transitional shared
// one), and the apply is told so.
func TestTheApplyHoldsPlatformLoginsToTheNatsProtocol(t *testing.T) {
	st := &State{Values: map[string]string{"natsCalloutIssuerPublic": "AI"}}
	var got string
	for _, v := range infraVars(st) {
		if strings.HasPrefix(v, "nats_mqtt_capable_users=") {
			got = v
		}
	}
	if got != `nats_mqtt_capable_users=["dc-event-sources-g1"]` {
		t.Fatalf("got %q; only event-sources' login may speak MQTT once the shared login is retired", got)
	}
	st.Values["natsServicePassword"] = "pw"
	if !slices.Contains(mqttCapableUsers(st), "dc_service") {
		t.Fatal("the transitional shared login stands in for event-sources and must keep MQTT while it exists")
	}
}
