// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package integration is event-sources' half of an inbound integration: a tenant's authenticated
// sender of device data over HTTP, such as a LoRaWAN network server or a webhook.
//
// It holds the pieces the ingest route is built from and that carry no HTTP of their own: the
// envelope codecs (ChirpStack v4 JSON, The Things Stack v3 JSON, generic JSON, generic binary),
// the flattening of a network server's already-decoded object into measurement readings, the
// cached pre-check that authenticates a sender before a byte of its body is read, the per-
// integration bad-credential budget, and the cached device lookup.
//
// Nothing here publishes. The route owns the gates and the publish, and is the only code that
// may mark an event as an integration's (see the guard in processor).
package integration

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/devicealias"
)

// Type is the kind of sender an integration is registered as. The route parses a body by the
// registered type and never by what the body looks like.
type Type string

const (
	ChirpStackV4  Type = "CHIRPSTACK_V4"
	TTSV3         Type = "TTS_V3"
	GenericJSON   Type = "GENERIC_JSON"
	GenericBinary Type = "GENERIC_BINARY"
)

// Known reports whether t is one of the four types. An integration of any other type is refused
// rather than parsed as a guess.
func (t Type) Known() bool {
	switch t {
	case ChirpStackV4, TTSV3, GenericJSON, GenericBinary:
		return true
	}
	return false
}

// The identity spellings device-management stores (model.IntegrationIdentity*).
const (
	identityDevEUI      = "DEV_EUI"
	identityDeviceID    = "DEVICE_ID"
	identityAliasPrefix = "ALIAS:"
)

// GenericSettings is a generic sender's settings document, exactly as device-management stores
// it. Every pointer is an RFC 6901 JSON pointer.
type GenericSettings struct {
	IdFrom        string `json:"idFrom"`
	ItemsPointer  string `json:"itemsPointer,omitempty"`
	TimePointer   string `json:"timePointer,omitempty"`
	FramePointer  string `json:"framePointer,omitempty"`
	FrameEncoding string `json:"frameEncoding,omitempty"`
}

// Descriptor is what the pre-check learns about an integration: enough to pick a codec and to
// say what the sender's identifier is. It is read from device-management (integrationAuth) and
// cached, so it can be up to the cache window stale; device-management re-checks everything
// that matters when the event reaches its resolver.
type Descriptor struct {
	Token       string  `json:"token"`
	Type        Type    `json:"type"`
	Enabled     bool    `json:"enabled"`
	Identity    string  `json:"identity"`
	Generic     *string `json:"generic"`
	LinkMetrics bool    `json:"linkMetrics"`

	settings *GenericSettings
}

// Prepare parses the generic settings document of a generic integration. It is called once, when
// the descriptor is read, so a malformed document fails the pre-check (503) and not each uplink.
func (d *Descriptor) Prepare() error {
	if !d.Type.Known() {
		return fmt.Errorf("integration %q has the unknown type %q", d.Token, d.Type)
	}
	if _, err := d.Scheme(); err != nil {
		return err
	}
	if d.Type != GenericJSON && d.Type != GenericBinary {
		return nil
	}
	if d.Generic == nil || *d.Generic == "" {
		return fmt.Errorf("integration %q is %s and carries no generic settings", d.Token, d.Type)
	}
	var s GenericSettings
	if err := json.Unmarshal([]byte(*d.Generic), &s); err != nil {
		return fmt.Errorf("integration %q has unreadable generic settings: %w", d.Token, err)
	}
	if s.IdFrom == "" {
		return fmt.Errorf("integration %q has generic settings with no idFrom", d.Token)
	}
	d.settings = &s
	return nil
}

// Settings is the parsed generic settings, nil for the network-server types.
func (d *Descriptor) Settings() *GenericSettings { return d.settings }

// Scheme is the alias scheme this integration's identifier is in.
func (d *Descriptor) Scheme() (string, error) {
	switch {
	case d.Identity == identityDevEUI:
		return devicealias.SchemeDevEUI, nil
	case d.Identity == identityDeviceID:
		return devicealias.SchemeTTSDeviceID, nil
	case strings.HasPrefix(d.Identity, identityAliasPrefix):
		scheme := strings.TrimPrefix(d.Identity, identityAliasPrefix)
		if devicealias.IsCustomScheme(scheme) {
			return scheme, nil
		}
	}
	return "", fmt.Errorf("integration %q has the unknown identity %q", d.Token, d.Identity)
}
