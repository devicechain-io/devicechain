// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

// recordingKicker is a SessionKicker that records every request and answers with err.
type recordingKicker struct {
	calls []kickCall
	err   error
}

type kickCall struct {
	tenant, reason string
	tokens         []string
}

func (k *recordingKicker) KickDeviceSessions(_ context.Context, tenant, reason string, tokens []string) error {
	k.calls = append(k.calls, kickCall{tenant, reason, append([]string(nil), tokens...)})
	return k.err
}

func kickFixture(t *testing.T) (*Api, context.Context, *recordingKicker) {
	t.Helper()
	api, ctx := resolveFixture(t)
	k := &recordingKicker{}
	api.SessionKicker = k
	return api, ctx, k
}

// REVOCATION KICKS. Disabling a credential ends the device's live sessions; the kick names the
// tenant and the device token the broker knows the session by.
//
// Negative control: with endDeviceSessions removed from UpdateDeviceCredential this has no call.
func TestDisablingACredentialKicksTheDevicesSessions(t *testing.T) {
	api, ctx, k := kickFixture(t)
	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	require.Equal(t, []kickCall{{partialUpdateTenant, messaging.SessionKickCredentialChanged, []string{"dev"}}}, k.calls)
}

func TestRotatingACredentialsSecretKicksTheDevicesSessions(t *testing.T) {
	api, ctx, k := kickFixture(t)
	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		CredentialValue: dcgraphql.OptionalStringOf("r0tated-secret-0123")})
	require.NoError(t, err)
	require.Len(t, k.calls, 1)
}

func TestAnEditThatChangesNoAuthorityKicksNothing(t *testing.T) {
	api, ctx, k := kickFixture(t)
	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		Metadata: dcgraphql.OptionalStringOf(`{"note":"renamed"}`)})
	require.NoError(t, err)
	require.Empty(t, k.calls, "a metadata edit must not drop a healthy device's connection")
	_, err = api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	require.Empty(t, k.calls, "enabling is not a revocation")
}

func TestDeletingACredentialKicksTheDevicesSessions(t *testing.T) {
	api, ctx, k := kickFixture(t)
	deleted, err := api.DeleteDeviceCredential(ctx, "c-1")
	require.NoError(t, err)
	require.True(t, deleted)
	require.Equal(t, []kickCall{{partialUpdateTenant, messaging.SessionKickCredentialDeleted, []string{"dev"}}}, k.calls)

	k.calls = nil
	deleted, err = api.DeleteDeviceCredential(ctx, "c-1")
	require.NoError(t, err)
	require.False(t, deleted)
	require.Empty(t, k.calls, "deleting what is not there ends no session")
}

func TestDisablingADeviceKicksItAndEditingOrEnablingItDoesNot(t *testing.T) {
	api, ctx, k := kickFixture(t)
	_, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Name: dcgraphql.OptionalStringOf("renamed")})
	require.NoError(t, err)
	require.Empty(t, k.calls)

	_, err = api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	require.Equal(t, []kickCall{{partialUpdateTenant, messaging.SessionKickDeviceDisabled, []string{"dev"}}}, k.calls)

	k.calls = nil
	_, err = api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	require.Empty(t, k.calls, "re-enabling a device ends nothing")
}

func TestDeletingADeviceKicksItsSessions(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	k := &recordingKicker{}
	api.SessionKicker = k
	dev := &Device{}
	dev.Token = "dev-1"
	require.NoError(t, api.RDB.DB(ctx).Create(dev).Error)

	deleted, err := api.DeleteDevice(ctx, "dev-1")
	require.NoError(t, err)
	require.True(t, deleted)
	require.Equal(t, []kickCall{{"acme", messaging.SessionKickDeviceDeleted, []string{"dev-1"}}}, k.calls)

	k.err = errors.New("no responder")
	require.NoError(t, api.RDB.DB(ctx).Create(&Device{TokenReference: rdb.TokenReference{Token: "dev-2"}}).Error)
	_, err = api.DeleteDevice(ctx, "dev-2")
	require.ErrorIs(t, err, ErrSessionsNotEnded)
}

