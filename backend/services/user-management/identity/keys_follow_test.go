// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Both managers use the same database, sealed store and real distributed lock.
func followPair(t *testing.T) (*Manager, *Manager) {
	t.Helper()
	f := newSeedFixture(t)
	sqlDB, err := f.rdbm.Database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // SQLite :memory: is per connection.
	t.Cleanup(func() { _ = sqlDB.Close() })
	a, b := f.manager(""), f.manager("")
	for _, m := range []*Manager{a, b} {
		set, err := m.loadSigningKeys(context.Background())
		require.NoError(t, err)
		m.applyKeys(set)
	}
	return a, b
}

func followToken(t *testing.T, m *Manager) string {
	t.Helper()
	m.mu.RLock()
	i := m.issuer
	m.mu.RUnlock()
	minted, err := i.IssueAccess("test", "person@example.test", nil, nil, "test-token")
	require.NoError(t, err)
	return minted.Token
}

func TestPeerValidatesRotatedSigningKey(t *testing.T) {
	a, b := followPair(t)
	validator := b.Validator()
	require.NoError(t, a.RotateSigningKey(context.Background(), 24*time.Hour))
	_, err := validator.Validate(followToken(t, a))
	require.NoError(t, err, "an unknown kid must fetch the peer's committed key")
	require.Same(t, validator, b.Validator())
	other, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	forged := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"exp": time.Now().Add(time.Hour).Unix()})
	forged.Header["kid"] = auth.Thumbprint(&other.PublicKey)
	signed, err := forged.SignedString(other)
	require.NoError(t, err)
	_, err = validator.Parse(signed)
	require.Error(t, err, "a key absent from every row must remain untrusted")
}

type countingSigningStore struct {
	secrets.SecretStore
	opens int
}

func (s *countingSigningStore) Resolve(ctx context.Context, ref secrets.SecretRef) ([]byte, error) {
	s.opens++
	return s.SecretStore.Resolve(ctx, ref)
}

func TestPeerFollowsActiveKeyWithoutUnnecessarySecretReads(t *testing.T) {
	a, b := followPair(t)
	ctx := context.Background()
	store := &countingSigningStore{SecretStore: b.secrets}
	b.secrets = store
	now := time.Now()
	b.now = func() time.Time { return now }
	changed, err := b.followSigningKeys(ctx)
	require.NoError(t, err)
	require.False(t, changed)
	require.Zero(t, store.opens)
	require.NoError(t, a.RotateSigningKey(ctx, 24*time.Hour))
	now = now.Add(signingKeyFollowMinInterval)
	changed, err = b.followSigningKeys(ctx)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, 1, store.opens)
	require.Equal(t, a.activeKid, b.activeKid)
	token, _, err := jwt.NewParser().ParseUnverified(followToken(t, b), jwt.MapClaims{})
	require.NoError(t, err)
	require.Equal(t, a.activeKid, token.Header["kid"])
}

func TestPeerJWKSConvergesAndSurvivesDatabaseFailure(t *testing.T) {
	a, b := followPair(t)
	ctx := context.Background()
	require.NoError(t, a.RotateSigningKey(ctx, 24*time.Hour))
	data, err := b.JWKS(ctx)
	require.NoError(t, err)
	require.Contains(t, string(data), a.activeKid)
	sqlDB, err := b.db.Database.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	b.now = func() time.Time { return time.Now().Add(time.Minute) }
	again, err := b.JWKS(ctx)
	require.NoError(t, err)
	require.JSONEq(t, string(data), string(again))
}

func TestAgeRotationAcrossReplicasMintsOnce(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprint(old), func(t *testing.T) {
			a, b := followPair(t)
			original := a.activeKid
			if old {
				require.NoError(t, a.db.Database.Exec("UPDATE signing_keys SET created_at = ?", time.Now().Add(-48*time.Hour)).Error)
			}
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			for _, m := range []*Manager{a, b} {
				wg.Add(1)
				go func(m *Manager) {
					defer wg.Done()
					errs <- m.MaybeRotateOnAge(context.Background(), 24*time.Hour, 8*24*time.Hour)
				}(m)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			rows := storedSigningKeys(t, a.db.Database)
			if old {
				require.Len(t, rows, 2)
				require.NotEqual(t, original, a.activeKid)
			} else {
				require.Len(t, rows, 1)
				require.Equal(t, original, a.activeKid)
			}
			require.Equal(t, a.activeKid, b.activeKid)
		})
	}
}

func TestFollowerNeverMintsAndKeepsLastKnownSigner(t *testing.T) {
	_, b := followPair(t)
	original := b.issuer
	require.NoError(t, b.db.Database.Exec("DELETE FROM signing_keys").Error)
	changed, err := b.followSigningKeys(context.Background())
	require.ErrorContains(t, err, "no active signing key")
	require.False(t, changed)
	require.Empty(t, storedSigningKeys(t, b.db.Database))
	require.Same(t, original, b.issuer)
	_, err = b.Validator().Validate(followToken(t, b))
	require.NoError(t, err, "the last known signer must still mint usable tokens")
}

