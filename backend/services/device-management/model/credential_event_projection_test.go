// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// The per-event credential lookup, and the access-token connect behind it, read only the
// columns the resolver uses. Metadata, name and description of both rows are not read: the
// device's are zero after the lookup, and the statement does not name them. The external id
// IS read, because the resolver stamps it on every event.
func TestTheEventCredentialLookupReadsOnlyWhatResolutionUses(t *testing.T) {
	f := newSQLiteCredentialFixture(t)
	storeVariableWidthColumns(t, f)

	cred, dev, err := f.api.authenticateCredential(f.ctx, basic("cred-1", "s3cret"), time.Now())
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if cred.Metadata != nil {
		t.Errorf("the credential carries %d bytes of metadata; the event lookup must not read it", len(*cred.Metadata))
	}
	if dev.Metadata != nil {
		t.Errorf("the device carries %d bytes of metadata; the event lookup must not read it", len(*dev.Metadata))
	}
	if dev.Name.Valid || dev.Description.Valid {
		t.Errorf("the device carries name %q and description %q; the event lookup must not read them",
			dev.Name.String, dev.Description.String)
	}
	if !dev.ExternalId.Valid || dev.ExternalId.String != "ext-1" || dev.DeviceTypeId == 0 ||
		dev.Token != "dev" || dev.ID != f.devId || dev.TenantId != "acme" {
		t.Errorf("a column the resolver reads was not read: %+v", dev)
	}

	f.stmts.reset()
	if _, err := f.api.AuthenticateDevice(f.ctx, basic("cred-1", "s3cret"), time.Now()); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	requireOneJoinedStatement(t, f, "event authentication")
	stmt := strings.ToLower(f.stmts.taken()[0])
	for _, banned := range []string{"metadata", "description", "`name`", `"name"`} {
		if strings.Contains(stmt, banned) {
			t.Errorf("the event lookup reads %s: %s", banned, stmt)
		}
	}
	for _, needed := range []string{"external_id", "device_type_id", "credential_type", "credential_value", "expires_at", "enabled"} {
		if !strings.Contains(stmt, needed) {
			t.Errorf("the event lookup does not read %s: %s", needed, stmt)
		}
	}
}

// credProjection and deviceProjection are every field of a resolved credential and its
// device that anything downstream of the lookup reads: evaluateCredential,
// credentialDevice, the credential cache's fill and the event resolver. A field the
// resolver starts to read belongs here AND in authDeviceFields; this test fails if only
// the first is done.
type credProjection struct {
	ID, DeviceId                      uint
	Tenant, Type, Value               string
	ValueValid, Enabled, ExpiresValid bool
	Expires                           time.Time
}

type deviceProjection struct {
	ID, DeviceTypeId     uint
	Tenant, Token, ExtID string
	ExtIDValid           bool
}

func projectCredential(c *DeviceCredential) credProjection {
	return credProjection{
		ID: c.ID, DeviceId: c.DeviceId, Tenant: c.TenantId, Type: c.CredentialType,
		Value: c.CredentialValue.String, ValueValid: c.CredentialValue.Valid, Enabled: c.Enabled,
		ExpiresValid: c.ExpiresAt.Valid, Expires: c.ExpiresAt.Time.UTC(),
	}
}

func projectDevice(d *Device) deviceProjection {
	if d == nil {
		return deviceProjection{}
	}
	return deviceProjection{ID: d.ID, DeviceTypeId: d.DeviceTypeId, Tenant: d.TenantId, Token: d.Token,
		ExtID: d.ExternalId.String, ExtIDValid: d.ExternalId.Valid}
}

