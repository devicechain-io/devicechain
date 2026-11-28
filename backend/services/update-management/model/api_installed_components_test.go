// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/require"
)

func inventoryOf(component, version, digest string) ota.Report {
	return ota.Report{V: 1, Kind: ota.KindInventory, Component: component, BootID: "boot-1", Running: &ota.Running{Version: version, Digest: digest}}
}

func installed(t *testing.T, e *updateEnv, tenant, device string) map[string]DeviceInstalledComponent {
	t.Helper()
	rows, err := e.api.InstalledComponents(tenantCtx(tenant), device)
	require.NoError(t, err)
	out := map[string]DeviceInstalledComponent{}
	for _, r := range rows {
		out[r.Component] = r
	}
	return out
}

func TestAnInventoryWithNoAssignmentIsStillRecorded(t *testing.T) {
	e := newUpdateEnv(t)
	// Nothing was ever assigned to dev-1: the report reconciles nothing and says so, and is still remembered.
	e.mustApply(t, "acme", "dev-1", inventoryOf("firmware", "1.4.2", "sha256:aa"), VerdictUnknownAssignment)
	got := installed(t, e, "acme", "dev-1")
	require.Len(t, got, 1)
	require.Equal(t, "1.4.2", got["firmware"].Version)
	require.Equal(t, "sha256:aa", got["firmware"].Digest)
	require.Equal(t, InstalledSourceInventory, got["firmware"].Source)
	require.EqualValues(t, 1, got["firmware"].DeviceIncarnation)
	require.True(t, got["firmware"].ObservedAt.Equal(e.clock))
}

func TestAnInventoryWhileUnknownRecordsAndReconciles(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-2", "2.0.0", []byte("firmware two"))
	v, err := e.api.AssignArtifact(tenantCtx("acme"), "dev-2", "fw-2")
	require.NoError(t, err)
	e.mustApply(t, "acme", "dev-2", progress(v, 1, ota.StageInstalling, "b"), ota.VerdictApplied)
	e.advance(40 * time.Minute)
	_, err = e.api.SweepDeadlines(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, ota.StateUnknown, e.state(t, "acme", v))
	require.Empty(t, installed(t, e, "acme", "dev-2"), "an attempt alone says nothing about what runs")

	e.mustApply(t, "acme", "dev-2", inventoryOf("firmware", "2.0.0", ""), ota.VerdictApplied)
	require.Equal(t, ota.StateUpdated, e.state(t, "acme", v))
	got := installed(t, e, "acme", "dev-2")
	require.Equal(t, "2.0.0", got["firmware"].Version)
	require.Equal(t, InstalledSourceInventory, got["firmware"].Source)
}

func TestReachingUpdatedRecordsTheArtifactsVersion(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-3", "3.1.0", []byte("firmware three"))
	v, err := e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-3")
	require.NoError(t, err)
	e.mustApply(t, "acme", "dev-1", progress(v, 1, ota.StageDownloaded, "b"), ota.VerdictApplied)
	require.Empty(t, installed(t, e, "acme", "dev-1"), "a progress report that is not UPDATED writes nothing")
	e.mustApply(t, "acme", "dev-1", progress(v, 2, ota.StageInstalling, "b"), ota.VerdictApplied)
	require.Empty(t, installed(t, e, "acme", "dev-1"))
	e.mustApply(t, "acme", "dev-1", running(v, 3, "b2"), ota.VerdictApplied)
	require.Equal(t, ota.StateUpdated, e.state(t, "acme", v))
	got := installed(t, e, "acme", "dev-1")
	require.Equal(t, "3.1.0", got["firmware"].Version)
	require.Equal(t, InstalledSourceUpdated, got["firmware"].Source)
	require.Equal(t, v.Attempt.TargetDigest, got["firmware"].Digest)
}

