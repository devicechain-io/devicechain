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
// omitting it then WRITES it, and the mutation returns success.
//
// The layers of this service's suite each miss it for their own reason, and the reasons do
// not overlap: partial_update_wire_test.go checks a document's SHAPE and addresses records
// that do not exist, so storage is never read; the model families drive the Api directly,
// below the packer; core's optional_test.go proves the three states survive for the TYPE,
// which says nothing about a DECLARATION in this schema's text.
//
// 🔴 IT IS THE CONVERSION THAT OPENS THE HOLE. A default against a POINTER field fails
// schema construction outright, so a full-replace input was protected by the library
// itself; an Optional* field absorbs the default cleanly and the schema builds. Wiring
// this is what replaces the compile-time refusal the conversion gave up.
//
// The input set is DERIVED from the schema's update* mutations, so a third update input is
// covered on the day it is added — including the nested ones, which are followed too.
func TestNoUpdateInputCarriesAnSDLDefault(t *testing.T) {
	putest.AssertNoUpdateInputCarriesAnSDLDefault(t,
		putest.UpdateSchema{
			Name: "notification-management", SDL: SchemaContent, Root: &SchemaResolver{},
			// updateNotificationChannel, updateNotificationPolicy.
			MinUpdateMutations: 2,
		},
	)
}
