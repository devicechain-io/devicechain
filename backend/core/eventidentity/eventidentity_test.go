// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventidentity

import "testing"

type viewed struct{ N int }

func (v viewed) IdentityPayload() any { return viewed{N: 0} }

func TestPayloadUsesTheViewForValueAndPointer(t *testing.T) {
	if got := Payload(viewed{N: 5}); got != (viewed{}) {
		t.Fatalf("value: got %v", got)
	}
	if got := Payload(&viewed{N: 5}); got != (viewed{}) {
		t.Fatalf("pointer: got %v", got)
	}
}

func TestPayloadPassesThroughWhatHasNoView(t *testing.T) {
	in := struct{ A int }{3}
	if got := Payload(in); got != in {
		t.Fatalf("got %v", got)
	}
}

func TestPayloadNilPointerDoesNotPanic(t *testing.T) {
	var p *viewed
	if got := Payload(p); got != any(p) {
		t.Fatalf("got %v", got)
	}
}
