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

func TestErrProviderInUseCarriesReferenceViolation(t *testing.T) {
	err := fmt.Errorf("%w (2 row(s))", ErrProviderInUse)
	if !errors.Is(err, ErrProviderInUse) {
		t.Fatalf("wrapping lost the sentinel's identity")
	}
	class, ok := integrity.Refused(err)
	if !ok || class != integrity.ClassReference {
		t.Fatalf("integrity.Refused(%v) = %v, %v; want a reference refusal", err, class, ok)
	}
	if got := ErrProviderInUse.Extensions()["code"]; got != "REFERENCE_VIOLATION" {
		t.Fatalf("extensions.code = %v, want REFERENCE_VIOLATION", got)
	}
	if got := ErrProviderInUse.Error(); got != "provider is still granted and cannot be deleted" {
		t.Fatalf("the sentence changed: %q", got)
	}
}
