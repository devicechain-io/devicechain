// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"errors"
	"math"
	"testing"

	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-notification-management/model"
)

// 🔴 A STORED INTERVAL WIDER THAN A GraphQL Int IS REFUSED ON READ, NOT WRAPPED.
//
// The policy's intervals are bigint columns and the wire type is a 32-bit Int. The read
// used to narrow with a bare int32 conversion, so a stored 2147483648 read back as
// -2147483648 — a plausible number, a wrong one, and no sign anything happened.
func TestAStoredIntervalWiderThanAnIntIsRefusedOnRead(t *testing.T) {
	resolver := func(v sql.NullInt64) *NotificationPolicyResolver {
		return &NotificationPolicyResolver{M: model.NotificationPolicy{
			ThrottleSeconds: v, EscalateAfterSeconds: v, MaxEscalations: v,
		}}
	}
	reads := map[string]func(*NotificationPolicyResolver) (*int32, error){
		"throttleSeconds":      (*NotificationPolicyResolver).ThrottleSeconds,
		"escalateAfterSeconds": (*NotificationPolicyResolver).EscalateAfterSeconds,
		"maxEscalations":       (*NotificationPolicyResolver).MaxEscalations,
	}
	for field, read := range reads {
		t.Run(field, func(t *testing.T) {
			got, err := read(resolver(sql.NullInt64{Int64: math.MaxInt32 + 1, Valid: true}))
			if !errors.Is(err, util.ErrStoredIntOutOfRange) || got != nil {
				t.Fatalf("a stored 2147483648 read back as (%v, %v), want a refusal", got, err)
			}

			got, err = read(resolver(sql.NullInt64{Int64: math.MaxInt32, Valid: true}))
			if err != nil || got == nil || *got != math.MaxInt32 {
				t.Fatalf("a stored 2147483647 read back as (%v, %v), want 2147483647", got, err)
			}

			got, err = read(resolver(sql.NullInt64{}))
			if err != nil || got != nil {
				t.Fatalf("NULL read back as (%v, %v), want (nil, nil)", got, err)
			}
		})
	}
}
