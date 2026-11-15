// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"github.com/devicechain-io/dc-microservice/entityevent"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-dashboard-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type stubAck struct{ acked int }

func (s *stubAck) Ack() error { s.acked++; return nil }

func deleted(t *testing.T, subject string, kind entity.Type, token string, at time.Time, delivered int, ack *stubAck) messaging.Message {
	t.Helper()
	b, err := entityevent.MarshalEntityDeletedEvent(&entityevent.EntityDeletedEvent{EntityType: kind, EntityId: 9,
		EntityToken: token, DeletedTime: at})
	require.NoError(t, err)
	return messaging.NewConsumedMessage(subject, b, delivered, nil, ack)
}

func sharesApi(t *testing.T) (*model.Api, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DashboardShare{}))
	return model.NewApi(&rdb.RdbManager{Database: db}), db
}

func share(t *testing.T, db *gorm.DB, tenant string, dashboard uint, kind, token string, at time.Time) {
	t.Helper()
	require.NoError(t, db.WithContext(core.WithTenant(context.Background(), tenant)).
		Create(&model.DashboardShare{DashboardID: dashboard, AnchorKind: kind, AnchorToken: token, CreatedAt: at}).Error)
}

func remaining(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var rows []model.DashboardShare
	require.NoError(t, db.WithContext(core.WithSystemContext(context.Background())).
		Order("tenant_id, dashboard_id, anchor_token").Find(&rows).Error)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.TenantId+"/"+r.Anchor())
	}
	return out
}

// A customer's deletion removes the shares naming it in ITS tenant and acks — not another
// tenant's customer with the same token, not another customer, not a share made after the
// deletion (a reused token, a new customer), and nothing at all for an entity no share can
// name.
func TestADeletedCustomersSharesAreRemoved(t *testing.T) {
	api, db := sharesApi(t)
	deletedAt := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	before, after := deletedAt.Add(-time.Hour), deletedAt.Add(time.Hour)
	share(t, db, "acme-tenant", 1, "customer", "acme", before)
	share(t, db, "acme-tenant", 2, "customer", "acme", before)
	share(t, db, "acme-tenant", 3, "customer", "acme", after)
	share(t, db, "acme-tenant", 1, "customer", "globex", before)
	share(t, db, "other-tenant", 1, "customer", "acme", before)
	r := &ShareReconciler{Shares: api}

	ack := &stubAck{}
	r.handle(context.Background(), deleted(t, "instance1.acme-tenant.entity-deleted", entity.TypeCustomer, "acme", deletedAt, 1, ack))
	require.Equal(t, 1, ack.acked)
	require.Equal(t, []string{"acme-tenant/customer:globex", "acme-tenant/customer:acme", "other-tenant/customer:acme"}, remaining(t, db))

	// A device sharing a customer's token is not a customer.
	ack = &stubAck{}
	r.handle(context.Background(), deleted(t, "instance1.acme-tenant.entity-deleted", entity.TypeDevice, "globex", after, 1, ack))
	require.Equal(t, 1, ack.acked)
	require.Len(t, remaining(t, db), 3)
}

// A group's deletion removes the shares naming the group, and only them: a customer with the
// same token keeps its share.
func TestADeletedGroupsSharesAreRemoved(t *testing.T) {
	api, db := sharesApi(t)
	deletedAt := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	before := deletedAt.Add(-time.Hour)
	share(t, db, "acme-tenant", 1, "group", "north", before)
	share(t, db, "acme-tenant", 2, "customer", "north", before)
	r := &ShareReconciler{Shares: api}

	ack := &stubAck{}
	r.handle(context.Background(), deleted(t, "instance1.acme-tenant.entity-deleted", entity.TypeGroup, "north", deletedAt, 1, ack))
	require.Equal(t, 1, ack.acked)
	require.Equal(t, []string{"acme-tenant/customer:north"}, remaining(t, db))
}

type failingRemover struct{ calls int }

func (f *failingRemover) DeleteSharesOfEntity(context.Context, string, string, time.Time) (int64, error) {
	f.calls++
	return 0, errors.New("database unavailable")
}

// Poison and a tenantless subject are dropped without a delete; a database failure is left
// unacked for paced redelivery until the delivery ceiling, then dropped.
func TestTheReconcilersDispositions(t *testing.T) {
	f := &failingRemover{}
	r := &ShareReconciler{Shares: f}

	ack := &stubAck{}
	r.handle(context.Background(), messaging.NewConsumedMessage("instance1.acme-tenant.entity-deleted", []byte("not-proto"), 1, nil, ack))
	require.Equal(t, 1, ack.acked)

	ack = &stubAck{}
	r.handle(context.Background(), deleted(t, "no-tenant", entity.TypeCustomer, "acme", time.Now(), 1, ack))
	require.Equal(t, 1, ack.acked)
	require.Zero(t, f.calls, "a poison or tenantless event reached the database")

	ack = &stubAck{}
	r.handle(context.Background(), deleted(t, "instance1.acme-tenant.entity-deleted", entity.TypeCustomer, "acme", time.Now(), 1, ack))
	require.Equal(t, 1, f.calls)
	require.Zero(t, ack.acked, "a failed cleanup below the ceiling must be left for redelivery")

	ack = &stubAck{}
	r.handle(context.Background(), deleted(t, "instance1.acme-tenant.entity-deleted", entity.TypeCustomer, "acme", time.Now(), messaging.MaxDeliver, ack))
	require.Equal(t, 1, ack.acked)
}

type dispositionRemover struct{ err error }

func (f dispositionRemover) DeleteSharesOfEntity(context.Context, string, string, time.Time) (int64, error) {
	return 0, f.err
}

func TestDeliveryCapDispositionEffects(t *testing.T) {
	for _, tc := range []struct {
		name            string
		err             error
		delivered, acks int
		exhausted       bool
	}{
		{"exhausted", errors.New("db down"), messaging.MaxDeliver, 1, true},
		{"erased final", fmt.Errorf("write: %w", rdb.ErrTenantPurged), messaging.MaxDeliver, 1, false},
		{"erased early", rdb.ErrTenantPurged, 1, 1, false},
		{"hold released", fmt.Errorf("write: %w", messaging.ErrHoldReleased), messaging.MaxDeliver, 0, false},
		{"retry", errors.New("db down"), messaging.MaxDeliver - 1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := logSink.Capture(t)
			r := &ShareReconciler{Shares: dispositionRemover{tc.err}}
			ack := &stubAck{}
			r.handle(context.Background(), deleted(t, "instance1.acme.entity-deleted", entity.TypeCustomer, "acme", time.Now(), tc.delivered, ack))
			require.Equal(t, tc.acks, ack.acked)
			if got := strings.Contains(logs.String(), "Giving up on entity-deleted event after max delivery attempts"); got != tc.exhausted {
				t.Errorf("exhausted log = %v; logs: %s", got, logs.String())
			}
			if tc.err == rdb.ErrTenantPurged || strings.HasPrefix(tc.name, "erased") {
				if !strings.Contains(logs.String(), `"level":"debug"`) || !strings.Contains(logs.String(), "Dropping message for an erased tenant") {
					t.Errorf("missing erased debug log: %s", logs.String())
				}
			}
		})
	}
}

var logSink *dctest.LogSink

func TestMain(m *testing.M) {
	logSink = dctest.InstallLogSink()
	os.Exit(m.Run())
}