// FAIL CLOSED. A kick that cannot be confirmed is an ERROR from the operation that withdrew
// the authority, never a quiet success; the change itself is saved (the next CONNECT is
// refused), and repeating the operation asks again.
func TestAKickThatCannotBeConfirmedIsReportedAndRetriedByRepeating(t *testing.T) {
	api, ctx, k := kickFixture(t)
	k.err = errors.New("the broker cluster could not be consulted in full")

	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	require.Contains(t, err.Error(), "broker cluster could not be consulted", "the cause is surfaced")
	// Saved: the credential no longer authenticates a new connect.
	_, authErr := api.AuthenticateDeviceConnect(ctx, basic("cred-1", "s3cret-0123456789"), time.Now())
	require.ErrorIs(t, authErr, ErrCredentialNotResolved)

	// The retry: the same disable asks the broker again, and succeeds once it can.
	k.err = nil
	_, err = api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	require.Len(t, k.calls, 2)

	// Same for a device disable and a credential delete.
	k.err = errors.New("no responder")
	_, err = api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	_, err = api.DeleteDeviceCredential(ctx, "c-1")
	require.ErrorIs(t, err, ErrSessionsNotEnded)
}

func TestNoKickerWiredIsAnInstanceWithoutAnMqttCallout(t *testing.T) {
	api, ctx := resolveFixture(t)
	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
}

// THE JWT CLAMP'S INPUT. The two connect lookups hand the callout the expiry of the credential
// they authenticated; the per-event lookup does not (it has no use for it and copies nothing).
func TestTheConnectLookupsCarryTheCredentialsExpiry(t *testing.T) {
	api, ctx := resolveFixture(t)
	exp := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		ExpiresAt: dcgraphql.OptionalStringOf(exp.Format(time.RFC3339))})
	require.NoError(t, err)

	d, _, err := api.ResolveDeviceCredential(ctx, basic("cred-1", "x"), time.Now())
	require.NoError(t, err)
	require.True(t, d.ConnectCredentialExpiresAt.Valid)
	require.True(t, exp.Equal(d.ConnectCredentialExpiresAt.Time), "got %s want %s", d.ConnectCredentialExpiresAt.Time, exp)

	// An access token takes the other connect path.
	_, err = api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "c-2", DeviceToken: "dev", CredentialType: string(CredentialAccessToken), CredentialId: "tok-2", Enabled: true,
		ExpiresAt: ptr(exp.Format(time.RFC3339))})
	require.NoError(t, err)
	d, err = api.AuthenticateDeviceConnect(ctx, accessToken("tok-2"), time.Now())
	require.NoError(t, err)
	require.True(t, exp.Equal(d.ConnectCredentialExpiresAt.Time))

	d, err = api.AuthenticateDevice(ctx, accessToken("tok-2"), time.Now())
	require.NoError(t, err)
	require.False(t, d.ConnectCredentialExpiresAt.Valid, "the per-event path does not carry it")
}

func ptr[T any](v T) *T { return &v }

// 🔴 A DEVICE DISABLE'S KICK IS DURABLE. A kick that could not be confirmed after the disable
// committed used to be retried only if an operator repeated the call; it is now a queued debt
// (as a trust anchor's is) that the background drain clears once the broker answers, and that
// re-enabling the device withdraws.
//
// Negative control: with the queue call removed from UpdateDevice there is no row and the drain
// below has nothing to retry.
func TestADisableWhoseKickFailedIsRetriedByTheBackgroundDrain(t *testing.T) {
	api, ctx := resolveFixture(t)
	k := &togglingKicker{}
	api.SessionKicker = k

	_, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.ErrorIs(t, err, ErrSessionsNotEnded, "the caller is told the disable is saved but the sessions are not confirmed ended")
	var rows []PendingSessionKick
	require.NoError(t, api.RDB.DB(ctx).Find(&rows).Error)
	require.Len(t, rows, 1, "the unconfirmed kick is on record")
	require.Equal(t, kickOriginDeviceDisabled, rows[0].Origin)

	// The responder comes back; nobody repeats the call. The background pass asks again.
	k.set(true)
	res, err := api.DrainSessionKicks(ctx, SessionKickDrainPageSize)
	require.NoError(t, err)
	require.Equal(t, 1, res.Tenants)
	require.NoError(t, api.RDB.DB(ctx).Find(&rows).Error)
	require.Empty(t, rows, "a confirmed kick clears its row")
	last := k.calls[len(k.calls)-1]
	require.Equal(t, togglingCall{messaging.SessionKickDeviceDisabled, []string{"dev"}}, last, "the kick carries the device-disabled reason")
}

