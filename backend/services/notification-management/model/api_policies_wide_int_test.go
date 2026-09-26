// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"math"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// 🔴 AN UPDATE THAT DOES NOT NAME AN INTERVAL DOES NOT NARROW IT.
//
// The policy's intervals are bigint columns behind a 32-bit GraphQL Int. The update used to
// fold them by converting the STORED value to int32 and back — on the absent branch too —
// so a value wider than 32 bits (written by a raw path, or by any client of the column
// other than this API) was wrapped by an edit that only renamed the policy.
func TestAnUpdateLeavesAWideIntervalItDidNotName(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("A")
	if _, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token: "ops", Enabled: true,
	}); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	wide := int64(math.MaxInt32) + 1
	if err := api.RDB.DB(ctx).Model(&NotificationPolicy{}).Where("token = ?", "ops").
		Update("throttle_seconds", wide).Error; err != nil {
		t.Fatalf("seed a wide interval: %v", err)
	}

	if _, err := api.UpdateNotificationPolicy(ctx, "ops", &NotificationPolicyUpdateRequest{
		Name: dcgraphql.OptionalStringOf("renamed"),
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	found, err := api.NotificationPoliciesByToken(ctx, []string{"ops"})
	if err != nil || len(found) != 1 {
		t.Fatalf("reload: got %d, err=%v", len(found), err)
	}
	if want := (sql.NullInt64{Int64: wide, Valid: true}); found[0].ThrottleSeconds != want {
		t.Fatalf("throttle_seconds = %#v after an update that named only name, want %#v",
			found[0].ThrottleSeconds, want)
	}
	if want := (sql.NullString{String: "renamed", Valid: true}); found[0].Name != want {
		t.Fatalf("name = %#v, want %#v — the update this test drives did not land", found[0].Name, want)
	}
}
