// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/limit"
)

func jsonOfSize(n int) string {
	// {"k":"xxx"} padded to exactly n bytes.
	const overhead = len(`{"k":""}`)
	return `{"k":"` + strings.Repeat("x", n-overhead) + `"}`
}

func TestJSONInputOfBoundsItsInput(t *testing.T) {
	atBound := jsonOfSize(MaxJSONInputBytes)
	if len(atBound) != MaxJSONInputBytes {
		t.Fatalf("fixture is %d bytes", len(atBound))
	}
	got, err := JSONInputOf("metadata", &atBound)
	if err != nil || got == nil {
		t.Fatalf("a value at the bound must be stored: %v", err)
	}

	over := jsonOfSize(MaxJSONInputBytes + 1)
	_, err = JSONInputOf("metadata", &over)
	le, ok := limit.As(err)
	if !ok {
		t.Fatalf("a value over the bound must be a limit refusal, got %v", err)
	}
	if le.Got != MaxJSONInputBytes+1 || le.Max != MaxJSONInputBytes {
		t.Fatalf("refusal carries wrong numbers: %+v", le)
	}
}

// The bound does not change what an invalid or empty value does.
func TestJSONInputOfBoundKeepsTheOtherContracts(t *testing.T) {
	bad := "{nope"
	if _, err := JSONInputOf("metadata", &bad); err == nil {
		t.Fatal("malformed JSON must still be refused")
	}
	if _, ok := limit.As(func() error { _, err := JSONInputOf("metadata", &bad); return err }()); ok {
		t.Fatal("malformed JSON is not a limit refusal")
	}
	blank := "   "
	if v, err := JSONInputOf("metadata", &blank); err != nil || v != nil {
		t.Fatalf("a blank value still clears: %v %v", v, err)
	}
}
