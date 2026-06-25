// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/rsa"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
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

// loadSigningKeys returns the instance signing-key set, generating and persisting
// the first key the very first time. The work runs under a distributed lock so
// concurrent replicas converge on a single keypair (ADR-008). SigningKey is not
// tenant-scoped, so these reads/writes need no tenant in context.
func (m *Manager) loadSigningKeys(ctx context.Context) (*signingKeySet, error) {
	var set *signingKeySet
	err := m.locker.WithLock(ctx, m.ms.FunctionalArea, func(ctx context.Context) error {
		db := m.db.DB(ctx)
		var current model.SigningKey
		err := db.Where("active = ?", true).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			created, cerr := m.createSigningKey(db)
			if cerr != nil {
				return cerr
			}
			current = *created
		} else if err != nil {
			return err
		}

		loaded, lerr := m.readKeySet(db, &current)
		if lerr != nil {
			return lerr
		}
		set = loaded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return set, nil
}

// readKeySet parses the active private key and loads every retained key's public
// half. The caller supplies the active row (already loaded or just created) and
// the db handle (which may be a transaction).
func (m *Manager) readKeySet(db *gorm.DB, current *model.SigningKey) (*signingKeySet, error) {
	priv, err := auth.DecodePrivateKeyPEM([]byte(current.PrivateKeyPem))
	if err != nil {
		return nil, err
	}

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
	return &signingKeySet{active: priv, publicKeys: publics}, nil
}

// createSigningKey generates a fresh RSA keypair and persists it as the active
// key on db (which may be a transaction). Used both for the first-ever key and
// for each rotation.
func (m *Manager) createSigningKey(db *gorm.DB) (*model.SigningKey, error) {
	generated, err := auth.GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	privPEM, err := auth.EncodePrivateKeyPEM(generated)
	if err != nil {
		return nil, err
	}
	pubPEM, err := auth.EncodePublicKeyPEM(&generated.PublicKey)
	if err != nil {
		return nil, err
	}
	record := model.SigningKey{Active: true, PrivateKeyPem: string(privPEM), PublicKeyPem: string(pubPEM)}
	if err := db.Create(&record).Error; err != nil {
		return nil, err
	}
	log.Info().Msg("Generated and persisted an instance JWT signing key.")
	return &record, nil
}

// rotateSigningKey demotes the current active key (Active=false, RetiredAt=now),
// mints a new active key, and hard-deletes keys retired longer ago than retention
// (no live token can still reference them). The demote+create+prune run in one
// DB transaction so a partial failure can never leave zero or two active keys,
// and the whole thing runs under the distributed lock so it happens once across
// replicas. retention <= 0 keeps retired keys indefinitely.
func (m *Manager) rotateSigningKey(ctx context.Context, retention time.Duration) (*signingKeySet, error) {
	var set *signingKeySet
	err := m.locker.WithLock(ctx, m.ms.FunctionalArea, func(ctx context.Context) error {
		return m.db.DB(ctx).Transaction(func(tx *gorm.DB) error {
			now := time.Now()
			if err := tx.Model(&model.SigningKey{}).Where("active = ?", true).
				Updates(map[string]any{"active": false, "retired_at": now}).Error; err != nil {
				return err
			}
			current, err := m.createSigningKey(tx)
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
			loaded, lerr := m.readKeySet(tx, current)
			if lerr != nil {
				return lerr
			}
			set = loaded
			log.Warn().Msg("Rotated the instance JWT signing key.")
			return nil
		})
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
	if err := m.db.DB(ctx).Where("active = ?", true).First(&current).Error; err != nil {
		return 0, err
	}
	return time.Since(current.CreatedAt), nil
}
