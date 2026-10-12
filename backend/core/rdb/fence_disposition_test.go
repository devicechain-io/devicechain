// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// The fence's two answers cross into messaging.Dispose as two DIFFERENT dispositions, driven
// through real writes rather than hand-built errors: a standing fence's refusal settles the
// message on its first delivery, and an unreadable fence's error is retried like any other
// failure the database could not answer. messaging recognises the refusal without importing
// this package, so this is the only place both halves meet.
func TestTheFencesAnswersAreDisposedOfDifferently(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	first := messaging.Message{Subject: "instance1.acme.resolved-events", NumDelivered: 1}

	t.Run("a standing fence: settled on the first delivery", func(t *testing.T) {
		db := newFenceDB(t)
		plant(t, db, "acme")
		err := db.WithContext(ctx).Create(&widget{Name: "refused"}).Error
		if err == nil {
			t.Fatal("a write was admitted behind a standing fence")
		}
		if got := messaging.Dispose(first, err); got != messaging.TenantErased {
			t.Errorf("Dispose(the fence's refusal %q) = %v; want %v", err, got, messaging.TenantErased)
		}
	})

	t.Run("an unreadable fence: retried", func(t *testing.T) {
		db := newFenceDB(t)
		if err := db.Migrator().DropTable(&PurgedTenant{}); err != nil {
			t.Fatalf("dropping the fence table: %v", err)
		}
		err := db.WithContext(ctx).Create(&widget{Name: "unchecked"}).Error
		if err == nil {
			t.Fatal("a write was admitted with no fence table to check it against")
		}
		if got := messaging.Dispose(first, err); got != messaging.Retry {
			t.Errorf("Dispose(an unreadable fence's error %q) = %v; want %v", err, got, messaging.Retry)
		}
	})
}
