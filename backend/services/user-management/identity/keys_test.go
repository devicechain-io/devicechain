// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-user-management/model"
	"github.com/devicechain-io/dc-user-management/schema"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// keysTestDB builds the database the way RdbManager does for a real service: every
// platform callback registered — above all the fail-closed tenant guard, so a signing-
// key or instance-secret write that lost its system context is refused here exactly as
// it would be in production — the core-owned tables migrated, and then this area's own
// migration chain, so the tables are the ones a fresh install builds.
//
// 🔴 ":memory:" gives EACH pooled connection its own empty database. A write that
// escapes the transaction lands on another connection and fails with "no such table",
// so a broken secrets.BindTx fails the tests here for that incidental reason, not on
// atomicity. Those failures are not coverage of BindTx: core/secrets/bindtx_test.go is
// what pins the store joining the caller's transaction.
func keysTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, rdb.RegisterAuditJournal(db))
	require.NoError(t, rdb.RegisterTenantFence(db))
	sys := db.WithContext(core.WithSystemContext(context.Background()))
	require.NoError(t, sys.AutoMigrate(&rdb.AuditEvent{}, &rdb.PurgedTenant{}))
	for _, m := range schema.Migrations {
		require.NoErrorf(t, m.Migrate(sys), "migration %s", m.ID)
	}
	return db
}

// rootKeyConfig is an instance secrets configuration carrying a fresh random root key.
func rootKeyConfig(t *testing.T) config.SecretsConfiguration {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	return config.SecretsConfiguration{RootKey: base64.StdEncoding.EncodeToString(raw)}
}

// keysTestManager builds a Manager the way main does — the secret store through the
// real constructor, which runs the root-key self-test, and the Manager through
// NewManager — over db.
func keysTestManager(t *testing.T, db *gorm.DB, cfg config.SecretsConfiguration) (*Manager, secrets.SecretStore) {
	t.Helper()
	store, err := secrets.NewFromConfig(context.Background(), cfg, db)
	require.NoError(t, err)
	return NewManager(nil, &rdb.RdbManager{Database: db}, nil, store, 0, 0, "", BootstrapConfig{}, nil), store
}

// storedSigningKeys reads every signing_keys row, soft-deleted or not.
func storedSigningKeys(t *testing.T, db *gorm.DB) []model.SigningKey {
	t.Helper()
	var rows []model.SigningKey
	require.NoError(t, db.WithContext(core.WithSystemContext(context.Background())).Unscoped().
		Order("id ASC").Find(&rows).Error)
	return rows
}

// storedSecret is one secrets row as the database holds it, read with raw SQL so no
// soft-delete scope or tenant callback can hide a row that is there.
type storedSecret struct {
	TenantId string
	Scope    string
	Name     string
}

func storedSecrets(t *testing.T, db *gorm.DB) []storedSecret {
	t.Helper()
	var rows []storedSecret
	require.NoError(t, db.Raw("SELECT tenant_id, scope, name FROM secrets ORDER BY id").Scan(&rows).Error)
	return rows
}

func rowPublicKey(t *testing.T, row model.SigningKey) *rsa.PublicKey {
	t.Helper()
	pub, err := auth.DecodePublicKeyPEM([]byte(row.PublicKeyPem))
	require.NoError(t, err)
	return pub
}

// privateKeyEncodings is every form a leaked private key could take in storage: its
// PKCS#1 and PKCS#8 DER, the PEM of the latter (which is what the table used to hold),
// and the base64 bodies a text column would carry.
func privateKeyEncodings(t *testing.T, key *rsa.PrivateKey) map[string][]byte {
	t.Helper()
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	pem, err := auth.EncodePrivateKeyPEM(key)
	require.NoError(t, err)
	pkcs1 := x509.MarshalPKCS1PrivateKey(key)
	return map[string][]byte{
		"PKCS#1 DER":      pkcs1,
		"PKCS#8 DER":      pkcs8,
		"PKCS#8 PEM":      pem,
		"PKCS#1 base64":   []byte(base64.StdEncoding.EncodeToString(pkcs1)),
		"PKCS#8 base64":   []byte(base64.StdEncoding.EncodeToString(pkcs8)[:64]),
		"PEM armour line": []byte("PRIVATE KEY"),
	}
}