func TestReEnablingADeviceWithdrawsItsOwedKick(t *testing.T) {
	api, ctx := resolveFixture(t)
	k := &togglingKicker{}
	api.SessionKicker = k
	_, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.ErrorIs(t, err, ErrSessionsNotEnded)

	_, err = api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	var rows []PendingSessionKick
	require.NoError(t, api.RDB.DB(ctx).Find(&rows).Error)
	require.Empty(t, rows, "an enabled device that has reconnected must not be kicked by a stale debt")
	k.set(true)
	calls := len(k.calls)
	_, err = api.DrainSessionKicks(ctx, SessionKickDrainPageSize)
	require.NoError(t, err)
	require.Len(t, k.calls, calls, "the drain asks the broker about nothing")
}

// A process restarted WITHOUT the callout still clears the debt when the device is enabled.
func TestEnablingADeviceClearsItsOwedKickEvenWithNoKickerWired(t *testing.T) {
	api, ctx := resolveFixture(t)
	api.SessionKicker = &togglingKicker{}
	_, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.ErrorIs(t, err, ErrSessionsNotEnded)

	api.SessionKicker = nil
	_, err = api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	var rows []PendingSessionKick
	require.NoError(t, api.RDB.DB(ctx).Find(&rows).Error)
	require.Empty(t, rows)
}

// A debt that outlived the disable (a replica on a release that does not clear it on enable, or a
// drain that read the row before the enable deleted it) is dropped at collection, not acted on.
func TestTheDrainDropsADeviceDisabledDebtOfAnEnabledDevice(t *testing.T) {
	api, ctx := resolveFixture(t)
	k := &togglingKicker{up: true}
	api.SessionKicker = k
	require.NoError(t, api.RDB.DB(ctx).Create(&PendingSessionKick{TenantId: partialUpdateTenant, DeviceToken: "dev",
		Origin: kickOriginDeviceDisabled, Generation: 1, RequestedAt: time.Now()}).Error)

	_, err := api.DrainSessionKicks(ctx, SessionKickDrainPageSize)
	require.NoError(t, err)
	require.Empty(t, k.calls, "the device is enabled: ending its session would drop a healthy connection")
	var rows []PendingSessionKick
	require.NoError(t, api.RDB.DB(ctx).Find(&rows).Error)
	require.Empty(t, rows)

	// While it is disabled, the same row is collected and the kick is made.
	_, err = api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	require.Len(t, k.calls, 1)
}

// 🔴 A DISABLED DEVICE THAT IS THEN DELETED STILL OWES ITS KICK. The device row is gone (a delete
// removes it for good), so a debt whose device cannot be found must be KEPT, not read as
// "enabled" and dropped. Here only the disable's debt exists (the delete is by a release or a
// path that queued nothing of its own), so it is the only thing that can end the session.
//
// Negative control: dropping a debt when the row is missing (the earlier rule) clears it with no
// kick made.
func TestADisabledThenDeletedDeviceStillOwesItsKick(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	k := &togglingKicker{up: true}
	api.SessionKicker = k
	require.NoError(t, api.RDB.DB(ctx).Create(&Device{TokenReference: rdb.TokenReference{Token: "dev-1"}}).Error)
	require.NoError(t, api.RDB.DB(ctx).Create(&PendingSessionKick{TenantId: "acme", DeviceToken: "dev-1",
		Origin: kickOriginDeviceDisabled, Generation: 1, RequestedAt: time.Now()}).Error)
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Where("token = ?", "dev-1").Delete(&Device{}).Error)

	_, err := api.DrainSessionKicks(ctx, SessionKickDrainPageSize)
	require.NoError(t, err)
	require.True(t, k.answered("dev-1"), "the broker was asked to end the session of the deleted device")
	var rows []PendingSessionKick
	require.NoError(t, api.RDB.DB(ctx).Find(&rows).Error)
	require.Empty(t, rows)
}

