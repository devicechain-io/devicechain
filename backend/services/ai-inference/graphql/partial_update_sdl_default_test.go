// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// THE ONE WAY TO DESTROY THE ABSENT STATE THAT NOTHING IN THIS SERVICE'S GO CODE CAN SEE.
//
// A default value is not in Go. It is a token in the SDL — `enabled: Boolean = true` — and
// StructPacker packs a defaulted field as NON-NULL and seeds it into the result before
// packing, so an ABSENT field arrives with Set=true holding the default. Every update
// omitting it then WRITES it, and the mutation returns success. On a provider that means
// an operator renaming a model assignment silently re-enabling a provider whose key was
// pulled.
//
// Nothing else here is positioned to see it: admin_partial_update_wire_test.go checks a
// document's SHAPE rather than reading storage, the model families drive the Api directly
// (below the packer), and core's optional_test.go proves the three states survive for the
// TYPE, which says nothing about a DECLARATION in this schema's text.
//
// 🔴 IT IS THE CONVERSION THAT OPENS THE HOLE. A default against a POINTER field fails
// schema construction outright, so a full-replace input was protected by the library
// itself; an Optional* field absorbs the default cleanly and the schema builds.
//
// The AdminResolver is constructed with nil dependencies deliberately: what is parsed is
// the schema against its resolver METHODS, and a method set does not depend on whether the
// fields behind it are wired.
func TestNoUpdateInputCarriesAnSDLDefault(t *testing.T) {
	putest.AssertNoUpdateInputCarriesAnSDLDefault(t,
		putest.UpdateSchema{
			Name: "admin", SDL: AdminSchemaContent, Root: &AdminResolver{},
			// updateAiProvider.
			MinUpdateMutations: 1,
		},
		// 🔴 THE TENANT DATA-PLANE SCHEMA (schema.graphql) IS NAMED RATHER THAN LISTED,
		// following user-management's settings_schema.gql. It serves ONE mutation,
		// inferRuleCandidate, and no update* at all — so a row for it would fail the
		// anti-vacuity floor, which must be greater than zero. Its single mutation is a
		// stateless inference call that persists nothing, so there is no stored value an
		// absent field could destroy. Add a row the day it grows an update.
	)
}
