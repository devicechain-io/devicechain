// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/integrity"
)

// A refusal because other records still refer to what is being deleted carries
// REFERENCE_VIOLATION, the code a database foreign-key violation gets, and keeps its own
// sentence and its identity.

func TestErrTierInUseCarriesReferenceViolation(t *testing.T) {
	err := fmt.Errorf("%w (2 row(s))", ErrTierInUse)
	if !errors.Is(err, ErrTierInUse) {
		t.Fatalf("wrapping lost the sentinel's identity")
	}
	class, ok := integrity.Refused(err)
	if !ok || class != integrity.ClassReference {
		t.Fatalf("integrity.Refused(%v) = %v, %v; want a reference refusal", err, class, ok)
	}
	if got := ErrTierInUse.Extensions()["code"]; got != "REFERENCE_VIOLATION" {
		t.Fatalf("extensions.code = %v, want REFERENCE_VIOLATION", got)
	}
	if got := ErrTierInUse.Error(); got != "tenant tier still has tenants; move them to another tier first" {
		t.Fatalf("the sentence changed: %q", got)
	}
}

func TestErrTenantHasMembershipsCarriesReferenceViolation(t *testing.T) {
	err := fmt.Errorf("%w (2 row(s))", ErrTenantHasMemberships)
	if !errors.Is(err, ErrTenantHasMemberships) {
		t.Fatalf("wrapping lost the sentinel's identity")
	}
	class, ok := integrity.Refused(err)
	if !ok || class != integrity.ClassReference {
		t.Fatalf("integrity.Refused(%v) = %v, %v; want a reference refusal", err, class, ok)
	}
	if got := ErrTenantHasMemberships.Extensions()["code"]; got != "REFERENCE_VIOLATION" {
		t.Fatalf("extensions.code = %v, want REFERENCE_VIOLATION", got)
	}
	if got := ErrTenantHasMemberships.Error(); got != "tenant still has memberships; remove them first" {
		t.Fatalf("the sentence changed: %q", got)
	}
}