// everyStoredByte returns every value in every column of every table, as bytes.
func everyStoredByte(t *testing.T, db *gorm.DB) map[string][]byte {
	t.Helper()
	var tables []string
	require.NoError(t, db.Raw("SELECT name FROM sqlite_master WHERE type = 'table'").Scan(&tables).Error)
	require.Contains(t, tables, "signing_keys")
	require.Contains(t, tables, "secrets")
	out := map[string][]byte{}
	for _, table := range tables {
		rows, err := db.Raw(fmt.Sprintf("SELECT * FROM %q", table)).Rows()
		require.NoError(t, err)
		cols, err := rows.Columns()
		require.NoError(t, err)
		var buf bytes.Buffer
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			require.NoError(t, rows.Scan(ptrs...))
			for _, v := range vals {
				switch b := v.(type) {
				case []byte:
					buf.Write(b)
				case string:
					buf.WriteString(b)
				default:
					fmt.Fprint(&buf, b)
				}
				buf.WriteByte(0)
			}
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		out[table] = buf.Bytes()
	}
	return out
}

// requireNoPrivateKeyStored fails if any encoding of key appears anywhere in the
// database. It asserts on the VALUES stored, not on the absence of a column.
func requireNoPrivateKeyStored(t *testing.T, db *gorm.DB, key *rsa.PrivateKey) {
	t.Helper()
	encodings := privateKeyEncodings(t, key)
	for table, stored := range everyStoredByte(t, db) {
		for what, enc := range encodings {
			require.Falsef(t, bytes.Contains(stored, enc),
				"table %s holds the active signing key's private half (%s)", table, what)
		}
	}
}

