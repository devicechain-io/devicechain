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
		want       string // "" = accepted
	}{
		{"off by default", "", "", ""},
		{"gateway", "gateway", "mqtt.example.com", ""},
		{"loadbalancer", "loadbalancer", "mqtt.example.com", ""},
		{"none", "none", "", ""},
		{"no host", "gateway", "", "needs --mqtt-host"},
		{"bad host", "gateway", "mqtt", "fully-qualified"},
		{"unknown mode", "internet", "mqtt.example.com", "want gateway, loadbalancer or none"},
		{"host alone, malformed", "", "NOPE", "fully-qualified"},
	}
	for _, tc := range cases {
		err := bootstrap.CheckMQTTExposureFlags(tc.mode, tc.host)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.want)
		}
	}
}

// An upgrade may switch the mode and keep the recorded host, so the up-front check cannot call a
// missing host an error; the pipeline's settlement against the record still does. Bootstrap has no
// record, so its check keeps refusing. Negative control: routing the upgrade through
// CheckMQTTExposureFlags fails the first case.
func TestUpgradeMQTTExposureCheckAllowsARecordedHost(t *testing.T) {
	if err := bootstrap.CheckMQTTExposureFlagsForUpgrade("loadbalancer", ""); err != nil {
		t.Errorf("a mode switch that keeps the recorded host was refused up front: %v", err)
	}
	if err := bootstrap.CheckMQTTExposureFlags("loadbalancer", ""); err == nil {
		t.Error("bootstrap accepted a mode with no host")
	}
	if err := bootstrap.CheckMQTTExposureFlagsForUpgrade("loadbalancer", "mqtt"); err == nil {
		t.Error("a malformed host was accepted")
	}
	if err := bootstrap.CheckMQTTExposureFlagsForUpgrade("internet", ""); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

func TestBrokerMaxConnectionsReachesTheOptionsOnlyWhenGiven(t *testing.T) {
	if got := bootstrap.BrokerMaxConnectionsFromFlag(false, 5); got != nil {
		t.Errorf("a flag not given arrived as %d, want nil (keep what is recorded)", *got)
	}
	if got := bootstrap.BrokerMaxConnectionsFromFlag(true, 0); got == nil || *got != 0 {
		t.Errorf("--broker-max-connections=0 arrived as %v, want &0 (clear)", got)
	}
	bootstrapBrokerMaxConnections, bootstrapBrokerMaxConnectionsGiven = 20000, true
	t.Cleanup(func() { bootstrapBrokerMaxConnections, bootstrapBrokerMaxConnectionsGiven = 0, false })
	opts := bootstrapOptions("prod", nil, nil)
	if opts.BrokerMaxConnections == nil || *opts.BrokerMaxConnections != 20000 {
		t.Fatalf("bootstrap options dropped the cap: %v", opts.BrokerMaxConnections)
	}
	st := bootstrapStateFor(opts, bootstrap.ClusterBinding{}, "uid", "local", nil, nil, nil, nil, false, bootstrap.EscrowPlan{})
	if st.BrokerMaxConnectionsGiven == nil || *st.BrokerMaxConnectionsGiven != 20000 {
		t.Fatalf("the state dropped the cap: %v", st.BrokerMaxConnectionsGiven)
	}
}
