// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

// The activation log on the real migration chain: one entry per publish and per rollback, in the
// order they committed, stamped with the very instant the profile stores as active_since after
// Postgres has rounded it, and discarded with a transaction that does not commit.
func TestProfileActivationsLogEveryActivationOnPostgres(t *testing.T) {
	api, ctx := newPostgresReconcileApi(t)
	seedPostgresProfileRule(t, api, ctx, unsortedSpacedRule)

	for i := 0; i < 2; i++ {
		if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "it"); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	if _, err := api.RollbackDeviceProfile(ctx, "prof", 1); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	rows, next, err := api.DeviceProfileActivations(ctx, "prof", 0, 10)
	if err != nil || next != 0 {
		t.Fatalf("read: %v (next %d)", err, next)
	}
	if len(rows) != 3 || rows[0].Kind != ActivationKindRollback || rows[0].Version != 1 ||
		rows[1].Version != 2 || rows[2].Version != 1 {
		t.Fatalf("want rollback->1, publish->2, publish->1 newest first, got %+v", rows)
	}
	p, err := api.deviceProfileByToken(ctx, "prof")
	if err != nil {
		t.Fatal(err)
	}
	if !p.ActiveSince.Time.Equal(rows[0].ActivatedAt) {
		t.Fatalf("active_since %s != newest activation %s", p.ActiveSince.Time, rows[0].ActivatedAt)
	}

	boom := errors.New("boom")
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := recordActivation(tx, p.ID, 2, ActivationKindRollback, time.Now(), "it"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("want the rollback error, got %v", err)
	}
	if again, _, _ := api.DeviceProfileActivations(ctx, "prof", 0, 10); len(again) != 3 {
		t.Fatalf("a rolled-back transaction left an entry: %d rows", len(again))
	}
}
