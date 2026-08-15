// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// CredentialType.Valid accepts the known credential types and rejects others
// (ADR-014).
func TestCredentialTypeValid(t *testing.T) {
	for _, valid := range []CredentialType{
		CredentialAccessToken,
		CredentialX509Certificate,
		CredentialMqttBasic,
	} {
		if !valid.Valid() {
			t.Errorf("known credential type %q rejected", valid)
		}
	}
	for _, invalid := range []CredentialType{"", "BOGUS", "access_token"} {
		if invalid.Valid() {
			t.Errorf("unknown credential type %q accepted", invalid)
		}
	}
}

func TestCredentialTypeString(t *testing.T) {
	if CredentialAccessToken.String() != "ACCESS_TOKEN" {
		t.Fatalf("unexpected string value: %s", CredentialAccessToken.String())
	}
}

// A paged read of device credentials leads with the credential that has the MOST
// runway left, because that is the one mintOrReuseCredential hands back: it walks the
// unbounded result and returns the first live credential it sees, so the ordering IS
// the reuse policy. DeviceCredential.DefaultOrder therefore sorts expires_at DESC
// NULLS FIRST — never-expiring first, then furthest expiry. The obvious id ASC would
// have returned the credential CLOSEST to expiry and forced the earliest possible
// re-provision, which is the defect this pins.
//
// It also pins that "expires_at DESC NULLS FIRST" PARSES on sqlite. Postgres defaults
// DESC to NULLS FIRST and sqlite puts NULLs first either way, so the placement is
// stated explicitly to keep the harness and production from differing silently; that
// only helps if the harness actually accepts the syntax, which nothing else asserts.
func TestDeviceCredentialsOrderLeadsWithMostRunway(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, rdb.RegisterTenantScoping(db), "register tenant scoping")
	require.NoError(t, db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceCredential{}), "migrate")
	api := NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "acme")

	soon := time.Now().Add(1 * time.Hour)
	far := time.Now().Add(1000 * time.Hour)
	// Inserted soonest-first so ascending id order is the WRONG answer: if the clause
	// were dropped, the heap/id order would surface "soon" and this test would fail.
	for _, c := range []struct {
		token     string
		expiresAt *time.Time
	}{{"soon", &soon}, {"never", nil}, {"far", &far}} {
		row := DeviceCredential{CredentialType: "ACCESS_TOKEN", CredentialId: c.token, Enabled: true}
		row.Token = c.token
		row.TenantId = "acme"
		if c.expiresAt != nil {
			row.ExpiresAt.Valid, row.ExpiresAt.Time = true, *c.expiresAt
		}
		require.NoError(t, api.RDB.DB(ctx).Create(&row).Error, "seed credential %q", c.token)
	}

	res, err := api.DeviceCredentials(ctx, DeviceCredentialSearchCriteria{
		Pagination: rdb.Pagination{Unbounded: true},
	})
	require.NoError(t, err, "unbounded credential read (NULLS FIRST must parse on sqlite)")

	order := make([]string, 0, len(res.Results))
	for _, cred := range res.Results {
		order = append(order, cred.Token)
	}
	require.Equal(t, []string{"never", "far", "soon"}, order,
		"credentials must lead with the most runway; mintOrReuseCredential reuses the first")
}
