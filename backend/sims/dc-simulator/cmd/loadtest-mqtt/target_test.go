// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-simulator/loadtest"
)

func writeCreds(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "creds.csv")
	if err := os.WriteFile(p, []byte("device,token\na,tok-a\nb,tok-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTargetProfileTakesTLSFromTheScheme(t *testing.T) {
	p, err := targetProfile(loadtest.MQTTProfile{}, targetFlags{
		target: "mqtts://broker.example:8883", credentials: writeCreds(t), topicTemplate: "devices/{device}/events",
		tlsOn: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.MQTT.TLS || p.MQTT.Broker != "ssl://broker.example:8883" || len(p.Credentials) != 2 {
		t.Fatalf("%+v", p.MQTT)
	}
}

// A flag that contradicts the scheme is refused, never silently overridden: the report states
// what was observed on the sockets, and a run that dialled something else than it was told
// would mislabel itself.
func TestTargetProfileRefusesContradictions(t *testing.T) {
	creds := writeCreds(t)
	base := targetFlags{target: "mqtt://broker:1883", credentials: creds, topicTemplate: "d/{device}"}
	for name, tc := range map[string]struct {
		mut  func(*targetFlags)
		want string
	}{
		"tls flag vs mqtt scheme": {func(f *targetFlags) { f.tlsFlagSet, f.tlsOn = true, true }, "contradicts"},
		"insecure on plaintext":   {func(f *targetFlags) { f.tlsInsecureSet = true }, "plaintext"},
		"gateway broker flag":     {func(f *targetFlags) { f.mqttBrokerSet = true }, "DeviceChain gateway"},
		"no credentials":          {func(f *targetFlags) { f.credentials = "" }, "--credentials"},
		"websocket target":        {func(f *targetFlags) { f.target = "wss://b/mqtt" }, "not driven"},
		"template without device": {func(f *targetFlags) { f.topicTemplate = "d/events" }, "{device}"},
		"missing baseline file":   {func(f *targetFlags) { f.baseline = filepath.Join(t.TempDir(), "nope.json") }, "--baseline"},
	} {
		t.Run(name, func(t *testing.T) {
			f := base
			tc.mut(&f)
			if _, err := targetProfile(loadtest.MQTTProfile{}, f); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTargetProfileDevicesMustMatchTheList(t *testing.T) {
	_, err := targetProfile(loadtest.MQTTProfile{Devices: 5}, targetFlags{
		target: "mqtt://b:1883", credentials: writeCreds(t), topicTemplate: "d/{device}",
	})
	if err == nil || !strings.Contains(err.Error(), "list IS the fleet") {
		t.Fatalf("err %v", err)
	}
}

// --tls-insecure=false is the operator FORCING verification: the tap must verify, and only an
// explicit =true may switch it off.
func TestTargetProfileTapInsecureReadsTheFlagValue(t *testing.T) {
	creds := writeCreds(t)
	for name, tc := range map[string]struct{ set, val, want bool }{
		"absent":         {false, false, false},
		"explicit false": {true, false, false},
		"explicit true":  {true, true, true},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := targetProfile(loadtest.MQTTProfile{}, targetFlags{
				target: "mqtts://broker:8883", credentials: creds, topicTemplate: "d/{device}",
				tapTarget: "mqtts://tap:8883", tlsInsecureSet: tc.set, tlsInsecure: tc.val,
			})
			if err != nil {
				t.Fatal(err)
			}
			if p.Tap.TLSInsecure != tc.want {
				t.Fatalf("Tap.TLSInsecure = %v, want %v", p.Tap.TLSInsecure, tc.want)
			}
		})
	}
}
