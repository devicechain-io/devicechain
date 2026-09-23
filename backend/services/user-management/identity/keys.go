// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-user-management/model"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// signingKeySet is the in-memory result of loading the instance signing keys: the
// active private key (the one signing new tokens) and the public halves of every
// retained key (active plus not-yet-pruned rotated-out keys, for the JWKS and the
// validator).
type signingKeySet struct {
	active     *rsa.PrivateKey
	publicKeys []*rsa.PublicKey
}

// signingKeySecretPrefix names the secret-store handles of signing keys' private
// halves. The rest of the name is the key's RFC 7638 thumbprint (its kid).
const signingKeySecretPrefix = "identity/signing-key/"

// signingKeyRef is the secret-store handle of the private half of the key whose
// public half is pub.
//
// It is DERIVED, not stored, for the same reason the kid is: the public half is the
// key's identity, so a row can name its private half without a column that could be
// pointed anywhere else — and the store binds the sealed value to this name as
// authenticated data, so an envelope moved under another key's handle does not open.
// It is instance-scoped: one keypair serves the whole instance.
func signingKeyRef(pub *rsa.PublicKey) secrets.SecretRef {
	return secrets.SecretRef{Scope: secrets.ScopeInstance, Name: signingKeySecretPrefix + auth.Thumbprint(pub)}
}

// errActiveKeyNotSealed is the refusal for an active signing key whose private half is
// not in the secret store. It is terminal and deliberately does not mint a replacement:
// a new key would silently end every session in the instance and hide whatever removed
// the old one's private half (a partial restore, a hand-edited table).
var errActiveKeyNotSealed = errors.New("the active JWT signing key has no sealed private half in the secret store")

// loadSigningKeys returns the instance signing-key set, generating and persisting
// the first key the very first time. The work runs under a distributed lock so
// concurrent replicas converge on a single keypair (ADR-008).
func (m *Manager) loadSigningKeys(ctx context.Context) (*signingKeySet, error) {
	var set *signingKeySet
	err := m.locker.WithLock(ctx, m.ms.FunctionalArea, func(ctx context.Context) error {
		loaded, err := m.loadSigningKeysLocked(ctx)
		set = loaded
		return err
	})
	if err != nil {
		return nil, err
	}
	return set, nil
}

// loadSigningKeysLocked is loadSigningKeys' body, run by a caller that already holds
// the lock (or, in a test, needs none).
//
// It runs in ONE transaction that the secret store joins, so a first-ever key is
// sealed and recorded together or not at all: there is no crash window that leaves a
// sealed private key no row refers to, or a row whose private half was never sealed.
//
// SigningKey and instance secrets are instance-global (not tenant-scoped), so the work
// runs under the sanctioned system context — the fail-closed tenant guard (ADR-015)
// rejects any write with neither a tenant nor the system context, even on exempt
// models.
func (m *Manager) loadSigningKeysLocked(ctx context.Context) (*signingKeySet, error) {
	var set *signingKeySet
	err := m.db.DB(core.WithSystemContext(ctx)).Transaction(func(tx *gorm.DB) error {
		store, err := m.sealedIn(tx)
		if err != nil {
			return err
		}
		var current model.SigningKey
		err = tx.Where("active = ?", true).First(&current).Error
		var active *rsa.PrivateKey
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			active, err = m.mintSigningKey(ctx, tx, store)
		case err == nil:
			active, err = openPrivateHalf(ctx, store, &current)
		}
		if err != nil {
			return err
		}
		set, err = readKeySet(tx, active)
		return err
	})
	if err != nil {
		return nil, err
	}
	return set, nil
}

// sealedIn is the secret store bound to tx, so the sealed private halves commit and
// roll back with the signing_keys rows that name them.
func (m *Manager) sealedIn(tx *gorm.DB) (secrets.SecretStore, error) {
	if m.secrets == nil {
		return nil, errors.New("identity: no secret store is configured, so the JWT signing keys cannot be sealed")
	}
	return secrets.BindTx(m.secrets, tx)
}

