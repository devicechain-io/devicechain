// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"gorm.io/gorm"
)

// recordingDesired is a DeviceDesiredPublisher that remembers what it was asked to deliver.
type recordingDesired struct {
	mu   sync.Mutex
	sent []sentRevision
}

type sentRevision struct {
	token    string
	revision int64
	digest   string
	document string
}

func (r *recordingDesired) PublishDesiredConfiguration(_ context.Context, token string, rev *DeviceConfigurationRevision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, sentRevision{token, rev.Revision, rev.Digest, rev.Document})
}

func (r *recordingDesired) snapshot() []sentRevision {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sentRevision(nil), r.sent...)
}

func reportedState(t *testing.T, api *Api, ctx context.Context) *DeviceConfigurationState {
	t.Helper()
	var rows []DeviceConfigurationState
	if err := api.RDB.DB(ctx).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > 1 {
		t.Fatalf("%d state rows for one device, want at most 1", len(rows))
	}
	return &rows[0]
}

func refusalReason(err error) string {
	var r *ConfigurationReportRefusal
	if errors.As(err, &r) {
		return r.Reason
	}
	return ""
}

func mintOne(t *testing.T, api *Api, ctx context.Context) *DeviceConfigurationRevision {
	t.Helper()
	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "eco")
	return readConfig(t, api, ctx).Desired
}

// A write that mints a revision delivers it, once it has committed; one that mints nothing
// delivers nothing.
func TestMintedRevisionIsDeliveredAfterCommit(t *testing.T) {
	api, ctx := configTestApi(t)
	pub := &recordingDesired{}
	api.DeviceDesiredPublisher = pub
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})

	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "eco")
	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "eco")   // same digest
	setConfigAttr(t, api, ctx, AttributeScopeShared, "facet", AttributeValueString, "x")    // undeclared
	setConfigAttr(t, api, ctx, AttributeScopeClient, "mode", AttributeValueString, "other") // not SHARED
	got := pub.snapshot()
	if len(got) != 1 || got[0].token != "dev" || got[0].revision != 1 || got[0].document != `{"mode":"eco"}` {
		t.Fatalf("delivered = %+v, want exactly revision 1 {\"mode\":\"eco\"} to dev", got)
	}

	if _, err := api.DeleteEntityAttribute(ctx, string(entity.TypeDevice), "dev", string(AttributeScopeShared), "mode"); err != nil {
		t.Fatal(err)
	}
	got = pub.snapshot()
	if len(got) != 2 || got[1].revision != 2 || got[1].document != `{}` {
		t.Fatalf("after deleting the declared key delivered = %+v, want revision 2 {}", got)
	}
}

// A write the document refuses rolls back, mints nothing and so delivers nothing.
func TestRefusedWriteDeliversNothing(t *testing.T) {
	api, ctx := configTestApi(t)
	pub := &recordingDesired{}
	api.DeviceDesiredPublisher = pub
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	big := strings.Repeat("x", MaxConfigurationDocumentBytes)
	if _, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: string(entity.TypeDevice), Entity: "dev", Scope: string(AttributeScopeShared),
		AttrKey: "mode", ValueType: string(AttributeValueString), Value: &big,
	}); err == nil {
		t.Fatal("an over-size configuration write was accepted")
	}
	if got := pub.snapshot(); len(got) != 0 {
		t.Fatalf("delivered %+v for a refused write", got)
	}
}