func TestFollowerAdoptsPublicSetChangesAndRefusesCorruption(t *testing.T) {
	a, b := followPair(t)
	ctx := context.Background()
	require.NoError(t, a.RotateSigningKey(ctx, 24*time.Hour))
	changed, err := b.followSigningKeys(ctx)
	require.NoError(t, err)
	require.True(t, changed)
	now := time.Now().Add(signingKeyFollowMinInterval)
	b.now = func() time.Time { return now }
	// Pruning changes the public set even though the active kid stays the same.
	require.NoError(t, b.db.Database.Exec("DELETE FROM signing_keys WHERE active = ?", false).Error)
	changed, err = b.followSigningKeys(ctx)
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, b.publicKeys, 1)
	original := b.issuer
	now = now.Add(signingKeyFollowMinInterval)
	require.NoError(t, b.db.Database.Exec("UPDATE signing_keys SET public_key_pem = ?", "broken public key").Error)
	changed, err = b.followSigningKeys(ctx)
	require.Error(t, err)
	require.False(t, changed)
	require.Same(t, original, b.issuer)
	require.Len(t, storedSigningKeys(t, b.db.Database), 1)
	_, err = b.Validator().Validate(followToken(t, b))
	require.NoError(t, err)
}

func TestFollowerChecksTheResolvedPrivateHalf(t *testing.T) {
	a, b := followPair(t)
	ctx := context.Background()
	require.NoError(t, a.RotateSigningKey(ctx, 24*time.Hour))
	original := b.issuer
	other, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	pem, err := auth.EncodePrivateKeyPEM(other)
	require.NoError(t, err)
	rows := storedSigningKeys(t, a.db.Database)
	pub := rowPublicKey(t, rows[len(rows)-1])
	require.NoError(t, a.secrets.Put(ctx, signingKeyRef(pub), pem))
	_, err = b.followSigningKeys(ctx)
	require.ErrorContains(t, err, "belongs to a different key")
	require.Same(t, original, b.issuer)
	require.Len(t, storedSigningKeys(t, b.db.Database), 2)
}

func TestUnknownKidsShareTheFollowReadLimit(t *testing.T) {
	_, b := followPair(t)
	reads := 0
	require.NoError(t, b.db.Database.Callback().Query().Before("gorm:query").Register("test:count_follow", func(tx *gorm.DB) {
		if tx.Statement.Table == "signing_keys" {
			reads++
		}
	}))
	now := time.Now()
	b.now = func() time.Time { return now }
	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"exp": time.Now().Add(time.Hour).Unix()})
		token.Header["kid"] = fmt.Sprintf("unknown-%d", i)
		signed, err := token.SignedString(key)
		require.NoError(t, err)
		_, err = b.Validator().Parse(signed)
		require.Error(t, err)
		_, err = b.JWKS(context.Background())
		require.NoError(t, err)
		require.NoError(t, b.signingKeyFollowTick(context.Background(), 0, 0))
	}
	require.Equal(t, 1, reads)
	now = now.Add(signingKeyFollowMinInterval)
	_, err = b.JWKS(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, reads, "the shared limit must permit a new read after its interval")
}

func TestPeriodicSigningKeyRotationWaitsForStartupGrace(t *testing.T) {
	a, _ := followPair(t)
	start := time.Now()
	a.startedAt = start
	now := start.Add(signingKeyStartupGrace - time.Second)
	a.now = func() time.Time { return now }
	require.NoError(t, a.db.Database.Exec("UPDATE signing_keys SET created_at = ?", start.Add(-48*time.Hour)).Error)
	original := a.activeKid
	require.NoError(t, a.signingKeyFollowTick(context.Background(), 24*time.Hour, 8*24*time.Hour))
	require.Len(t, storedSigningKeys(t, a.db.Database), 1)
	require.Equal(t, original, a.activeKid)
	now = start.Add(signingKeyStartupGrace + time.Second)
	require.NoError(t, a.signingKeyFollowTick(context.Background(), 24*time.Hour, 8*24*time.Hour))
	require.Len(t, storedSigningKeys(t, a.db.Database), 2)
	require.NotEqual(t, original, a.activeKid)
}

type missingSigningStore struct{ secrets.SecretStore }

func (s missingSigningStore) Resolve(context.Context, secrets.SecretRef) ([]byte, error) {
	return nil, secrets.ErrSecretNotFound
}

func TestFollowerRetriesAMissingPrivateHalfAndCountsFailures(t *testing.T) {
	a, b := followPair(t)
	ctx := context.Background()
	require.NoError(t, a.RotateSigningKey(ctx, 24*time.Hour))
	original := b.issuer
	store := b.secrets
	b.secrets = missingSigningStore{store}
	counter := &countingInc{}
	b.followFailures = counter
	var buf bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = previous })
	now := time.Now()
	b.now = func() time.Time { return now }
	for attempt := 0; attempt < 2; attempt++ {
		buf.Reset()
		_, err := b.followSigningKeys(ctx)
		require.ErrorIs(t, err, errSigningKeyChanged)
		require.NotErrorIs(t, err, errActiveKeyNotSealed)
		require.Same(t, original, b.issuer)
		if attempt == 0 {
			require.Contains(t, buf.String(), `"level":"warn"`)
			require.NotContains(t, buf.String(), `"level":"error"`)
		} else {
			require.Contains(t, buf.String(), `"level":"error"`)
		}
		now = now.Add(signingKeyFollowMinInterval)
	}
	require.Equal(t, 2, counter.n)
	b.secrets = store
	changed, err := b.followSigningKeys(ctx)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, a.activeKid, b.activeKid)
}

// countingInc stands in for the failure counter.
type countingInc struct{ n int }

func (c *countingInc) Inc() { c.n++ }
