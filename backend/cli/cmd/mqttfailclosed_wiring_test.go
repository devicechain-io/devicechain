// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// resetFailClosedFlag puts the shared command's flag back to "not given", because the
// flag set is package state and a parsed value would leak into the next case.
func resetFailClosedFlag(t *testing.T, c *cobra.Command) {
	t.Helper()
	f := c.Flags().Lookup("mqtt-fail-closed")
	if f == nil {
		t.Fatalf("%s has no --mqtt-fail-closed flag", c.Name())
	}
	reset := func() {
		_ = f.Value.Set("false")
		f.Changed = false
	}
	reset()
	t.Cleanup(reset)
}

func wantTri(t *testing.T, what string, got *bool, want string) {
	t.Helper()
	switch {
	case want == "nil" && got != nil:
		t.Errorf("%s: flag not given arrived as %v, want nil", what, *got)
	case want == "true" && (got == nil || !*got):
		t.Errorf("%s: --mqtt-fail-closed arrived as %v, want &true", what, got)
	case want == "false" && (got == nil || *got):
		t.Errorf("%s: --mqtt-fail-closed=false arrived as %v, want &false", what, got)
	}
}

var failClosedArgv = []struct {
	name string
	argv []string
	want string
}{
	{"not given", nil, "nil"},
	{"given", []string{"--mqtt-fail-closed"}, "true"},
	{"given true", []string{"--mqtt-fail-closed=true"}, "true"},
	{"given false", []string{"--mqtt-fail-closed=false"}, "false"},
}

// 🔴 argv -> flag set -> Options -> the State the pipeline starts from. Deleting the copy
// in either literal must fail here: the choice would otherwise stop at the command layer
// and every bootstrap would record "never chose" without a test noticing.
func TestBootstrapCarriesMqttFailClosedFromArgvToTheState(t *testing.T) {
	for _, tc := range failClosedArgv {
		t.Run(tc.name, func(t *testing.T) {
			resetFailClosedFlag(t, bootstrapCmd)
			if err := bootstrapCmd.ParseFlags(tc.argv); err != nil {
				t.Fatal(err)
			}
			flag := mqttFailClosedFlag(bootstrapCmd.Flags().Changed, bootstrapMqttFailClosed)
			opts := bootstrapOptions("prod", nil, flag)
			wantTri(t, "bootstrap options", opts.MqttFailClosed, tc.want)
			store := &dcv1beta1.ObjectStoreSpec{Backend: "filesystem"}
			st := bootstrapStateFor(opts, bootstrap.ClusterBinding{}, "uid", "local", nil, nil, "", nil, store, true, bootstrap.EscrowPlan{})
			if st.ObjectStore != store || !st.ObjectStoreExplicit {
				t.Errorf("the object store did not reach the state: %v explicit=%v", st.ObjectStore, st.ObjectStoreExplicit)
			}
			wantTri(t, "bootstrap state", st.MqttFailClosed, tc.want)
		})
	}
}

// argv -> flag set -> the UpgradeOptions handed to Upgrade.
func TestUpgradeCarriesMqttFailClosedFromArgvToTheOptions(t *testing.T) {
	for _, tc := range failClosedArgv {
		t.Run(tc.name, func(t *testing.T) {
			resetFailClosedFlag(t, upgradeCmd)
			if err := upgradeCmd.ParseFlags(tc.argv); err != nil {
				t.Fatal(err)
			}
			flag := mqttFailClosedFlag(upgradeCmd.Flags().Changed, upgradeMqttFailClosed)
			opts := upgradeOptions("prod", "", flag, nil)
			wantTri(t, "upgrade options", opts.MqttFailClosed, tc.want)
		})
	}
}