// A sync mints a revision for a document that moved without a write (a profile publish does
// not fan out), records that the device was seen, and delivers the current revision. A sync
// with nothing to deliver delivers nothing.
func TestSyncMintsWhatAProfilePublishLeftStaleAndDelivers(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	pub := &recordingDesired{}
	api.DeviceDesiredPublisher = pub
	clock := fakeSyncClock(t)

	// Nothing declared-and-set yet: a sync tells the device nothing, but is recorded.
	rev, err := api.SyncDeviceConfiguration(ctx, "dev", 0)
	if err != nil || rev != nil {
		t.Fatalf("sync with nothing to deliver = (%v, %v), want (nil, nil)", rev, err)
	}
	if len(pub.snapshot()) != 0 {
		t.Fatalf("delivered %+v for a device with no configuration", pub.snapshot())
	}
	if st := reportedState(t, api, ctx); st == nil || !st.LastSyncAt.Valid || st.ReportedStatus.Valid {
		t.Fatalf("state after a first sync = %+v, want a row with only last_sync_at", st)
	}

	// The device holds a SHARED value for a key the profile does not yet declare; publishing a
	// declaration that names it moves the document with no write, and no revision.
	setConfigAttr(t, api, ctx, AttributeScopeShared, "level", AttributeValueString, "3")
	if n := countRevisions(t, api, ctx); n != 0 {
		t.Fatalf("revisions before the declaration moved = %d, want 0", n)
	}
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof", []ConfigurationKey{
		{Key: "mode", ValueType: "STRING"}, {Key: "level", ValueType: "STRING"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatal(err)
	}
	if !readConfig(t, api, ctx).Stale {
		t.Fatal("the device does not read stale after its declaration moved")
	}

	clock.advance(SyncCooldown + time.Second)
	rev, err = api.SyncDeviceConfiguration(ctx, "dev", 0)
	if err != nil || rev == nil || rev.Revision != 1 || rev.Document != `{"level":"3"}` {
		t.Fatalf("sync = (%+v, %v), want revision 1 {\"level\":\"3\"}", rev, err)
	}
	if rev.Actor != SystemActorDeviceSync {
		t.Fatalf("actor = %q, want %q", rev.Actor, SystemActorDeviceSync)
	}
	// Syncing again (outside the cooldown) mints nothing new and delivers the same revision again.
	clock.advance(SyncCooldown + time.Second)
	again, err := api.SyncDeviceConfiguration(ctx, "dev", 1)
	if err != nil || again.Revision != 1 {
		t.Fatalf("second sync = (%+v, %v), want revision 1", again, err)
	}
	if n := countRevisions(t, api, ctx); n != 1 {
		t.Fatalf("revisions after two syncs = %d, want 1", n)
	}
	if got := pub.snapshot(); len(got) != 2 || got[0].revision != 1 || got[1].revision != 1 {
		t.Fatalf("delivered = %+v, want revision 1 twice", got)
	}
	if st := reportedState(t, api, ctx); st == nil || st.DeviceId != dev.ID || !st.LastSyncAt.Valid {
		t.Fatalf("state = %+v", st)
	}
}

// A report is recorded only for a revision the device has, with that revision's digest; anything
// else is refused and no state is written.
func TestReportNamingARevisionTheDeviceDoesNotHaveIsRefused(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	rev := mintOne(t, api, ctx)

	cases := []struct {
		name     string
		device   string
		revision int64
		digest   string
		reason   string
	}{
		{"revision never minted", "dev", rev.Revision + 1, rev.Digest, ReportRejectedUnknownRevision},
		{"revision zero", "dev", 0, rev.Digest, ReportRejectedUnknownRevision},
		{"forged digest", "dev", rev.Revision, "sha256:" + strings.Repeat("f", 64), ReportRejectedDigestMismatch},
		{"unknown device", "ghost", rev.Revision, rev.Digest, ReportRejectedUnknownDevice},
	}
	for _, c := range cases {
		err := api.ApplyDeviceConfigurationReport(ctx, c.device, c.revision, c.digest, ConfigurationApplied, nil)
		if got := refusalReason(err); got != c.reason {
			t.Errorf("%s: err = %v, want a refusal with reason %q", c.name, err, c.reason)
		}
		if st := reportedState(t, api, ctx); st != nil {
			t.Errorf("%s: a refused report left a state row %+v", c.name, st)
		}
	}
	if !readConfig(t, api, ctx).Pending {
		t.Fatal("device reads converged after only refused reports")
	}
}

