// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

var errRollback = errors.New("roll this transaction back")

// A Put made through a transaction-bound view is undone when the caller's transaction
// rolls back. This is the property BindTx exists for: if the view's writes escaped the
// transaction, a caller that rolled back its own row would still leave the sealed
// value behind, with nothing referring to it and no listing to find it by.
func TestBindTxPutRollsBackWithTheCallersTransaction(t *testing.T) {
	db := newStoreDB(t)
	store := NewStore(db, newTestKP(t))
	ctx := context.Background()
	ref := instanceRef("identity/signing-key/rolled-back")

	err := db.WithContext(core.WithSystemContext(ctx)).Transaction(func(tx *gorm.DB) error {
		bound, err := BindTx(store, tx)
		if err != nil {
			return err
		}
		if err := bound.Put(ctx, ref, []byte("never committed")); err != nil {
			return err
		}
		// Inside the transaction the value is there — the rollback below is what
		// removes it, not a Put that silently did nothing.
		got, err := bound.Resolve(ctx, ref)
		if err != nil {
			return err
		}
		if string(got) != "never committed" {
			t.Fatalf("inside the transaction the bound view must read its own write, got %q", got)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("the transaction must end with the test's rollback, got %v", err)
	}

	if _, err := store.Resolve(ctx, ref); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("a Put inside a rolled-back transaction must leave nothing behind, got %v", err)
	}
	var rows int64
	if err := db.Raw("SELECT COUNT(*) FROM secrets").Scan(&rows).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("a rolled-back Put must leave no envelope row, found %d", rows)
	}
}

// The other direction, and the one that makes rollback meaningful: a Delete through a
// bound view that rolls back keeps the secret, and one that commits removes it.
func TestBindTxDeleteFollowsTheCallersTransaction(t *testing.T) {
	db := newStoreDB(t)
	store := NewStore(db, newTestKP(t))
	ctx := context.Background()
	ref := instanceRef("identity/signing-key/deleted")
	if err := store.Put(ctx, ref, []byte("sealed")); err != nil {
		t.Fatalf("put: %v", err)
	}

	deleteIn := func(finish error) error {
		return db.WithContext(core.WithSystemContext(ctx)).Transaction(func(tx *gorm.DB) error {
			bound, err := BindTx(store, tx)
			if err != nil {
				return err
			}
			if err := bound.Delete(ctx, ref); err != nil {
				return err
			}
			return finish
		})
	}

	if err := deleteIn(errRollback); !errors.Is(err, errRollback) {
		t.Fatalf("rollback: %v", err)
	}
	got, err := store.Resolve(ctx, ref)
	if err != nil || !bytes.Equal(got, []byte("sealed")) {
		t.Fatalf("a Delete inside a rolled-back transaction must keep the secret, got %q err=%v", got, err)
	}

	if err := deleteIn(nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := store.Resolve(ctx, ref); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("a committed Delete must remove the secret, got %v", err)
	}
}

// otherStore is a SecretStore that is not this package's Postgres store — the shape an
// external secret manager backend would have.
type otherStore struct{ SecretStore }

// A store whose values do not live in the database cannot join its transaction, and
// BindTx must say so rather than hand back something that runs outside it.
func TestBindTxRefusesAStoreThatCannotJoinATransaction(t *testing.T) {
	db := newStoreDB(t)
	bound, err := BindTx(otherStore{}, db)
	if err == nil {
		t.Fatalf("BindTx must refuse a store outside the database, got %#v", bound)
	}
	if !strings.Contains(err.Error(), "cannot join a database transaction") {
		t.Fatalf("the refusal must say why, got %q", err)
	}
	if _, err := BindTx(NewStore(db, newTestKP(t)), nil); err == nil {
		t.Fatal("BindTx must refuse a nil transaction handle")
	}
}

// NewFromConfig is the config-to-store wiring every secret-holding area uses, so what
// it reads out of the configuration is tested through it: the backend selection, the
// KEK provider selection and the root key must each reach New.
func TestNewFromConfigReadsEveryField(t *testing.T) {
	ctx := context.Background()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, dekSize))

	t.Run("backend", func(t *testing.T) {
		_, err := NewFromConfig(ctx, config.SecretsConfiguration{Backend: BackendVault, RootKey: key}, newStoreDB(t))
		if err == nil || !strings.Contains(err.Error(), BackendVault) {
			t.Fatalf("the configured backend must reach New and be refused as unbuilt, got %v", err)
		}
	})
	t.Run("kek provider", func(t *testing.T) {
		_, err := NewFromConfig(ctx, config.SecretsConfiguration{KEKProvider: KEKProviderAWSKMS, RootKey: key}, newStoreDB(t))
		if err == nil || !strings.Contains(err.Error(), KEKProviderAWSKMS) {
			t.Fatalf("the configured KEK provider must reach New and be refused as unbuilt, got %v", err)
		}
	})
	t.Run("missing root key", func(t *testing.T) {
		_, err := NewFromConfig(ctx, config.SecretsConfiguration{}, newStoreDB(t))
		if err == nil || !strings.Contains(err.Error(), "rootKey is not configured") {
			t.Fatalf("an absent root key must be refused, got %v", err)
		}
	})
	t.Run("root key", func(t *testing.T) {
		db := newStoreDB(t)
		store, err := NewFromConfig(ctx, config.SecretsConfiguration{RootKey: key}, db)
		if err != nil {
			t.Fatalf("a well-formed configuration must build: %v", err)
		}
		ref := instanceRef("probe")
		if err := store.Put(ctx, ref, []byte("v")); err != nil {
			t.Fatalf("put: %v", err)
		}
		// The key that was configured is the key that sealed it: a store built over
		// a DIFFERENT key must be refused by the startup self-test.
		other := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, dekSize))
		_, err = NewFromConfig(ctx, config.SecretsConfiguration{RootKey: other}, db)
		if err == nil || !strings.Contains(err.Error(), "does not open this service's stored secrets") {
			t.Fatalf("a store over a different root key must be refused, got %v", err)
		}
		if _, err := NewFromConfig(ctx, config.SecretsConfiguration{RootKey: key}, db); err != nil {
			t.Fatalf("the sealing key must still build: %v", err)
		}
	})
}

// NewSecretStoreSchemaAt records the ID it is given, and NewSecretStoreSchema keeps the
// ID three areas have already recorded — moving that one would make each of them run
// the migration again.
func TestSecretStoreSchemaIDs(t *testing.T) {
	if got := NewSecretStoreSchema().ID; got != "20260713120000" {
		t.Fatalf("the shared migration's ID is recorded in three areas and must not move, got %q", got)
	}
	if got := NewSecretStoreSchemaAt("20260923120000").ID; got != "20260923120000" {
		t.Fatalf("NewSecretStoreSchemaAt must use the ID it is given, got %q", got)
	}
}
