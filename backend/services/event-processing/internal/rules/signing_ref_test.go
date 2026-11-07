// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"strings"
	"testing"
)

func TestCompileAcceptsAWellFormedSigningHandle(t *testing.T) {
	a := Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{
		URL: "https://hooks.example/x", SecretRef: "hooks/auth", SigningSecretRef: "hooks/prod.v2",
	}}
	if _, err := Compile(thresholdWith(SeverityCritical, a), testLimits); err != nil {
		t.Fatalf("a signed httpCall should compile: %v", err)
	}
}

func TestCompileRejectsAMalformedSigningHandle(t *testing.T) {
	for _, bad := range []string{"bad handle!", "../../etc/passwd", "/abs", "a//b", strings.Repeat("x", 257)} {
		a := Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "https://x/y", SigningSecretRef: bad}}
		_, err := Compile(thresholdWith(SeverityCritical, a), testLimits)
		if err == nil || !strings.Contains(err.Error(), "signingSecretRef") {
			t.Fatalf("handle %q: want a signingSecretRef rejection, got %v", bad, err)
		}
	}
}

// Two calls that differ only in whether, or with which key, they sign are different requests: the
// duplicate gate must let both through, and an identical pair must still be a duplicate.
func TestSigningFoldsIntoTheDedupIdentity(t *testing.T) {
	call := func(handle string) Action {
		return Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{URL: "https://x/y", SigningSecretRef: handle}}
	}
	if _, err := Compile(thresholdWith(SeverityCritical, call(""), call("k1"), call("k2")), testLimits); err != nil {
		t.Fatalf("calls differing only by signing handle should be distinct: %v", err)
	}
	if _, err := Compile(thresholdWith(SeverityCritical, call("k1"), call("k1")), testLimits); err == nil ||
		!strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("identical signed calls should be a duplicate, got %v", err)
	}
}

// Golden: an unsigned call keeps the identity it had before signing existed. The expected string is
// written out in full, not derived from the code under test.
func TestUnsignedDedupKeyIsUnchanged(t *testing.T) {
	a := Action{Type: ActionHTTPCall, HTTPCall: &HTTPCallAction{
		URL: "https://x/y", BodyTemplate: `"b"`, SecretRef: "s", Headers: map[string]string{"X-Env": "prod"},
	}}
	want := "httpCall|https://x/y|POST|\"b\"|s|" + HeaderKey(a.HTTPCall.Headers)
	if got := ActionDedupKey(a); got != want {
		t.Fatalf("unsigned dedup key changed:\n got %q\nwant %q", got, want)
	}
	if SigningKeySeg("") != "" {
		t.Fatal("an unsigned call must add nothing to its identity")
	}
	signed := a
	h := *a.HTTPCall
	h.SigningSecretRef = "k"
	signed.HTTPCall = &h
	if ActionDedupKey(signed) == want {
		t.Fatal("a signed call must not share an unsigned call's identity")
	}
}