// A delete's kick is recorded with the delete and retried in the background, like a disable's.
func TestADeviceDeleteWhoseKickFailedIsRetriedByTheBackgroundDrain(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	k := &togglingKicker{}
	api.SessionKicker = k
	require.NoError(t, api.RDB.DB(ctx).Create(&Device{TokenReference: rdb.TokenReference{Token: "dev-1"}}).Error)
	_, err := api.DeleteDevice(ctx, "dev-1")
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	var rows []PendingSessionKick
	require.NoError(t, api.RDB.DB(ctx).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, kickOriginDeviceDeleted, rows[0].Origin)

	k.set(true)
	_, err = api.DrainSessionKicks(ctx, SessionKickDrainPageSize)
	require.NoError(t, err)
	require.Equal(t, togglingCall{messaging.SessionKickDeviceDeleted, []string{"dev-1"}}, k.calls[len(k.calls)-1])
	require.NoError(t, api.RDB.DB(ctx).Find(&rows).Error)
	require.Empty(t, rows)
}

// 🔴 THE QUEUE DECISION IS MADE FROM THE ROW AS STORED. A metadata edit read the device disabled;
// it was enabled before the save. The edit must not queue a debt (or kick) for a device that is
// enabled.
//
// Negative control: deciding from the pre-transaction copy (updated.DisabledAt.Valid) queues a
// debt here.
func TestAnEditThatReadADeviceDisabledDoesNotQueueAKickForOneEnabledBeforeTheSave(t *testing.T) {
	api, ctx := resolveFixture(t)
	k := &togglingKicker{up: true}
	api.SessionKicker = k
	// Disable it with no responder wired yet so no kick is made, then clear the debt.
	api.SessionKicker = nil
	_, err := api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	api.SessionKicker = k

	db := api.RDB.Database
	armed := true
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:racing-enable", func(d *gorm.DB) {
		row, ok := d.Statement.Dest.(*Device)
		if !armed || !ok || row.Token != "dev" {
			return
		}
		armed = false
		_ = d.Session(&gorm.Session{NewDB: true}).Model(&Device{}).Where("id = ?", row.ID).
			UpdateColumn("disabled_at", nil).Error
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove("test:racing-enable") })

	_, err = api.UpdateDevice(ctx, "dev", &DeviceUpdateRequest{Name: dcgraphql.OptionalStringOf("renamed")})
	require.NoError(t, err)
	require.False(t, armed, "the racing enable never ran; the test proves nothing")
	var rows []PendingSessionKick
	require.NoError(t, api.RDB.DB(ctx).Find(&rows).Error)
	require.Empty(t, rows, "an enabled device owes no kick")
	require.Empty(t, k.calls)
}

// Repeating a delete whose kick could not be confirmed asks again, instead of finding no device
// and reporting success.
//
// Negative control: draining only when this call deleted something returns (false, nil) here.
func TestRepeatingADeleteWhoseKickFailedAsksAgain(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	k := &togglingKicker{}
	api.SessionKicker = k
	require.NoError(t, api.RDB.DB(ctx).Create(&Device{TokenReference: rdb.TokenReference{Token: "dev-1"}}).Error)
	_, err := api.DeleteDevice(ctx, "dev-1")
	require.ErrorIs(t, err, ErrSessionsNotEnded)

	deleted, err := api.DeleteDevice(ctx, "dev-1")
	require.ErrorIs(t, err, ErrSessionsNotEnded, "still unconfirmed: the repeat must say so")
	require.False(t, deleted)

	k.set(true)
	deleted, err = api.DeleteDevice(ctx, "dev-1")
	require.NoError(t, err)
	require.False(t, deleted)
	var rows []PendingSessionKick
	require.NoError(t, api.RDB.DB(ctx).Find(&rows).Error)
	require.Empty(t, rows)
}

