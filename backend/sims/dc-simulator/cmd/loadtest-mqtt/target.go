// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/devicechain-io/dc-simulator/loadtest"
	"github.com/devicechain-io/dc-simulator/mqttload"
	"github.com/devicechain-io/dc-simulator/sim"
)

// targetFlags are the flags that only mean something with --target.
type targetFlags struct {
	target, credentials, topicTemplate, tapTarget, tapTopic string
	tapClientID, tapUsername, tapPasswordFile, baseline     string
	maxRegression                                           float64
	// tlsFlagSet says the operator gave --tls or --no-tls; tlsOn is what they asked for.
	tlsFlagSet, tlsOn bool
	// tlsInsecureSet says --tls-insecure was given; tlsInsecure is its VALUE. Presence is not
	// consent: --tls-insecure=false is the operator forcing verification.
	tlsInsecureSet, tlsInsecure bool
	mqttBrokerSet               bool
}

// seedOf is the handshake's seed, or 0 when there is no handshake (a target run).
func seedOf(hs *sim.Handshake) int64 {
	if hs == nil {
		return 0
	}
	return hs.Seed
}

// targetProfile turns the shared MQTT profile and the target flags into a target run. The
// target's scheme decides TLS, and a flag that contradicts it is refused, never overridden.
func targetProfile(base loadtest.MQTTProfile, f targetFlags) (loadtest.TargetProfile, error) {
	var zero loadtest.TargetProfile
	if f.mqttBrokerSet {
		return zero, fmt.Errorf("--mqtt-broker is for a DeviceChain gateway; with --target the broker is the target")
	}
	if f.credentials == "" {
		return zero, fmt.Errorf("--credentials is required with --target: the bring-your-own CSV, one row per device")
	}
	broker, useTLS, err := loadtest.ParseTarget(f.target)
	if err != nil {
		return zero, err
	}
	if f.tlsFlagSet && f.tlsOn != useTLS {
		return zero, fmt.Errorf("--tls=%v contradicts the target scheme (%s decides TLS; drop the flag)", f.tlsOn, f.target)
	}
	if f.tlsInsecureSet && !useTLS {
		return zero, fmt.Errorf("--tls-insecure means nothing on a plaintext target")
	}
	if err := mqttload.ValidateTopicTemplate(f.topicTemplate); err != nil {
		return zero, err
	}
	creds, err := mqttload.LoadCredentials(f.credentials, f.topicTemplate)
	if err != nil {
		return zero, err
	}
	if base.Devices != 0 && base.Devices != len(creds) {
		return zero, fmt.Errorf("--devices %d but the credential list has %d device(s): with --target the list IS the fleet; drop --devices", base.Devices, len(creds))
	}

	p := loadtest.TargetProfile{
		MQTTProfile: base, Credentials: creds, TopicTemplate: f.topicTemplate,
		MaxP99Regression: f.maxRegression,
	}
	p.MQTT.Broker, p.MQTT.TLS = broker, useTLS
	p.Tap = mqttload.TapConfig{
		Filter: f.tapTopic, ClientID: f.tapClientID, Username: f.tapUsername,
		Password: os.Getenv("DC_LOADTEST_TAP_PASSWORD"),
	}
	if f.tapPasswordFile != "" {
		b, err := os.ReadFile(f.tapPasswordFile)
		if err != nil {
			return zero, fmt.Errorf("read --tap-password-file: %w", err)
		}
		p.Tap.Password = strings.TrimRight(string(b), "\r\n")
	}
	if f.tapTarget != "" {
		tb, ttls, err := loadtest.ParseTarget(f.tapTarget)
		if err != nil {
			return zero, fmt.Errorf("--tap-target: %w", err)
		}
		p.Tap.Broker, p.Tap.TLS, p.Tap.TLSInsecure = tb, ttls, f.tlsInsecure && ttls
	}
	if f.baseline != "" {
		b, err := os.ReadFile(f.baseline)
		if err != nil {
			return zero, fmt.Errorf("read --baseline: %w", err)
		}
		var rep loadtest.MQTTReport
		if err := json.Unmarshal(b, &rep); err != nil {
			return zero, fmt.Errorf("--baseline is not a report: %w", err)
		}
		p.Baseline = &rep
	}
	return p, nil
}