// The scanner above is only worth something if it can FIND a key. This is its
// positive control: plant the PEM in a row and it must be reported.
func TestThePrivateKeyScannerFindsAPlantedKey(t *testing.T) {
	db := keysTestDB(t)
	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	pem, err := auth.EncodePrivateKeyPEM(key)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO signing_keys (created_at, updated_at, active, public_key_pem)
		VALUES (CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, false, ?)`, string(pem)).Error)

	found := false
	for _, stored := range everyStoredByte(t, db) {
		if bytes.Contains(stored, privateKeyEncodings(t, key)["PKCS#8 PEM"]) {
			found = true
		}
	}
	require.True(t, found, "the scanner must find a private key planted in a column")
}

// The first load mints a key, seals its private half as ONE instance secret named for
// the key's kid, and stores nothing in any table that could sign a token.
func TestFirstLoadSealsTheSigningKeyAndStoresNoPrivateHalf(t *testing.T) {
	db := keysTestDB(t)
	m, store := keysTestManager(t, db, rootKeyConfig(t))
	ctx := context.Background()

	set, err := m.loadSigningKeysLocked(ctx)
	require.NoError(t, err)
	require.NotNil(t, set.active)

	rows := storedSigningKeys(t, db)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Active)
	pub := rowPublicKey(t, rows[0])
	require.True(t, set.active.PublicKey.Equal(pub), "the active key must be the row's key")
	require.Len(t, set.publicKeys, 1)
	require.True(t, set.publicKeys[0].Equal(pub))

	kid := auth.Thumbprint(pub)
	require.Equal(t, []storedSecret{{TenantId: "", Scope: "instance", Name: "identity/signing-key/" + kid}},
		storedSecrets(t, db), "exactly one sealed private half, instance-scoped, named for the kid")

	sealed, err := store.Resolve(ctx, signingKeyRef(pub))
	require.NoError(t, err)
	resolved, err := auth.DecodePrivateKeyPEM(sealed)
	require.NoError(t, err)
	require.True(t, resolved.Equal(set.active), "the sealed private half must be the active key")

	requireNoPrivateKeyStored(t, db, set.active)

	// A second load opens the same key rather than minting another.
	again, err := m.loadSigningKeysLocked(ctx)
	require.NoError(t, err)
	require.True(t, again.active.Equal(set.active))
	require.Len(t, storedSigningKeys(t, db), 1)
	require.Len(t, storedSecrets(t, db), 1)
}

// A rotation deletes the retired key's private half — gone from the table, not soft-
// deleted — keeps its public half in the set so the tokens it signed still verify, and
// leaves the new key signing.
func TestRotationDeletesTheRetiredPrivateHalf(t *testing.T) {
	db := keysTestDB(t)
	cfg := rootKeyConfig(t)
	m, _ := keysTestManager(t, db, cfg)
	ctx := context.Background()

	first, err := m.loadSigningKeysLocked(ctx)
	require.NoError(t, err)
	oldKid := auth.Thumbprint(&first.active.PublicKey)

	rotated, err := m.rotateSigningKeyLocked(ctx, 0)
	require.NoError(t, err)
	require.False(t, rotated.active.Equal(first.active), "a rotation must mint a new key")
	newKid := auth.Thumbprint(&rotated.active.PublicKey)

	require.Equal(t, []storedSecret{{TenantId: "", Scope: "instance", Name: "identity/signing-key/" + newKid}},
		storedSecrets(t, db), "only the active key may have a private half; the retired one's envelope must be gone")

	rows := storedSigningKeys(t, db)
	require.Len(t, rows, 2)
	require.False(t, rows[0].Active)
	require.NotNil(t, rows[0].RetiredAt)
	require.Equal(t, oldKid, auth.Thumbprint(rowPublicKey(t, rows[0])))
	require.True(t, rows[1].Active)
	require.Equal(t, newKid, auth.Thumbprint(rowPublicKey(t, rows[1])))

	kids := map[string]bool{}
	for _, p := range rotated.publicKeys {
		kids[auth.Thumbprint(p)] = true
	}
	require.Equal(t, map[string]bool{oldKid: true, newKid: true}, kids,
		"the retired key's public half must stay in the set so its tokens verify")

	// The new key signs, and its published public half verifies.
	digest := sha256.Sum256([]byte("a token"))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rotated.active, crypto.SHA256, digest[:])
	require.NoError(t, err)
	require.NoError(t, rsa.VerifyPKCS1v15(rowPublicKey(t, rows[1]), crypto.SHA256, digest[:], sig))

	requireNoPrivateKeyStored(t, db, first.active)
	requireNoPrivateKeyStored(t, db, rotated.active)

	// A restart opens the rotated key from the store.
	restarted, _ := keysTestManager(t, db, cfg)
	loaded, err := restarted.loadSigningKeysLocked(ctx)
	require.NoError(t, err)
	require.True(t, loaded.active.Equal(rotated.active))
}

// failSigningKeyInserts makes every INSERT into signing_keys fail while armed, so a
// test can break a key write half way through its transaction.
func failSigningKeyInserts(t *testing.T, db *gorm.DB) *bool {
	t.Helper()
	armed := new(bool)
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:fail_signing_key_insert",
		func(tx *gorm.DB) {
			if *armed && tx.Statement.Table == "signing_keys" {
				_ = tx.AddError(errors.New("injected signing_keys insert failure"))
			}
		}))
	return armed
}

// The sealed private half and its row are ONE transaction. A first mint whose row
// insert fails must leave no sealed key behind: the store has no listing, so an orphan
// could never be found and deleted.
func TestAFailedFirstMintLeavesNoSealedKey(t *testing.T) {
	db := keysTestDB(t)
	armed := failSigningKeyInserts(t, db)
	m, _ := keysTestManager(t, db, rootKeyConfig(t))

	*armed = true
	_, err := m.loadSigningKeysLocked(context.Background())
	require.ErrorContains(t, err, "injected signing_keys insert failure")

	require.Empty(t, storedSigningKeys(t, db))
	require.Empty(t, storedSecrets(t, db), "the private half sealed before the failed insert must roll back with it")
}

// A rotation that fails part way leaves the key it was replacing exactly as it was:
// still active, private half still sealed — and no half-minted new key.
func TestAFailedRotationKeepsTheActiveKey(t *testing.T) {
	db := keysTestDB(t)
	armed := failSigningKeyInserts(t, db)
	m, store := keysTestManager(t, db, rootKeyConfig(t))
	ctx := context.Background()

	first, err := m.loadSigningKeysLocked(ctx)
	require.NoError(t, err)
	kid := auth.Thumbprint(&first.active.PublicKey)

	*armed = true
	_, err = m.rotateSigningKeyLocked(ctx, 0)
	require.ErrorContains(t, err, "injected signing_keys insert failure")

	rows := storedSigningKeys(t, db)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Active, "the demotion must roll back")
	require.Nil(t, rows[0].RetiredAt)
	require.Equal(t, []storedSecret{{TenantId: "", Scope: "instance", Name: "identity/signing-key/" + kid}},
		storedSecrets(t, db), "the old key's private half must survive and the new one must not exist")
	sealed, err := store.Resolve(ctx, signingKeyRef(&first.active.PublicKey))
	require.NoError(t, err)
	resolved, err := auth.DecodePrivateKeyPEM(sealed)
	require.NoError(t, err)
	require.True(t, resolved.Equal(first.active))
}

// An active key whose private half is not in the store is a loud, terminal error —
// never a silent re-mint, which would end every session and hide the cause.
func TestAnActiveKeyWithNoSealedHalfIsRefused(t *testing.T) {
	db := keysTestDB(t)
	cfg := rootKeyConfig(t)
	m, _ := keysTestManager(t, db, cfg)
	ctx := context.Background()

	first, err := m.loadSigningKeysLocked(ctx)
	require.NoError(t, err)
	require.NoError(t, db.Exec("DELETE FROM secrets").Error)

	// Through the real constructor again: with nothing stored, the self-test has
	// nothing to refuse on, so the refusal must come from the load itself.
	restarted, _ := keysTestManager(t, db, cfg)
	_, err = restarted.loadSigningKeysLocked(ctx)
	require.ErrorIs(t, err, errActiveKeyNotSealed)
	require.ErrorContains(t, err, auth.Thumbprint(&first.active.PublicKey), "the refusal must name the key")

	rows := storedSigningKeys(t, db)
	require.Len(t, rows, 1, "no replacement key may be minted")
	require.True(t, rowPublicKey(t, rows[0]).Equal(&first.active.PublicKey))
	require.Empty(t, storedSecrets(t, db))
}

// A different root key cannot start the service at all: the store's startup self-test
// finds the sealed signing key and cannot open it. That is where production refuses, so
// it is tested through the same constructor main uses.
func TestADifferentRootKeyIsRefusedAtStartup(t *testing.T) {
	db := keysTestDB(t)
	m, _ := keysTestManager(t, db, rootKeyConfig(t))
	_, err := m.loadSigningKeysLocked(context.Background())
	require.NoError(t, err)

	_, err = secrets.NewFromConfig(context.Background(), rootKeyConfig(t), db)
	require.ErrorContains(t, err, "does not open this service's stored secrets")
}

// A sealed half that belongs to a different key is refused rather than used to sign
// tokens nothing would verify.
func TestASealedHalfOfAnotherKeyIsRefused(t *testing.T) {
	db := keysTestDB(t)
	m, store := keysTestManager(t, db, rootKeyConfig(t))
	ctx := context.Background()

	first, err := m.loadSigningKeysLocked(ctx)
	require.NoError(t, err)
	other, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	otherPEM, err := auth.EncodePrivateKeyPEM(other)
	require.NoError(t, err)
	require.NoError(t, store.Put(ctx, signingKeyRef(&first.active.PublicKey), otherPEM))

	_, err = m.loadSigningKeysLocked(ctx)
	require.ErrorContains(t, err, "belongs to a different key")
	require.NotContains(t, err.Error(), "PRIVATE KEY", "an error must never carry key material")
}

// Without a secret store the keys cannot be sealed, and the Manager says so instead of
// falling back to anything.
func TestNoSecretStoreIsRefused(t *testing.T) {
	db := keysTestDB(t)
	m := NewManager(nil, &rdb.RdbManager{Database: db}, nil, nil, 0, 0, "", BootstrapConfig{}, nil)
	_, err := m.loadSigningKeysLocked(context.Background())
	require.ErrorContains(t, err, "no secret store")
	require.Empty(t, storedSigningKeys(t, db))
}

// signingKeyRef's name is the kid, and nothing else: the handle is derived from the
// public half, never stored.
func TestSigningKeyRefIsTheKid(t *testing.T) {
	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	ref := signingKeyRef(&key.PublicKey)
	require.Equal(t, secrets.ScopeInstance, ref.Scope)
	require.Empty(t, ref.Tenant)
	require.True(t, strings.HasPrefix(ref.Name, "identity/signing-key/"))
	require.Equal(t, auth.Thumbprint(&key.PublicKey), strings.TrimPrefix(ref.Name, "identity/signing-key/"))
	require.NoError(t, ref.Valid())
}

// The retention path: a rotation with a retention prunes a key retired before the
// cutoff, and only the active key has a private half throughout.
func TestRotationPrunesPastRetention(t *testing.T) {
	db := keysTestDB(t)
	m, _ := keysTestManager(t, db, rootKeyConfig(t))
	ctx := context.Background()

	_, err := m.loadSigningKeysLocked(ctx)
	require.NoError(t, err)
	_, err = m.rotateSigningKeyLocked(ctx, time.Hour)
	require.NoError(t, err)
	// Backdate the retired key past the retention window.
	require.NoError(t, db.Exec("UPDATE signing_keys SET retired_at = ? WHERE active = ?",
		time.Now().Add(-2*time.Hour), false).Error)
	third, err := m.rotateSigningKeyLocked(ctx, time.Hour)
	require.NoError(t, err)

	rows := storedSigningKeys(t, db)
	require.Len(t, rows, 2, "the key retired past retention is pruned; the one retired just now stays")
	require.Len(t, third.publicKeys, 2)
	require.Equal(t, []storedSecret{{TenantId: "", Scope: "instance",
		Name: "identity/signing-key/" + auth.Thumbprint(&third.active.PublicKey)}}, storedSecrets(t, db))
}
