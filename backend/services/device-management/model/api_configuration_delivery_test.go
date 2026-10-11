// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
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

	rev, err = api.SyncDeviceConfiguration(ctx, "dev", 0)
	if err != nil || rev == nil || rev.Revision != 1 || rev.Document != `{"level":"3"}` {
		t.Fatalf("sync = (%+v, %v), want revision 1 {\"level\":\"3\"}", rev, err)
	}
	if rev.Actor != SystemActorDeviceSync {
		t.Fatalf("actor = %q, want %q", rev.Actor, SystemActorDeviceSync)
	}
	// Syncing again mints nothing new and delivers the same revision again.
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
