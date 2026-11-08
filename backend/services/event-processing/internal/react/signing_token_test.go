// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

// The idempotency token is durable and the connectors service forwards it as the webhook-id, so an
// unsigned httpCall must mint the key it minted before signing existed. The expected string is
// spelled out here (NUL is the field delimiter), not computed from the function under test.
func TestUnsignedHTTPCallContentKeyIsUnchanged(t *testing.T) {
	nul := string(rune(0))
	a := rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &rules.HTTPCallAction{
		URL: "https://x/y", BodyTemplate: `"b"`, SecretRef: "s",
	}}
	want := "httpCall" + nul + "https://x/y" + nul + "POST" + nul + `"b"` + nul + "s" + nul
	if got := actionContentKey(a); got != want {
		t.Fatalf("unsigned content key changed:\n got %q\nwant %q", got, want)
	}
	guarded := a
	guarded.Guard = "value > 1"
	if got := actionContentKey(guarded); got != want+nul+"value > 1" {
		t.Fatalf("guarded unsigned content key changed: %q", got)
	}
}

func TestSigningHandleFoldsIntoTheContentKey(t *testing.T) {
	base := rules.HTTPCallAction{URL: "https://x/y"}
	signedA, signedB := base, base
	signedA.SigningSecretRef, signedB.SigningSecretRef = "k1", "k2"
	keys := map[string]string{}
	for name, h := range map[string]rules.HTTPCallAction{"unsigned": base, "k1": signedA, "k2": signedB} {
		h := h
		k := actionContentKey(rules.Action{Type: rules.ActionHTTPCall, HTTPCall: &h})
		if other, dup := keys[k]; dup {
			t.Fatalf("%s and %s share the content key %q", name, other, k)
		}
		keys[k] = name
	}
}