// The narrow read resolves exactly what the full read resolves, for every shape of
// credential and device the resolver meets: both credential types, a device with and
// without an external id and with large metadata, an expiring credential, a disabled one,
// a soft-deleted device, a device in another tenant, an unknown credential, another
// tenant's context and no tenant at all. The verdict must match as well as the row, and
// tenant scoping must still apply to the narrow statement.
func TestTheEventLookupResolvesWhatTheFullReadResolves(t *testing.T) {
	f := newSQLiteCredentialFixture(t)
	now := time.Now()
	db := func() *gorm.DB { return f.api.RDB.DB(f.ctx) }

	dev2, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: "dev2", DeviceTypeToken: "dt"})
	if err != nil {
		t.Fatal(err)
	}
	_ = dev2
	mk := func(token, id string, ctype CredentialType, enabled bool, exp *time.Time) {
		t.Helper()
		req := &DeviceCredentialCreateRequest{Token: token, DeviceToken: "dev2", CredentialType: string(ctype),
			CredentialId: id, Enabled: enabled}
		if ctype == CredentialMqttBasic {
			v := "pw-" + id
			req.CredentialValue = &v
		}
		if _, err := f.api.CreateDeviceCredential(f.ctx, req); err != nil {
			t.Fatalf("seed %s: %v", token, err)
		}
		if exp != nil {
			mustExec(t, db().Model(&DeviceCredential{}).Where("credential_id = ?", id).Update("expires_at", *exp))
		}
	}
	soon, past := now.Add(time.Hour), now.Add(-time.Hour)
	mk("c-3", "tok-2", CredentialAccessToken, true, nil)
	mk("c-4", "tok-exp", CredentialAccessToken, true, &soon)
	mk("c-5", "tok-past", CredentialAccessToken, true, &past)
	mk("c-6", "tok-off", CredentialAccessToken, false, nil)
	mk("c-7", "tok-gone", CredentialAccessToken, true, nil)
	mk("c-8", "tok-cross", CredentialAccessToken, true, nil)
	mk("c-9", "basic-2", CredentialMqttBasic, true, nil)
	storeVariableWidthColumns(t, f) // dev: external id, name, description, 64 KiB metadata on both rows

	gone, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: "gone", DeviceTypeToken: "dt"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db().Model(&DeviceCredential{}).Where("credential_id = ?", "tok-gone").Update("device_id", gone.ID))
	mustExec(t, db().Delete(&Device{}, gone.ID))

	other := core.WithTenant(context.Background(), "other")
	if _, err := f.api.CreateDeviceType(other, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
		t.Fatal(err)
	}
	foreign, err := f.api.CreateDevice(other, &DeviceCreateRequest{Token: "foreign", DeviceTypeToken: "dt"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db().Model(&DeviceCredential{}).Where("credential_id = ?", "tok-cross").Update("device_id", foreign.ID))

	cases := []struct {
		name  string
		ctx   context.Context
		ctype CredentialType
		id    string
		pw    string
	}{
		{"basic with external id and metadata", f.ctx, CredentialMqttBasic, "cred-1", "s3cret"},
		{"basic, wrong secret", f.ctx, CredentialMqttBasic, "cred-1", "wrong"},
		{"basic, bare device", f.ctx, CredentialMqttBasic, "basic-2", "pw-basic-2"},
		{"access token with external id", f.ctx, CredentialAccessToken, "tok-1", ""},
		{"access token, bare device", f.ctx, CredentialAccessToken, "tok-2", ""},
		{"unexpired credential", f.ctx, CredentialAccessToken, "tok-exp", ""},
		{"expired credential", f.ctx, CredentialAccessToken, "tok-past", ""},
		{"disabled credential", f.ctx, CredentialAccessToken, "tok-off", ""},
		{"soft-deleted device", f.ctx, CredentialAccessToken, "tok-gone", ""},
		{"device in another tenant", f.ctx, CredentialAccessToken, "tok-cross", ""},
		{"unknown credential", f.ctx, CredentialAccessToken, "nope", ""},
		{"another tenant's context", other, CredentialAccessToken, "tok-1", ""},
		{"no tenant in context", context.Background(), CredentialAccessToken, "tok-1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full, fullErr := f.api.DeviceCredentialByCredentialId(tc.ctx, string(tc.ctype), tc.id)
			narrow, narrowErr := f.api.deviceCredentialForAuth(tc.ctx, string(tc.ctype), tc.id)
			if (fullErr == nil) != (narrowErr == nil) ||
				errors.Is(fullErr, gorm.ErrRecordNotFound) != errors.Is(narrowErr, gorm.ErrRecordNotFound) {
				t.Fatalf("lookup verdicts differ: full %v, narrow %v", fullErr, narrowErr)
			}
			if fullErr == nil {
				if got, want := projectCredential(narrow), projectCredential(full); got != want {
					t.Errorf("the narrow read resolved credential %+v, the full read %+v", got, want)
				}
				if got, want := projectDevice(narrow.Device), projectDevice(full.Device); got != want {
					t.Errorf("the narrow read resolved device %+v, the full read %+v", got, want)
				}
			}

			// The whole check, as the resolver runs it, against a reference built from the
			// full row.
			presented := &PresentedCredential{CredentialType: string(tc.ctype), CredentialId: tc.id}
			if tc.ctype == CredentialMqttBasic {
				pw := tc.pw
				presented.Secret = &pw
			}
			gotDev, gotErr := f.api.AuthenticateDevice(tc.ctx, presented, now)
			var wantDev *Device
			var wantErr error
			switch {
			case errors.Is(fullErr, gorm.ErrRecordNotFound):
				wantErr = ErrCredentialNotResolved
			case fullErr != nil:
				wantErr = fullErr
			default:
				if wantErr = evaluateCredential(full, presented, now); wantErr == nil {
					wantDev, wantErr = credentialDevice(full)
				}
			}
			if wantErr == nil || gotErr == nil {
				if wantErr != nil || gotErr != nil {
					t.Fatalf("AuthenticateDevice: got %v, the full read gives %v", gotErr, wantErr)
				}
			} else if !errors.Is(gotErr, wantErr) && gotErr.Error() != wantErr.Error() {
				t.Fatalf("AuthenticateDevice: got %v, the full read gives %v", gotErr, wantErr)
			}
			if wantErr == nil {
				if got, want := projectDevice(gotDev), projectDevice(wantDev); got != want {
					t.Errorf("AuthenticateDevice resolved device %+v, the full read %+v", got, want)
				}
			}
		})
	}
}