func TestAppliedReportConvergesAndIsIdempotent(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	rev := mintOne(t, api, ctx)

	for i := 0; i < 3; i++ { // at-least-once: the same report may arrive again
		if err := api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil); err != nil {
			t.Fatalf("report %d: %v", i, err)
		}
	}
	c := readConfig(t, api, ctx)
	if c.Pending || c.Reported == nil || c.Reported.ReportedStatus.String != "APPLIED" ||
		c.Reported.ReportedRevision.Int64 != rev.Revision {
		t.Fatalf("after APPLIED: %+v", c)
	}
	first := reportedState(t, api, ctx).ReportedAt
	if err := api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil); err != nil {
		t.Fatal(err)
	}
	if !reportedState(t, api, ctx).ReportedAt.Time.Equal(first.Time) {
		t.Fatal("an identical repeat rewrote the row")
	}
}

// Reports reorder. An older revision's report, or a RECEIVED after the same revision's outcome,
// must not undo what is recorded; a newer revision supersedes it.
func TestReportsAreMonotonic(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	r1 := mintOne(t, api, ctx)
	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "turbo")
	r2 := readConfig(t, api, ctx).Desired

	if err := api.ApplyDeviceConfigurationReport(ctx, "dev", r2.Revision, r2.Digest, ConfigurationApplied, nil); err != nil {
		t.Fatal(err)
	}
	if got := refusalReason(api.ApplyDeviceConfigurationReport(ctx, "dev", r1.Revision, r1.Digest, ConfigurationApplied, nil)); got != ReportRejectedStale {
		t.Fatalf("older revision's report: reason %q, want %q", got, ReportRejectedStale)
	}
	if got := refusalReason(api.ApplyDeviceConfigurationReport(ctx, "dev", r2.Revision, r2.Digest, ConfigurationReceived, nil)); got != ReportRejectedStale {
		t.Fatalf("RECEIVED after APPLIED: reason %q, want %q", got, ReportRejectedStale)
	}
	st := reportedState(t, api, ctx)
	if st.ReportedRevision.Int64 != r2.Revision || st.ReportedStatus.String != "APPLIED" {
		t.Fatalf("state = %+v, want revision %d APPLIED", st, r2.Revision)
	}
	if c := readConfig(t, api, ctx); c.Pending {
		t.Fatal("pending after a stale report was refused")
	}
}

func TestRejectedReportStoresItsErrors(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	rev := mintOne(t, api, ctx)
	if err := api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationReceived, nil); err != nil {
		t.Fatal(err)
	}
	if err := api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, ConfigurationRejected,
		[]string{"mode: unsupported"}); err != nil {
		t.Fatal(err)
	}
	c := readConfig(t, api, ctx)
	if !c.Pending {
		t.Fatal("a REJECTED revision reads as converged")
	}
	if got := c.Reported.ReportedErrorList(); len(got) != 1 || got[0] != "mode: unsupported" {
		t.Fatalf("errors = %v", got)
	}
	if err := api.ApplyDeviceConfigurationReport(ctx, "dev", rev.Revision, rev.Digest, "DONE", nil); err == nil {
		t.Fatal("an unknown status was recorded")
	}
}

// A device is its tenant's. A report for tenant A's device made in tenant B's context finds no
// device, and neither tenant's rows move.
func TestReportsAndSyncsAreTenantScoped(t *testing.T) {
	api, acme := configTestApi(t)
	seedConfiguredDevice(t, api, acme, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	rev := mintOne(t, api, acme)
	other := core.WithTenant(context.Background(), "globex")

	if got := refusalReason(api.ApplyDeviceConfigurationReport(other, "dev", rev.Revision, rev.Digest, ConfigurationApplied, nil)); got != ReportRejectedUnknownDevice {
		t.Fatalf("cross-tenant report: reason %q, want %q", got, ReportRejectedUnknownDevice)
	}
	if _, err := api.SyncDeviceConfiguration(other, "dev", 0); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-tenant sync err = %v, want not found", err)
	}
	if st := reportedState(t, api, acme); st != nil {
		t.Fatalf("a cross-tenant call wrote %+v for the other tenant's device", st)
	}
	if !readConfig(t, api, acme).Pending {
		t.Fatal("tenant A's device converged on tenant B's report")
	}
}

type testClock struct{ now time.Time }

