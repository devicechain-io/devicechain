// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"
)

// The connect lookup fills exactly the credential fields the password check reads —
// tenant, device, stored secret, expiry — and leaves the rest, the credential's own
// metadata included, at their zero values. ResolveDeviceCredential does not return the
// credential, so this asks the finder directly.
func TestTheConnectLookupFillsOnlyTheConnectFields(t *testing.T) {
	f := newSQLiteCredentialFixture(t)
	storeVariableWidthColumns(t, f)

	cred, err := f.api.deviceCredentialForConnect(f.ctx, string(CredentialMqttBasic), "cred-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if cred.ID == 0 || cred.TenantId != "acme" || cred.DeviceId != f.devId ||
		!cred.SecretDigest.Valid || !verifies(cred.SecretDigest.String, "s3cret") || cred.ExpiresAt.Valid {
		t.Errorf("the connect fields were not read: id %d, tenant %q, device %d (want %d), secret %v, expires %v",
			cred.ID, cred.TenantId, cred.DeviceId, f.devId, cred.SecretDigest, cred.ExpiresAt)
	}
	if cred.Metadata != nil {
		t.Errorf("the credential carries %d bytes of metadata; the connect lookup must not read it", len(*cred.Metadata))
	}
	if cred.Token != "" || cred.CredentialId != "" || cred.CredentialType != "" || cred.Enabled {
		t.Errorf("the connect lookup read columns it does not need: token %q, id %q, type %q, enabled %v",
			cred.Token, cred.CredentialId, cred.CredentialType, cred.Enabled)
	}
	if cred.Device == nil || cred.Device.ID != f.devId || cred.Device.TenantId != "acme" || cred.Device.Token != "dev" {
		t.Fatalf("the joined device is not the credential's own: %+v", cred.Device)
	}
	if cred.Device.Metadata != nil || cred.Device.DeviceTypeId != 0 || cred.Device.Name.Valid {
		t.Errorf("the joined device carries columns the connect does not read: %+v", cred.Device)
	}
}

// Two live rows for one presented credential break the invariant the live-rows partial
// unique index keeps on PostgreSQL. SQLite's AutoMigrate creates no such index, which is
// what lets this test seed the state at all. Both finders must then refuse — neither may
// pick a row and authenticate against it — and refuse with an error that is not
// ErrRecordNotFound, since the credential does exist and the store is what is wrong.
func TestBothCredentialFindersRefuseAnAmbiguousMatch(t *testing.T) {
	f := newSQLiteCredentialFixture(t)
	dup := &DeviceCredential{DeviceId: f.devId, CredentialType: string(CredentialMqttBasic),
		CredentialId: "cred-1", SecretDigest: testDigest("other"), Enabled: true}
	dup.Token = "c-dup"
	mustExec(t, f.api.RDB.DB(f.ctx).Create(dup))

	finders := map[string]func(context.Context, string, string) (*DeviceCredential, error){
		"DeviceCredentialByCredentialId": f.api.DeviceCredentialByCredentialId,
		"deviceCredentialForConnect":     f.api.deviceCredentialForConnect,
	}
	for name, find := range finders {
		cred, err := find(f.ctx, string(CredentialMqttBasic), "cred-1")
		if err == nil || errors.Is(err, gorm.ErrRecordNotFound) {
			t.Errorf("%s over two live rows: got (%v, %v), want an ambiguity refusal", name, cred, err)
		}
	}
}
