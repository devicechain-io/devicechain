// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/integrity"
)

// A refusal because other records still refer to what is being deleted carries
// REFERENCE_VIOLATION, the code a database foreign-key violation gets, and keeps its own
// sentence and its identity.

func TestErrChannelInUseCarriesReferenceViolation(t *testing.T) {
	err := fmt.Errorf("%w (2 row(s))", ErrChannelInUse)
	if !errors.Is(err, ErrChannelInUse) {
		t.Fatalf("wrapping lost the sentinel's identity")
	}
	class, ok := integrity.Refused(err)
	if !ok || class != integrity.ClassReference {
		t.Fatalf("integrity.Refused(%v) = %v, %v; want a reference refusal", err, class, ok)
	}
	if got := ErrChannelInUse.Extensions()["code"]; got != "REFERENCE_VIOLATION" {
		t.Fatalf("extensions.code = %v, want REFERENCE_VIOLATION", got)
	}
	if got := ErrChannelInUse.Error(); got != "notification channel is still referenced by a policy rule and cannot be deleted" {
		t.Fatalf("the sentence changed: %q", got)
	}
}
