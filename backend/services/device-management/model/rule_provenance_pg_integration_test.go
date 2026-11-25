// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Two editors read the same history before either save starts. Both doors must
// survive, whichever transaction wins the row lock. SQLite cannot prove this lock.
func TestRuleProvenanceConcurrentSavesPG(t *testing.T) {
	r := newPGEnforcementRig(t)
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(r.rule).Update("authored_via", ruleDoorJSON("FORM")).Error)
	ctx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	defer cancel()
	loaded := make(chan struct{}, 2)
	release := make(chan struct{})
	var reads atomic.Int32
	db := r.api.RDB.Database
	const callback = "test:rule-provenance-concurrent-read"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "detection_rules" || len(tx.Statement.Selects) != 0 || reads.Add(1) > 2 {
			return
		}
		loaded <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			tx.AddError(ctx.Err())
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	errors := make(chan error, 2)
	for _, door := range []AuthoredVia{"DESCRIBE", "CANVAS"} {
		go func(door AuthoredVia) {
			_, err := r.api.UpdateDetectionRule(ctx, "r1", &DetectionRuleUpdateRequest{AuthoredVia: OptionalAuthoredVia{Set: true, Value: &door}})
			errors <- err
		}(door)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-loaded:
		case <-ctx.Done():
			t.Fatal("both editors did not read the draft before the save: ", ctx.Err())
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		require.NoError(t, <-errors)
	}
	rows, err := r.api.DetectionRulesByToken(ctx, []string{"r1"})
	require.NoError(t, err)
	require.JSONEq(t, `["CANVAS","DESCRIBE","FORM"]`, string(rows[0].AuthoredVia))
	version, err := r.api.PublishDeviceProfile(ctx, "profile", nil, nil, "publisher")
	require.NoError(t, err)
	require.JSONEq(t, `["CANVAS","DESCRIBE","FORM"]`, string(version.AuthoredVia))
}
