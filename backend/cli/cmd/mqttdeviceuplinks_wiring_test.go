// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// resetDeviceUplinksFlag puts the shared command's flag back to "not given", because the
// flag set is package state and a parsed value would leak into the next case.
func resetDeviceUplinksFlag(t *testing.T, c *cobra.Command) {
	t.Helper()
	f := c.Flags().Lookup("mqtt-device-uplinks")
	if f == nil {
		t.Fatalf("%s has no --mqtt-device-uplinks flag", c.Name())
	}
	reset := func() {
		_ = f.Value.Set("false")
		f.Changed = false
	}
	reset()
	t.Cleanup(reset)
}

func wantDeviceUplinksTri(t *testing.T, what string, got *bool, want string) {
	t.Helper()
	switch {
	case want == "nil" && got != nil:
		t.Errorf("%s: flag not given arrived as %v, want nil", what, *got)
	case want == "true" && (got == nil || !*got):
		t.Errorf("%s: --mqtt-device-uplinks arrived as %v, want &true", what, got)
	case want == "false" && (got == nil || *got):
		t.Errorf("%s: --mqtt-device-uplinks=false arrived as %v, want &false", what, got)
	}
}

var deviceUplinksArgv = []struct {
	name string
	argv []string
	want string
}{
	{"not given", nil, "nil"},
	{"given", []string{"--mqtt-device-uplinks"}, "true"},
	{"given true", []string{"--mqtt-device-uplinks=true"}, "true"},
	{"given false", []string{"--mqtt-device-uplinks=false"}, "false"},
}

// 🔴 argv -> flag set -> Options -> the State the pipeline starts from. Deleting the copy
// in either literal must fail here: the choice would otherwise stop at the command layer
// and every bootstrap would record "never chose" without a test noticing.
func TestBootstrapCarriesMqttDeviceUplinksFromArgvToTheState(t *testing.T) {
	for _, tc := range deviceUplinksArgv {
		t.Run(tc.name, func(t *testing.T) {
			resetDeviceUplinksFlag(t, bootstrapCmd)
			if err := bootstrapCmd.ParseFlags(tc.argv); err != nil {
				t.Fatal(err)
			}
			flag := mqttDeviceUplinksFlag(bootstrapCmd.Flags().Changed, bootstrapMqttDeviceUplinks)
			opts := bootstrapOptions("prod", nil, nil, flag)
			wantDeviceUplinksTri(t, "bootstrap flag", flag, tc.want)
			wantDeviceUplinksTri(t, "bootstrap options", opts.MqttDeviceUplinks, tc.want)
			store := &dcv1beta1.ObjectStoreSpec{Backend: "filesystem"}
			st := bootstrapStateFor(opts, bootstrap.ClusterBinding{}, "uid", "local", nil, nil, nil, store, true, bootstrap.EscrowPlan{})
			if st.ObjectStore != store || !st.ObjectStoreExplicit {
				t.Errorf("the object store did not reach the state: %v explicit=%v", st.ObjectStore, st.ObjectStoreExplicit)
			}
			wantDeviceUplinksTri(t, "bootstrap state", st.MqttDeviceUplinks, tc.want)
		})
	}
}

// argv -> flag set -> the UpgradeOptions handed to Upgrade.
func TestUpgradeCarriesMqttDeviceUplinksFromArgvToTheOptions(t *testing.T) {
	for _, tc := range deviceUplinksArgv {
		t.Run(tc.name, func(t *testing.T) {
			resetDeviceUplinksFlag(t, upgradeCmd)
			if err := upgradeCmd.ParseFlags(tc.argv); err != nil {
				t.Fatal(err)
			}
			flag := mqttDeviceUplinksFlag(upgradeCmd.Flags().Changed, upgradeMqttDeviceUplinks)
			opts := upgradeOptions("prod", nil, flag, nil)
			wantDeviceUplinksTri(t, "upgrade flag", flag, tc.want)
			wantDeviceUplinksTri(t, "upgrade options", opts.MqttDeviceUplinks, tc.want)
		})
	}
}
