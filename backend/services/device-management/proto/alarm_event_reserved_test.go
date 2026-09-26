// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Field 13 of PAlarmStateChangeEvent was `message`, a summary nothing ever set. It is
// removed and RESERVED, by number and by name, so neither can be reused for a field of a
// different meaning: an older notification-management decoding a reused 13 would read it
// as the retired string.
func TestAlarmEventReservesRetiredMessageField(t *testing.T) {
	d := (&PAlarmStateChangeEvent{}).ProtoReflect().Descriptor()
	assert.Nil(t, d.Fields().ByNumber(13), "field 13 must not be declared")
	assert.Nil(t, d.Fields().ByName("message"), "no field may be named message")
	assert.True(t, d.ReservedRanges().Has(13), "field number 13 must be reserved")
	assert.True(t, d.ReservedNames().Has("message"), "field name message must be reserved")
}
