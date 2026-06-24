// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
)

// CredentialType.Valid accepts the known credential types and rejects others
// (ADR-014).
func TestCredentialTypeValid(t *testing.T) {
	for _, valid := range []CredentialType{
		CredentialAccessToken,
		CredentialX509Certificate,
		CredentialMqttBasic,
	} {
		if !valid.Valid() {
			t.Errorf("known credential type %q rejected", valid)
		}
	}
	for _, invalid := range []CredentialType{"", "BOGUS", "access_token"} {
		if invalid.Valid() {
			t.Errorf("unknown credential type %q accepted", invalid)
		}
	}
}

func TestCredentialTypeString(t *testing.T) {
	if CredentialAccessToken.String() != "ACCESS_TOKEN" {
		t.Fatalf("unexpected string value: %s", CredentialAccessToken.String())
	}
}
