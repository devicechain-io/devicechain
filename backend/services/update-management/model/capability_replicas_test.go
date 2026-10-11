// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/stretchr/testify/require"
)

// missOnce is a secret store whose first Resolve answers "not found": the read a replica makes
// before another replica's insert of the same key has committed.
type missOnce struct {
	secrets.SecretStore
	missed bool
}

func (m *missOnce) Resolve(ctx context.Context, ref secrets.SecretRef) ([]byte, error) {
	if !m.missed {
		m.missed = true
		return nil, secrets.ErrSecretNotFound
	}
	return m.SecretStore.Resolve(ctx, ref)
}

// CreateIfAbsent passes through: the store under the wrapper is the real one.
func (m *missOnce) CreateIfAbsent(ctx context.Context, ref secrets.SecretRef, v []byte) (bool, error) {
	return secrets.CreateIfAbsent(ctx, m.SecretStore, ref, v)
}

// Two replicas minting the first capability of a tenant at once must end with ONE key: the second
// creates insert-if-absent, loses, and signs with the winner key rather than replacing it.
func TestConcurrentFirstMintKeepsOneKey(t *testing.T) {
	e := newUpdateEnv(t)
	kp, err := secrets.NewInstanceKeyProvider(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	shared := secrets.NewStore(e.api.RDB.Database, kp)
	replica := func(store secrets.SecretStore) *Api {
		a := NewApi(&rdb.RdbManager{Database: e.api.RDB.Database}, e.store, Limits{})
		a.Secrets = store
		a.Clock = func() time.Time { return e.clock }
		return a
	}
	a := replica(shared)
	b := replica(&missOnce{SecretStore: shared})
	c := replica(shared)

	exp := e.clock.Add(5 * time.Minute)
	capA, err := a.MintCapability(context.Background(), Capability{Tenant: "acme", DeviceToken: "dev-1",
		AssignmentID: "asg-1", Digest: "sha256:x", ArtifactTok: "fw-1", Expires: exp})
	require.NoError(t, err)
	// B read before A's row was visible, so it creates a key of its own.
	_, err = b.MintCapability(context.Background(), Capability{Tenant: "acme", DeviceToken: "dev-2",
		AssignmentID: "asg-2", Digest: "sha256:y", ArtifactTok: "fw-2", Expires: exp})
	require.NoError(t, err)

	// A third replica, with nothing cached, must accept what A minted.
	_, err = c.VerifyCapability(context.Background(), capA, "fw-1")
	require.NoError(t, err, "a capability minted by replica A is refused by replica C: the tenant key was overwritten")
}

// Rows the sweep skips must not starve the rows behind them.
func TestSweepIsNotStarvedBySkippedRows(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "globex", "fw-g", "1.0.0", []byte("globex firmware"))
	e.availableArtifact(t, "acme", "fw-a", "1.0.0", []byte("acme firmware"))
	_, err := e.api.AssignArtifact(tenantCtx("globex"), "dev-1", "fw-g")
	require.NoError(t, err)
	e.advance(time.Minute)
	live, err := e.api.AssignArtifact(tenantCtx("acme"), "dev-1", "fw-a")
	require.NoError(t, err)
	e.api.TenantDeleted = func(tenant string) bool { return tenant == "globex" }

	e.advance(2 * time.Hour) // past the acknowledge deadline for both
	for range 5 {
		_, err := e.api.SweepDeadlines(context.Background(), 1)
		require.NoError(t, err)
	}
	require.Equal(t, ota.StateTimedOut, e.state(t, "acme", live),
		"a deleted tenant's older row filled every page and the live tenant's attempt was never swept")
}
