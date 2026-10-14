// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The signer-row lock that pins an assignment's signature, asked of a real PostgreSQL server.
//
// SQLite takes no row locks, so the unit tests prove the pin logic and cannot prove the one thing
// the FOR SHARE read exists for: that a revoke and a pin that overlap in time serialize. Run
// against a throwaway server (hack/integration-tests.sh does this in CI):
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run Pin -v
package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-update-management/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// pinFixture is a signed artifact on a real server and the means to race a pin against a revoke.
type pinFixture struct {
	api      *Api
	artifact *Artifact
	signer   *ArtifactSigner
}

func newPinFixture(t *testing.T) *pinFixture {
	t.Helper()
	ctx := context.Background()
	host, port := itServer(t)
	// A fresh database per run: nothing to truncate, and no system-context escape hatch needed.
	instance := fmt.Sprintf("updatepinit%d", time.Now().UnixNano())
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "update-management"},
		Migrations:   schema.Migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": "postgres", "password": "postgres", "sslMode": "disable",
			},
		},
	}
	require.NoError(t, mgr.ExecuteInitialize(ctx))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	store, err := blob.New(ctx, blob.Config{Backend: blob.BackendFilesystem, Directory: t.TempDir()}, instance)
	require.NoError(t, err)
	api := NewApi(mgr, store, Limits{MaxArtifactBytes: 1000, ConfiguredMaxArtifactBytes: 1000, MaxTenantArtifactBytes: 1000})
	api.Profiles = &fakeProfiles{known: map[string][]string{"acme": {"gateway"}}}

	tctx := tenantCtx("acme")
	content := []byte("x")
	artifact, err := api.CreateArtifact(tctx, validRequest("fw-1", content))
	require.NoError(t, err)
	kp := newEd25519(t)
	signer, err := api.RegisterSigner(tctx, "release", kp.pem)
	require.NoError(t, err)
	_, err = api.AddSignature(tctx, "fw-1", signer.Token, b64(kp.sign(rawDigest(content))))
	require.NoError(t, err)
	return &pinFixture{api: api, artifact: artifact, signer: signer}
}

// settles reports whether done closes within d.
func settles(done <-chan struct{}, d time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// A revoke that arrives while a pinning transaction is open waits for it: the pin then commits
// naming a signer that was ACTIVE when it was read, and the revoke lands after.
func TestARevokeWaitsForAnOpenPinningTransactionOnPostgres(t *testing.T) {
	f := newPinFixture(t)
	ctx := tenantCtx("acme")

	tx := f.api.RDB.DB(ctx).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	pin, err := f.api.pinSignature(tx, f.artifact)
	require.NoError(t, err)
	require.NotNil(t, pin, "the signer is ACTIVE inside the open transaction")

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := f.api.RevokeSigner(ctx, f.signer.Token)
		require.NoError(t, err)
	}()
	require.False(t, settles(done, time.Second), "the revoke must wait for the transaction that holds the signer row FOR SHARE")

	require.NoError(t, tx.Commit().Error)
	require.True(t, settles(done, 10*time.Second), "the revoke proceeds once the pinning transaction commits")
	got, err := f.api.SignerByToken(ctx, f.signer.Token)
	require.NoError(t, err)
	require.Equal(t, SignerRevoked, got.State)
}

// A pin that arrives while a revoke is uncommitted waits for it, and then sees REVOKED: it can
// never pin a signer whose revoke is already under way.
func TestAPinWaitsForAnUncommittedRevokeAndThenSeesRevokedOnPostgres(t *testing.T) {
	f := newPinFixture(t)
	ctx := tenantCtx("acme")

	revoke := f.api.RDB.DB(ctx).Begin()
	require.NoError(t, revoke.Error)
	defer revoke.Rollback()
	require.NoError(t, revoke.Model(&ArtifactSigner{}).Where("id = ?", f.signer.ID).Update("state", SignerRevoked).Error)

	type result struct {
		pin *SignaturePin
		err error
	}
	out := make(chan result, 1)
	go func() {
		var r result
		r.err = f.api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			var err error
			r.pin, err = f.api.pinSignature(tx, f.artifact)
			return err
		})
		out <- r
	}()
	select {
	case r := <-out:
		t.Fatalf("the pin must wait for the uncommitted revoke, but returned pin=%v err=%v", r.pin, r.err)
	case <-time.After(time.Second):
	}

	require.NoError(t, revoke.Commit().Error)
	select {
	case r := <-out:
		require.NoError(t, r.err)
		require.Nil(t, r.pin, "after the revoke committed there is no active signer to pin")
	case <-time.After(10 * time.Second):
		t.Fatal("the pin never proceeded after the revoke committed")
	}
}
