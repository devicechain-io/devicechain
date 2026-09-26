// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
)

// ErrConflict is a STALE-VERSION refusal ("modified by another writer; reload and try
// again"), not a uniqueness conflict. A client may treat the CONFLICT code as "already
// exists, carry on", and on a lost update that would report a save that never happened as
// done, so despite the name it must never carry that code.
func TestErrConflictIsNotAUniquenessConflict(t *testing.T) {
	if conflict.Is(ErrConflict) {
		t.Fatalf("ErrConflict (%v) is classified as a uniqueness conflict", ErrConflict)
	}
	if ext, ok := any(ErrConflict).(interface{ Extensions() map[string]any }); ok && ext.Extensions()["code"] == conflict.Code {
		t.Fatalf("ErrConflict carries extensions.code %q", conflict.Code)
	}
}