// openPrivateHalf resolves and checks the active key's sealed private half. Every
// error names the key by kid and never carries key material.
func openPrivateHalf(ctx context.Context, store secrets.SecretStore, row *model.SigningKey) (*rsa.PrivateKey, error) {
	pub, err := auth.DecodePublicKeyPEM([]byte(row.PublicKeyPem))
	if err != nil {
		return nil, fmt.Errorf("the active JWT signing key's public half does not parse: %w", err)
	}
	kid := auth.Thumbprint(pub)
	sealed, err := store.Resolve(ctx, signingKeyRef(pub))
	if errors.Is(err, secrets.ErrSecretNotFound) {
		return nil, fmt.Errorf("%w (kid %s). A replacement is not minted, because that would silently end every "+
			"session in the instance and hide whatever removed it. To start over with a fresh key — every user "+
			"signs in again — delete this area's signing_keys rows and restart", errActiveKeyNotSealed, kid)
	}
	if err != nil {
		return nil, fmt.Errorf("opening the sealed private half of the active JWT signing key (kid %s): %w", kid, err)
	}
	priv, err := auth.DecodePrivateKeyPEM(sealed)
	zero(sealed)
	if err != nil {
		return nil, fmt.Errorf("the sealed private half of the active JWT signing key (kid %s) does not parse: %w", kid, err)
	}
	// A private half sealed under this handle that belongs to a different key cannot
	// sign anything the JWKS would verify. The handle's AAD binding makes this all but
	// impossible to reach; checking costs one comparison and turns "every token fails
	// validation" into a refusal that names the cause.
	if !priv.PublicKey.Equal(pub) {
		return nil, fmt.Errorf("the sealed private half under the active JWT signing key's handle (kid %s) belongs to a different key", kid)
	}
	return priv, nil
}

// readKeySet loads every retained key's public half and pairs it with the active
// private key. db is the caller's transaction.
func readKeySet(db *gorm.DB, active *rsa.PrivateKey) (*signingKeySet, error) {
	var all []model.SigningKey
	if err := db.Find(&all).Error; err != nil {
		return nil, err
	}
	publics := make([]*rsa.PublicKey, 0, len(all))
	for _, k := range all {
		pub, derr := auth.DecodePublicKeyPEM([]byte(k.PublicKeyPem))
		if derr != nil {
			return nil, derr
		}
		publics = append(publics, pub)
	}
	return &signingKeySet{active: active, publicKeys: publics}, nil
}

// mintSigningKey generates a fresh RSA keypair, seals its private half in the secret
// store and records its public half as the active key. tx is the caller's transaction
// and store is bound to it, so the two writes commit together. Used both for the
// first-ever key and for each rotation.
func (m *Manager) mintSigningKey(ctx context.Context, tx *gorm.DB, store secrets.SecretStore) (*rsa.PrivateKey, error) {
	generated, err := auth.GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	privPEM, err := auth.EncodePrivateKeyPEM(generated)
	if err != nil {
		return nil, err
	}
	err = store.Put(ctx, signingKeyRef(&generated.PublicKey), privPEM)
	zero(privPEM)
	if err != nil {
		return nil, fmt.Errorf("sealing the new JWT signing key's private half: %w", err)
	}
	pubPEM, err := auth.EncodePublicKeyPEM(&generated.PublicKey)
	if err != nil {
		return nil, err
	}
	if err := tx.Create(&model.SigningKey{Active: true, PublicKeyPem: string(pubPEM)}).Error; err != nil {
		return nil, err
	}
	log.Info().Str("kid", auth.Thumbprint(&generated.PublicKey)).
		Msg("Generated an instance JWT signing key; its private half is sealed in the secret store.")
	return generated, nil
}

