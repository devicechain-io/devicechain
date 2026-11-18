// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ruletest

import "testing"

func TestValidateConstructedExpectation(t *testing.T) {
	if err := (Expect{}).Validate(); err == nil {
		t.Fatal("empty expectation accepted")
	}
	if err := (Expect{Raised: &EdgeExpect{}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Expect{Intent: &IntentExpect{Kind: "unknown"}}).Validate(); err == nil {
		t.Fatal("unknown intent kind accepted")
	}
}
