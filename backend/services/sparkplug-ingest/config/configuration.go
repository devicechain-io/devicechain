// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package config is the typed configuration for the Sparkplug ingest service
// (ADR-069): a stateful Sparkplug B Host Application that terminates edge-node
// telemetry over DeviceChain's own MQTT gateway.
package config

import (
	"fmt"
	"strings"
)

// DefaultHostId is the Sparkplug Host Application identity used when none is
// configured. It appears in the STATE topic (spBv1.0/STATE/{HostId}) that edge
// nodes watch to decide whether their primary host is online, so it must be a
// stable, MQTT-topic-safe token; "devicechain" is the platform default.
const DefaultHostId = "devicechain"

// SparkplugConfiguration configures the Host Application (ADR-069). It is
// deliberately small at SP1 — the adapter connects, announces STATE, subscribes,
// and logs; identity mapping, tenancy attribution, and presence emission arrive
// in later slices.
type SparkplugConfiguration struct {
	// HostId is this Host Application's Sparkplug identifier. It is the final
	// segment of the STATE topic (spBv1.0/STATE/{HostId}) an edge node subscribes
	// to in order to learn whether its primary host is online (Sparkplug 3.0), so
	// it must be unique among Host Applications on the broker and free of MQTT
	// topic metacharacters. Defaults to DefaultHostId.
	HostId string

	// Groups restricts the Sparkplug Group IDs this host subscribes to
	// (spBv1.0/{group}/#, one subscription per entry). Empty means subscribe to
	// EVERY group on the broker (spBv1.0/#) — the SP1 default, since group→tenant
	// attribution (which will scope this) is not wired until SP3. Each entry must
	// be a single MQTT topic level with no wildcard.
	Groups []string
}

// NewSparkplugConfiguration builds a defaulted configuration.
func NewSparkplugConfiguration() *SparkplugConfiguration {
	cfg := &SparkplugConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills unset fields (ADR-022 decision 1). Only HostId has a
// universal default; an empty Groups list is a meaningful value (subscribe to
// all groups), not an unset one, so it is left alone.
func (c *SparkplugConfiguration) ApplyDefaults() {
	if strings.TrimSpace(c.HostId) == "" {
		c.HostId = DefaultHostId
	}
}

// Validate fails the load closed on a configuration that would produce an
// invalid MQTT subscription or STATE topic (ADR-022 decision 1). A HostId or
// group carrying a topic separator or wildcard would silently mis-target the
// STATE announcement or over-subscribe, so both are rejected here rather than at
// the broker.
func (c *SparkplugConfiguration) Validate() error {
	if err := validateTopicToken("hostId", c.HostId); err != nil {
		return err
	}
	for i, g := range c.Groups {
		if err := validateTopicToken(fmt.Sprintf("groups[%d]", i), g); err != nil {
			return err
		}
	}
	return nil
}

// validateTopicToken requires a non-empty single MQTT topic level: no separator
// ('/'), no wildcards ('+'/'#'), and no NUL. These are exactly the characters
// that would let a value escape its intended topic level and either mis-address
// the retained STATE message or widen a subscription past its group.
func validateTopicToken(field, raw string) error {
	if raw == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.ContainsAny(raw, "/+#\x00") {
		return fmt.Errorf("%s: must be a single MQTT topic level with no '/', '+', '#', or NUL (got %q)", field, raw)
	}
	return nil
}