func (c *testClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func fakeSyncClock(t *testing.T) *testClock {
	t.Helper()
	c := &testClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	prev := syncClock
	syncClock = func() time.Time { return c.now }
	t.Cleanup(func() { syncClock = prev })
	return c
}

// A device that syncs again inside the cooldown is acknowledged and nothing happens: no
// transaction's worth of work, no publish, no change to the recorded sync time. The first sync
// after the window is served in full, including the re-delivery.
func TestSyncInsideTheCooldownDoesNothing(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "eco")
	pub := &recordingDesired{}
	api.DeviceDesiredPublisher = pub
	clock := fakeSyncClock(t)

	if rev, err := api.SyncDeviceConfiguration(ctx, "dev", 0); err != nil || rev == nil {
		t.Fatalf("first sync = (%v, %v)", rev, err)
	}
	at := reportedState(t, api, ctx).LastSyncAt.Time

	clock.advance(SyncCooldown - time.Second)
	if rev, err := api.SyncDeviceConfiguration(ctx, "dev", 0); !errors.Is(err, ErrSyncThrottled) || rev != nil {
		t.Fatalf("sync inside the window = (%v, %v), want ErrSyncThrottled", rev, err)
	}
	if got := len(pub.snapshot()); got != 1 {
		t.Fatalf("delivered %d times, want 1: a throttled sync published", got)
	}
	if !reportedState(t, api, ctx).LastSyncAt.Time.Equal(at) {
		t.Fatal("a throttled sync moved the recorded sync time, so a steady stream of them would never be served")
	}

	clock.advance(2 * time.Second)
	if rev, err := api.SyncDeviceConfiguration(ctx, "dev", 0); err != nil || rev == nil {
		t.Fatalf("sync after the window = (%v, %v)", rev, err)
	}
	if got := len(pub.snapshot()); got != 2 {
		t.Fatalf("delivered %d times, want 2: the first sync after the window must re-deliver", got)
	}
}

// visibilityPublisher checks, at the moment it is asked to deliver, that the revision it is
// delivering is committed: it reads through a SEPARATE connection, which under SQLite's shared
// cache cannot see (and is locked out of) a table another connection is still writing in an open
// transaction. A publish made from inside the transaction fails one of the two ways.
type visibilityPublisher struct {
	t     *testing.T
	other *gorm.DB
	calls int
}

func (p *visibilityPublisher) PublishDesiredConfiguration(_ context.Context, _ string, rev *DeviceConfigurationRevision) {
	p.calls++
	var n int64
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		err = p.other.Raw("SELECT count(*) FROM device_configuration_revisions WHERE revision = ? AND digest = ?",
			rev.Revision, rev.Digest).Scan(&n).Error
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		// Blocked behind the writer's open transaction: the revision is not committed.
		p.t.Errorf("revision %d was published before it was committed (separate connection blocked)", rev.Revision)
		return
	}
	if err != nil || n != 1 {
		p.t.Errorf("revision %d was published before it was committed (separate connection: count=%d err=%v)", rev.Revision, n, err)
	}
}

func TestRevisionsArePublishedOnlyOnceCommitted(t *testing.T) {
	// A second handle on the SAME database, to watch from outside the writer's transaction.
	dsn := rdbtest.SQLiteMemoryDSN(t)
	api, ctx := configTestApiAt(t, dsn)
	other := rdbtest.OpenSQLiteDSN(t, dsn, nil)
	pub := &visibilityPublisher{t: t, other: other}
	api.DeviceDesiredPublisher = pub
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}, {Key: "level", ValueType: "STRING"}})
	fakeSyncClock(t)

	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "eco")                                             // write path
	if _, err := api.DeleteEntityAttribute(ctx, string(entity.TypeDevice), "dev", string(AttributeScopeShared), "mode"); err != nil { // delete path
		t.Fatal(err)
	}
	// sync path: a value written under an undeclared name, then a declaration that names it.
	setConfigAttr(t, api, ctx, AttributeScopeShared, "extra", AttributeValueString, "x")
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof", []ConfigurationKey{
		{Key: "mode", ValueType: "STRING"}, {Key: "extra", ValueType: "STRING"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.SyncDeviceConfiguration(ctx, "dev", 0); err != nil {
		t.Fatal(err)
	}
	if pub.calls != 3 {
		t.Fatalf("publisher called %d times, want 3 (write, delete, sync)", pub.calls)
	}
}
