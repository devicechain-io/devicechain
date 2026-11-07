// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"strings"
	"testing"
)

// A Bearer credential handle in the signing namespace is refused when the rule is saved, with a
// message that points at signingSecretRef, rather than being accepted and refused on every firing.
func TestCompileRejectsASigningKeyAsABearerCredential(t *testing.T) {
	a := Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{
		URL: "https://x/y", SecretRef: "webhook-signing/hooks/prod/current",
	}}
	_, err := Compile(thresholdWith(SeverityCritical, a), testLimits)
	if err == nil || !strings.Contains(err.Error(), "signingSecretRef") {
		t.Fatalf("want a refusal pointing at signingSecretRef, got %v", err)
	}
	// The same handle is a perfectly good SIGNING handle: the namespace is prefixed by the service.
	ok := Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "https://x/y", SigningSecretRef: "webhook-signing/hooks/prod"}}
	if _, err := Compile(thresholdWith(SeverityCritical, ok), testLimits); err != nil {
		t.Fatalf("a signing handle is not restricted: %v", err)
	}
}
