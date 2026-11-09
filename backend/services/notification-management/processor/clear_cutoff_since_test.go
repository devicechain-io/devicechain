// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-notification-management/model"
)

// Turning the option off and on again restarts the cut-off; leaving it on does not move it.
func TestTheClearCutoffMovesOnlyWhenTheOptionTurnsOn(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedClearPolicy(t, api, ctx, false)
	get := func() *model.NotificationPolicy {
		ps, err := api.NotificationPoliciesByToken(ctx, []string{"p"})
		if err != nil || len(ps) != 1 {
			t.Fatalf("load: %v", err)
		}
		return ps[0]
	}
	set := func(v bool) {
		if _, err := api.UpdateNotificationPolicy(ctx, "p",
			&model.NotificationPolicyUpdateRequest{NotifyOnClear: boolOpt(v)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if get().NotifyOnClearSince.Valid {
		t.Fatal("an off policy carries a cut-off")
	}
	set(true)
	first := get().NotifyOnClearSince
	if !first.Valid {
		t.Fatal("turning the option on stamped nothing")
	}
	time.Sleep(20 * time.Millisecond)
	set(true)
	if got := get().NotifyOnClearSince; !got.Time.Equal(first.Time) {
		t.Fatalf("re-saving an on policy moved its cut-off: %v -> %v", first.Time, got.Time)
	}
	set(false)
	if get().NotifyOnClearSince.Valid {
		t.Fatal("turning the option off left a cut-off")
	}
}
