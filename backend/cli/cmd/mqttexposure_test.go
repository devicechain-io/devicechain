// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

func resetStringFlags(t *testing.T, c *cobra.Command, names ...string) {
	t.Helper()
	reset := func() {
		for _, n := range names {
			f := c.Flags().Lookup(n)
			if f == nil {
				t.Fatalf("%s has no --%s flag", c.Name(), n)
			}
			_ = f.Value.Set("")
			f.Changed = false
		}
	}
	reset()
	t.Cleanup(reset)
}

// 🔴 argv -> flag set -> Options -> the State the pipeline starts from: deleting the copy in any
// of them would leave the flags parsed and the exposure never applied.
func TestBootstrapCarriesTheMQTTExposureFromArgvToTheState(t *testing.T) {
	resetStringFlags(t, bootstrapCmd, "mqtt-exposure", "mqtt-host")
	if err := bootstrapCmd.ParseFlags([]string{"--mqtt-exposure", "gateway", "--mqtt-host", "mqtt.example.com"}); err != nil {
		t.Fatal(err)
	}
	opts := bootstrapOptions("prod", nil, nil)
	if opts.MQTTExposure != "gateway" || opts.MQTTHost != "mqtt.example.com" {
		t.Fatalf("options = %q %q", opts.MQTTExposure, opts.MQTTHost)
	}
	st := bootstrapStateFor(opts, bootstrap.ClusterBinding{}, "uid", "local", nil, nil, nil, nil, false, bootstrap.EscrowPlan{})
	if st.MQTTExposureGiven != "gateway" || st.MQTTHostGiven != "mqtt.example.com" {
		t.Fatalf("state = %q %q", st.MQTTExposureGiven, st.MQTTHostGiven)
	}
}

func TestUpgradeCarriesTheMQTTExposureFromArgvToTheOptions(t *testing.T) {
	resetStringFlags(t, upgradeCmd, "mqtt-exposure", "mqtt-host")
	if err := upgradeCmd.ParseFlags([]string{"--mqtt-exposure=loadbalancer", "--mqtt-host=mqtt.example.com"}); err != nil {
		t.Fatal(err)
	}
	opts := upgradeOptions("prod", nil, nil)
	if opts.MQTTExposure != "loadbalancer" || opts.MQTTHost != "mqtt.example.com" {
		t.Fatalf("options = %q %q", opts.MQTTExposure, opts.MQTTHost)
	}
}

func TestInstallCarriesMQTTGateway(t *testing.T) {
	if got := mqttGatewayFlag(false, true); got != nil {
		t.Errorf("flag not given arrived as %v, want nil (keep what the controller has)", *got)
	}
	if got := mqttGatewayFlag(true, false); got == nil || *got {
		t.Errorf("--mqtt-gateway=false arrived as %v, want &false", got)
	}
	if got := mqttGatewayFlag(true, true); got == nil || !*got {
		t.Errorf("--mqtt-gateway arrived as %v, want &true", got)
	}
	// Off unless asked for: the default must not publish a port.
	if installCmd.Flags().Lookup("mqtt-gateway").DefValue != "false" {
		t.Error("--mqtt-gateway must default to off")
	}
	installMQTTGateway, installMQTTGatewayGiven = true, true
	t.Cleanup(func() { installMQTTGateway, installMQTTGatewayGiven = false, false })
	opts, err := installOptions(nil, bootstrap.RestorePlan{}, bootstrap.ImageSource{})
	if err != nil {
		t.Fatal(err)
	}
	if opts.MQTTGateway == nil || !*opts.MQTTGateway {
		t.Fatalf("install options dropped --mqtt-gateway: %v", opts.MQTTGateway)
	}
}

// The up-front check fails closed on every unsafe combination, before a cluster exists.
func TestMQTTExposureFlagsFailClosedUpFront(t *testing.T) {
	cases := []struct {
		name       string
		mode, host string
		noTLS      bool
		want       string // "" = accepted
	}{
		{"off by default", "", "", false, ""},
		{"gateway", "gateway", "mqtt.example.com", false, ""},
		{"loadbalancer", "loadbalancer", "mqtt.example.com", false, ""},
		{"none", "none", "", true, ""},
		{"no host", "gateway", "", false, "needs --mqtt-host"},
		{"bad host", "gateway", "mqtt", false, "fully-qualified"},
		{"TLS off", "gateway", "mqtt.example.com", true, "plaintext MQTT is never exposed"},
		{"TLS off with loadbalancer", "loadbalancer", "mqtt.example.com", true, "plaintext MQTT is never exposed"},
		{"unknown mode", "internet", "mqtt.example.com", false, "want gateway, loadbalancer or none"},
		{"host alone, malformed", "", "NOPE", false, "fully-qualified"},
	}
	for _, tc := range cases {
		err := bootstrap.CheckMQTTExposureFlags(tc.mode, tc.host, tc.noTLS)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.want)
		}
	}
}