func TestAReorderedOlderReportDoesNotRegressTheVersion(t *testing.T) {
	e := newUpdateEnv(t)
	ctx := tenantCtx("acme")
	later := e.clock.Add(time.Hour)
	// The newer observation arrives first.
	require.NoError(t, e.api.recordInstalled(ctx, "dev-1", 1, "firmware", "2.0.0", "", later, InstalledSourceInventory))
	require.NoError(t, e.api.recordInstalled(ctx, "dev-1", 1, "firmware", "1.0.0", "", e.clock, InstalledSourceInventory))
	require.Equal(t, "2.0.0", installed(t, e, "acme", "dev-1")["firmware"].Version)
	// An equal timestamp keeps what is there; a newer one replaces it.
	require.NoError(t, e.api.recordInstalled(ctx, "dev-1", 1, "firmware", "9.9.9", "", later, InstalledSourceInventory))
	require.Equal(t, "2.0.0", installed(t, e, "acme", "dev-1")["firmware"].Version)
	require.NoError(t, e.api.recordInstalled(ctx, "dev-1", 1, "firmware", "2.1.0", "", later.Add(time.Second), InstalledSourceUpdated))
	got := installed(t, e, "acme", "dev-1")["firmware"]
	require.Equal(t, "2.1.0", got.Version)
	require.Equal(t, InstalledSourceUpdated, got.Source)
	// One row per component, however many reports.
	rows, err := e.api.InstalledComponents(ctx, "dev-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestAReplacedIncarnationStartsEmpty(t *testing.T) {
	e := newUpdateEnv(t)
	ctx := tenantCtx("acme")
	e.mustApply(t, "acme", "dev-1", inventoryOf("firmware", "1.0.0", ""), VerdictUnknownAssignment)
	// The same token, new hardware.
	e.devices.incarnations = map[string]uint64{"acme/dev-1": 5}
	e.api.disabled.forget("acme/dev-1")
	e.advance(time.Minute)
	e.mustApply(t, "acme", "dev-1", inventoryOf("modem", "7", ""), VerdictUnknownAssignment)
	rows, err := e.api.InstalledComponents(ctx, "dev-1")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.EqualValues(t, 5, rows[0].DeviceIncarnation, "newest incarnation first")
	require.Equal(t, "modem", rows[0].Component)
	require.EqualValues(t, 1, rows[1].DeviceIncarnation)
	// The new hardware reporting firmware does not overwrite what the old one said.
	e.mustApply(t, "acme", "dev-1", inventoryOf("firmware", "2.0.0", ""), VerdictUnknownAssignment)
	rows, err = e.api.InstalledComponents(ctx, "dev-1")
	require.NoError(t, err)
	require.Len(t, rows, 3)
}

func TestADeviceDeletionFactRemovesWhatItReportedButNotTheReplacements(t *testing.T) {
	e := newUpdateEnv(t)
	ctx := tenantCtx("acme")
	e.mustApply(t, "acme", "dev-1", inventoryOf("firmware", "1.0.0", ""), VerdictUnknownAssignment)
	e.devices.incarnations = map[string]uint64{"acme/dev-1": 9}
	e.api.disabled.forget("acme/dev-1")
	e.advance(time.Minute)
	e.mustApply(t, "acme", "dev-1", inventoryOf("firmware", "3.0.0", ""), VerdictUnknownAssignment)
	// Another device is untouched.
	e.mustApply(t, "acme", "dev-2", inventoryOf("firmware", "1.0.0", ""), VerdictUnknownAssignment)

	require.NoError(t, e.api.DeviceDeleted(ctx, "dev-1", 1, e.clock))
	rows, err := e.api.InstalledComponents(ctx, "dev-1")
	require.NoError(t, err)
	require.Len(t, rows, 1, "incarnation 1 is gone, the replacement stays")
	require.EqualValues(t, 9, rows[0].DeviceIncarnation)
	require.Len(t, installed(t, e, "acme", "dev-2"), 1)

	require.NoError(t, e.api.DeviceDeleted(ctx, "dev-1", 9, e.clock))
	require.Empty(t, installed(t, e, "acme", "dev-1"))
	// A redelivery changes nothing.
	require.NoError(t, e.api.DeviceDeleted(ctx, "dev-1", 9, e.clock))
	require.Len(t, installed(t, e, "acme", "dev-2"), 1)
}

func TestInstalledVersionsAreTenantScoped(t *testing.T) {
	e := newUpdateEnv(t)
	e.mustApply(t, "acme", "dev-1", inventoryOf("firmware", "1.0.0", ""), VerdictUnknownAssignment)
	e.mustApply(t, "globex", "dev-1", inventoryOf("firmware", "9.0.0", ""), VerdictUnknownAssignment)
	require.Equal(t, "1.0.0", installed(t, e, "acme", "dev-1")["firmware"].Version)
	require.Equal(t, "9.0.0", installed(t, e, "globex", "dev-1")["firmware"].Version)
}

func TestAnInventoryWithoutARunningVersionRecordsNothing(t *testing.T) {
	e := newUpdateEnv(t)
	e.mustApply(t, "acme", "dev-1", ota.Report{V: 1, Kind: ota.KindInventory, Component: "firmware", BootID: "b"}, VerdictUnknownAssignment)
	require.Empty(t, installed(t, e, "acme", "dev-1"))
	e.mustApply(t, "acme", "dev-1", ota.Report{V: 1, Kind: ota.KindInventory, Component: "firmware", BootID: "b", Running: &ota.Running{Version: ""}}, VerdictUnknownAssignment)
	require.Empty(t, installed(t, e, "acme", "dev-1"), "an empty version is not an observation")
}

func TestAnObservationNeedsTheDevicesIncarnation(t *testing.T) {
	e := newUpdateEnv(t)
	err := e.api.recordInstalled(tenantCtx("acme"), "dev-1", 0, "firmware", "1.0.0", "", e.clock, InstalledSourceInventory)
	require.Error(t, err, "an incarnation of zero would let a replaced device inherit the old one's versions")
	require.Empty(t, installed(t, e, "acme", "dev-1"))
}

func TestARedeliveredOlderReportKeepsItsBrokerTimeAndCannotRegressTheVersion(t *testing.T) {
	e := newUpdateEnv(t)
	t0 := e.clock
	at := func(d time.Duration) context.Context { return WithObservedAt(tenantCtx("acme"), t0.Add(d)) }
	apply := func(ctx context.Context, version string) {
		v, err := e.api.ApplyReport(ctx, "dev-1", inventoryOf("firmware", version, ""))
		require.NoError(t, err)
		require.Equal(t, VerdictUnknownAssignment, v)
	}
	apply(at(1*time.Minute), "1.0.0")
	apply(at(2*time.Minute), "2.0.0")
	// The first report is delivered AGAIN, long after (the platform's own clock has moved on), but it
	// carries the broker time it was first appended with.
	e.advance(time.Hour)
	apply(at(1*time.Minute), "1.0.0")
	require.Equal(t, "2.0.0", installed(t, e, "acme", "dev-1")["firmware"].Version)
	// Negative control: with no stamp the platform's clock is used, and a fresh report is the newest.
	_, err := e.api.ApplyReport(tenantCtx("acme"), "dev-1", inventoryOf("firmware", "3.0.0", ""))
	require.NoError(t, err)
	require.Equal(t, "3.0.0", installed(t, e, "acme", "dev-1")["firmware"].Version)
}

func TestARetriedUpdatedReportRestoresAnInstalledVersionThatFailedToWrite(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-3", "3.1.0", []byte("firmware three"))
	v, err := e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-3")
	require.NoError(t, err)
	e.mustApply(t, "acme", "dev-1", progress(v, 1, ota.StageInstalling, "b"), ota.VerdictApplied)
	report := running(v, 2, "b2")
	e.advance(time.Minute)
	updatedAt := e.clock
	ctx := WithObservedAt(tenantCtx("acme"), updatedAt.Add(-time.Second))
	verdict, err := e.api.ApplyReport(ctx, "dev-1", report)
	require.NoError(t, err)
	require.Equal(t, ota.VerdictApplied, verdict)
	require.Equal(t, "3.1.0", installed(t, e, "acme", "dev-1")["firmware"].Version)
	// The write of the installed version is what failed after the attempt committed.
	require.NoError(t, e.api.RDB.DB(tenantCtx("acme")).Where("device_token = ?", "dev-1").Delete(&DeviceInstalledComponent{}).Error)
	require.Empty(t, installed(t, e, "acme", "dev-1"))
	// The same message is redelivered later with its original broker time, and still repairs the
	// observation using the persisted time the attempt reached UPDATED.
	e.advance(time.Hour)
	verdict, err = e.api.ApplyReport(ctx, "dev-1", report)
	require.NoError(t, err)
	require.Equal(t, ota.VerdictDuplicate, verdict)
	got := installed(t, e, "acme", "dev-1")["firmware"]
	require.Equal(t, "3.1.0", got.Version)
	require.Equal(t, v.Attempt.TargetDigest, got.Digest)
	require.Equal(t, InstalledSourceUpdated, got.Source)
	require.True(t, got.ObservedAt.Equal(updatedAt.Add(-time.Second)), "repair keeps the completing report's broker time")
}

func TestAResentUpdatedReportDoesNotOverwriteANewerInstalledVersion(t *testing.T) {
	for _, newer := range []string{"assignment", "inventory"} {
		t.Run(newer, func(t *testing.T) {
			e := newUpdateEnv(t)
			ctx := tenantCtx("acme")
			e.availableArtifact(t, "acme", "fw-2", "2.0.0", []byte("firmware two"))
			a, err := e.api.AssignArtifact(ctx, "dev-1", "fw-2")
			require.NoError(t, err)
			e.mustApply(t, "acme", "dev-1", progress(a, 1, ota.StageInstalling, "b"), ota.VerdictApplied)
			report := running(a, 2, "b2")
			e.advance(time.Minute)
			e.mustApply(t, "acme", "dev-1", report, ota.VerdictApplied)
			require.Equal(t, "2.0.0", installed(t, e, "acme", "dev-1")["firmware"].Version)

			e.advance(time.Minute)
			if newer == "assignment" {
				e.availableArtifact(t, "acme", "fw-3", "3.0.0", []byte("firmware three"))
				b, err := e.api.AssignArtifact(ctx, "dev-1", "fw-3")
				require.NoError(t, err)
				e.mustApply(t, "acme", "dev-1", progress(b, 1, ota.StageInstalling, "b2"), ota.VerdictApplied)
				e.mustApply(t, "acme", "dev-1", running(b, 2, "b3"), ota.VerdictApplied)
			} else {
				verdict, err := e.api.ApplyReport(WithObservedAt(ctx, e.clock), "dev-1", inventoryOf("firmware", "3.0.0", "sha256:bb"))
				require.NoError(t, err)
				require.Equal(t, ota.VerdictLate, verdict)
				current, err := e.api.AssignmentByToken(ctx, a.Assignment.Token)
				require.NoError(t, err)
				require.False(t, current.Assignment.SupersededAt.Valid, "exercise ordering while A is still current")
			}
			before := installed(t, e, "acme", "dev-1")["firmware"]
			require.Equal(t, "3.0.0", before.Version)

			// Reconnecting publishes the old final report as a NEW message with a later append time.
			e.advance(time.Minute)
			verdict, err := e.api.ApplyReport(WithObservedAt(ctx, e.clock), "dev-1", report)
			require.NoError(t, err)
			require.Equal(t, ota.VerdictDuplicate, verdict)
			require.Equal(t, before, installed(t, e, "acme", "dev-1")["firmware"], "the newer installed observation must stay unchanged")
		})
	}
}

func TestARepairDoesNotOutrankANewerBrokerObservationUnderClockSkew(t *testing.T) {
	e := newUpdateEnv(t)
	ctx := tenantCtx("acme")
	t0 := e.clock
	// The replica is ahead of the broker, whose timestamps order installed observations.
	e.advance(10 * time.Minute)
	e.availableArtifact(t, "acme", "fw-2", "2.0.0", []byte("firmware two"))
	a, err := e.api.AssignArtifact(ctx, "dev-1", "fw-2")
	require.NoError(t, err)
	e.mustApply(t, "acme", "dev-1", progress(a, 1, ota.StageInstalling, "b"), ota.VerdictApplied)
	report := running(a, 2, "b2")
	completed := WithObservedAt(ctx, t0)
	verdict, err := e.api.ApplyReport(completed, "dev-1", report)
	require.NoError(t, err)
	require.Equal(t, ota.VerdictApplied, verdict)
	got := installed(t, e, "acme", "dev-1")["firmware"]
	require.Equal(t, "2.0.0", got.Version)
	require.True(t, got.ObservedAt.Equal(t0))

	// A later broker observation supersedes the completed update despite replica skew.
	verdict, err = e.api.ApplyReport(WithObservedAt(ctx, t0.Add(2*time.Minute)), "dev-1", inventoryOf("firmware", "1.0.0", ""))
	require.NoError(t, err)
	require.Equal(t, ota.VerdictLate, verdict)
	before := installed(t, e, "acme", "dev-1")["firmware"]
	require.Equal(t, "1.0.0", before.Version)
	require.True(t, before.ObservedAt.Equal(t0.Add(2*time.Minute)))

	// Redelivery keeps the completing report's original broker time.
	verdict, err = e.api.ApplyReport(completed, "dev-1", report)
	require.NoError(t, err)
	require.Equal(t, ota.VerdictDuplicate, verdict)
	got = installed(t, e, "acme", "dev-1")["firmware"]
	require.Equal(t, "1.0.0", got.Version, "duplicate repair must not outrank a newer broker observation")
	require.Equal(t, before, got)

	// So does a device resend of the same final report, a new message with a later append time.
	verdict, err = e.api.ApplyReport(WithObservedAt(ctx, t0.Add(3*time.Minute)), "dev-1", report)
	require.NoError(t, err)
	require.Equal(t, ota.VerdictDuplicate, verdict)
	require.Equal(t, before, installed(t, e, "acme", "dev-1")["firmware"], "a resend must not outrank a newer broker observation")
}

func TestAResentUpdatedReportForASupersededAssignmentDoesNotOverwriteANewerVersionUnderClockSkew(t *testing.T) {
	e := newUpdateEnv(t)
	ctx := tenantCtx("acme")
	t0 := e.clock
	// The replica runs ahead of the broker throughout.
	e.advance(10 * time.Minute)
	e.availableArtifact(t, "acme", "fw-2", "2.0.0", []byte("firmware two"))
	a, err := e.api.AssignArtifact(ctx, "dev-1", "fw-2")
	require.NoError(t, err)
	e.mustApply(t, "acme", "dev-1", progress(a, 1, ota.StageInstalling, "b"), ota.VerdictApplied)
	old := running(a, 2, "b2")
	oldCtx := WithObservedAt(ctx, t0)
	verdict, err := e.api.ApplyReport(oldCtx, "dev-1", old)
	require.NoError(t, err)
	require.Equal(t, ota.VerdictApplied, verdict)

	e.availableArtifact(t, "acme", "fw-3", "3.0.0", []byte("firmware three"))
	b, err := e.api.AssignArtifact(ctx, "dev-1", "fw-3")
	require.NoError(t, err)
	verdict, err = e.api.ApplyReport(WithObservedAt(ctx, t0.Add(time.Minute)), "dev-1", progress(b, 1, ota.StageInstalling, "b2"))
	require.NoError(t, err)
	require.Equal(t, ota.VerdictApplied, verdict)
	verdict, err = e.api.ApplyReport(WithObservedAt(ctx, t0.Add(2*time.Minute)), "dev-1", running(b, 2, "b3"))
	require.NoError(t, err)
	require.Equal(t, ota.VerdictApplied, verdict)
	before := installed(t, e, "acme", "dev-1")["firmware"]
	require.Equal(t, "3.0.0", before.Version)
	require.True(t, before.ObservedAt.Equal(t0.Add(2*time.Minute)))

	// The superseded assignment's final report arrives again: redelivered with its own broker time,
	// and resent by the device as a new message appended after the newer completion.
	for _, at := range []context.Context{oldCtx, WithObservedAt(ctx, t0.Add(3*time.Minute))} {
		verdict, err = e.api.ApplyReport(at, "dev-1", old)
		require.NoError(t, err)
		require.Equal(t, ota.VerdictDuplicate, verdict)
		require.Equal(t, before, installed(t, e, "acme", "dev-1")["firmware"], "a superseded assignment must not overwrite a newer version")
	}
}

func TestASupersededUpdatedDuplicateDoesNotRepairAMissingInstalledVersion(t *testing.T) {
	e := newUpdateEnv(t)
	ctx := tenantCtx("acme")
	e.availableArtifact(t, "acme", "fw-2", "2.0.0", []byte("firmware two"))
	a, err := e.api.AssignArtifact(ctx, "dev-1", "fw-2")
	require.NoError(t, err)
	e.mustApply(t, "acme", "dev-1", progress(a, 1, ota.StageInstalling, "b"), ota.VerdictApplied)
	report := running(a, 2, "b2")
	e.mustApply(t, "acme", "dev-1", report, ota.VerdictApplied)

	e.advance(time.Minute)
	e.availableArtifact(t, "acme", "fw-3", "3.0.0", []byte("firmware three"))
	_, err = e.api.AssignArtifact(ctx, "dev-1", "fw-3")
	require.NoError(t, err)
	old, err := e.api.AssignmentByToken(ctx, a.Assignment.Token)
	require.NoError(t, err)
	require.True(t, old.Assignment.SupersededAt.Valid)
	require.NoError(t, e.api.RDB.DB(ctx).Where("device_token = ?", "dev-1").Delete(&DeviceInstalledComponent{}).Error)
	require.Empty(t, installed(t, e, "acme", "dev-1"))

	e.advance(time.Minute)
	verdict, err := e.api.ApplyReport(WithObservedAt(ctx, e.clock), "dev-1", report)
	require.NoError(t, err)
	require.Equal(t, ota.VerdictDuplicate, verdict)
	require.Empty(t, installed(t, e, "acme", "dev-1"), "a superseded assignment cannot repair an installed observation")
}

func TestADeletionFactNeverTouchesAnotherTenantOrAnUnnamedIncarnation(t *testing.T) {
	e := newUpdateEnv(t)
	e.mustApply(t, "acme", "dev-1", inventoryOf("firmware", "1.0.0", ""), VerdictUnknownAssignment)
	e.mustApply(t, "globex", "dev-1", inventoryOf("firmware", "1.0.0", ""), VerdictUnknownAssignment)
	// A fact that names no incarnation covers nothing here.
	require.NoError(t, e.api.DeviceDeleted(tenantCtx("acme"), "dev-1", 0, e.clock.Add(time.Hour)))
	require.Len(t, installed(t, e, "acme", "dev-1"), 1)
	// Acme's deletion leaves globex's device of the same token alone.
	require.NoError(t, e.api.DeviceDeleted(tenantCtx("acme"), "dev-1", 1, e.clock))
	require.Empty(t, installed(t, e, "acme", "dev-1"))
	require.Len(t, installed(t, e, "globex", "dev-1"), 1)
}