// rotateSigningKey demotes the current active key (Active=false, RetiredAt=now),
// deletes its sealed private half, mints a new active key, and hard-deletes keys
// retired longer ago than retention (no live token can still reference them). All of
// it runs in one DB transaction that the secret store joins, so a partial failure can
// never leave zero or two active keys, a retired key whose private half is still
// stored, or an active key with no private half; and the whole thing runs under the
// distributed lock so it happens once across replicas. That is a claim about the
// DATABASE: another replica already running keeps the demoted key in memory and signs
// with it until it restarts, because nothing tells it the key was rotated. retention <= 0 keeps retired keys'
// PUBLIC halves indefinitely; their private halves are gone at demotion regardless.
func (m *Manager) rotateSigningKey(ctx context.Context, retention time.Duration) (*signingKeySet, error) {
	var set *signingKeySet
	err := m.locker.WithLock(ctx, m.ms.FunctionalArea, func(ctx context.Context) error {
		rotated, err := m.rotateSigningKeyLocked(ctx, retention)
		set = rotated
		return err
	})
	if err != nil {
		return nil, err
	}
	return set, nil
}

// rotateSigningKeyLocked is rotateSigningKey's body, run by a caller that already
// holds the lock (or, in a test, needs none).
func (m *Manager) rotateSigningKeyLocked(ctx context.Context, retention time.Duration) (*signingKeySet, error) {
	var set *signingKeySet
	err := m.db.DB(core.WithSystemContext(ctx)).Transaction(func(tx *gorm.DB) error {
		store, err := m.sealedIn(tx)
		if err != nil {
			return err
		}
		var demoted []model.SigningKey
		if err := tx.Where("active = ?", true).Find(&demoted).Error; err != nil {
			return err
		}
		now := time.Now()
		if err := tx.Model(&model.SigningKey{}).Where("active = ?", true).
			Updates(map[string]any{"active": false, "retired_at": now}).Error; err != nil {
			return err
		}
		// A retired key is only meant to verify (a replica still running with it in
		// memory signs until it restarts; nothing here reaches that copy), so its
		// stored private half has no further use — and kept, it is a key that can still mint tokens every service accepts for as
		// long as the public half is served.
		for _, k := range demoted {
			pub, err := auth.DecodePublicKeyPEM([]byte(k.PublicKeyPem))
			if err != nil {
				return fmt.Errorf("the JWT signing key being retired has a public half that does not parse: %w", err)
			}
			if err := store.Delete(ctx, signingKeyRef(pub)); err != nil {
				return fmt.Errorf("deleting the retired JWT signing key's private half (kid %s): %w", auth.Thumbprint(pub), err)
			}
		}
		active, err := m.mintSigningKey(ctx, tx, store)
		if err != nil {
			return err
		}
		if retention > 0 {
			cutoff := now.Add(-retention)
			if err := tx.Unscoped().
				Where("active = ? AND retired_at IS NOT NULL AND retired_at < ?", false, cutoff).
				Delete(&model.SigningKey{}).Error; err != nil {
				return err
			}
		}
		loaded, err := readKeySet(tx, active)
		if err != nil {
			return err
		}
		set = loaded
		log.Warn().Msg("Rotated the instance JWT signing key; the retired key's private half was deleted.")
		return nil
	})
	if err != nil {
		return nil, err
	}
	return set, nil
}

// activeKeyAge reports how long the current active signing key has existed, used
// by the age-based auto-rotation check.
func (m *Manager) activeKeyAge(ctx context.Context) (time.Duration, error) {
	var current model.SigningKey
	if err := m.db.DB(core.WithSystemContext(ctx)).Where("active = ?", true).First(&current).Error; err != nil {
		return 0, err
	}
	return time.Since(current.CreatedAt), nil
}

// zero overwrites key material this process no longer needs, best effort: Go may
// have copied it elsewhere, but the buffer handed back by the store or the encoder is
// not left holding it.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