// A system-context delete (no tenant on the context) names the tenant the device's row holds.
//
// Negative control: reading the tenant from the context queues nothing here.
func TestADeleteWithNoTenantOnTheContextStillQueuesItsKick(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	tctx := core.WithTenant(context.Background(), "acme")
	k := &togglingKicker{}
	api.SessionKicker = k
	require.NoError(t, api.RDB.DB(tctx).Create(&Device{TokenReference: rdb.TokenReference{Token: "dev-1"}}).Error)

	sys := core.WithSystemContext(context.Background())
	_, err := api.DeleteDevice(sys, "dev-1")
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	// Repeating it finds no device and no tenant, and must still ask again.
	_, err = api.DeleteDevice(sys, "dev-1")
	require.ErrorIs(t, err, ErrSessionsNotEnded, "the repeat recovers the tenant from the debt")
	var rows []PendingSessionKick
	require.NoError(t, api.RDB.DB(tctx).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "acme", rows[0].TenantId)
	require.Equal(t, kickOriginDeviceDeleted, rows[0].Origin)
}

// A repeated system-context delete reads which tenants owe the kick, and drains only the one
// debt it names. It touches no other tenant's rows and no other device's.
//
// Negative control: draining without the origin/token scope clears acme's other rows too.
func TestARepeatedSystemDeleteTouchesOnlyTheDebtItNames(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	globex := core.WithTenant(context.Background(), "globex")
	k := &togglingKicker{up: true}
	api.SessionKicker = k
	now := time.Now()
	seed := func(tenant, token string, origin uint) {
		require.NoError(t, api.RDB.DB(core.WithTenant(context.Background(), tenant)).Create(
			&PendingSessionKick{TenantId: tenant, DeviceToken: token, Origin: origin, Generation: 1, RequestedAt: now}).Error)
	}
	seed("acme", "gone", kickOriginDeviceDeleted)
	seed("acme", "other", kickOriginDeviceDeleted)
	seed("globex", "bystander", kickOriginDeviceDeleted)

	deleted, err := api.DeleteDevice(core.WithSystemContext(context.Background()), "gone")
	require.NoError(t, err)
	require.False(t, deleted)

	var rows []PendingSessionKick
	require.NoError(t, api.RDB.DB(acme).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "other", rows[0].DeviceToken)
	require.NoError(t, api.RDB.DB(globex).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "bystander", rows[0].DeviceToken)
	for _, c := range k.calls {
		require.Equal(t, []string{"gone"}, c.tokens, "only the named device is asked about")
	}
}

// A token that owes a kick in two tenants is ambiguous for a system caller: it is refused and
// nothing is cleared, rather than ending sessions in a tenant the call was not about.
func TestARepeatedSystemDeleteOfATokenOwedByTwoTenantsIsRefused(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	k := &togglingKicker{up: true}
	api.SessionKicker = k
	for _, tenant := range []string{"acme", "globex"} {
		require.NoError(t, api.RDB.DB(core.WithTenant(context.Background(), tenant)).Create(
			&PendingSessionKick{TenantId: tenant, DeviceToken: "shared", Origin: kickOriginDeviceDeleted, Generation: 1, RequestedAt: time.Now()}).Error)
	}
	_, err := api.DeleteDevice(core.WithSystemContext(context.Background()), "shared")
	require.ErrorIs(t, err, ErrSessionsNotEnded)
	require.Empty(t, k.calls, "no tenant was asked about")
	for _, tenant := range []string{"acme", "globex"} {
		var rows []PendingSessionKick
		require.NoError(t, api.RDB.DB(core.WithTenant(context.Background(), tenant)).Find(&rows).Error)
		require.Len(t, rows, 1, tenant)
	}
}
