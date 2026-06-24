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

// loadOrCreateSigningKey returns the instance-global RSA signing key, generating
// and persisting one the first time. The work runs under a distributed lock so
// concurrent replicas converge on a single keypair (ADR-008). SigningKey is not
// tenant-scoped, so these reads/writes need no tenant in context.
func (m *Manager) loadOrCreateSigningKey(ctx context.Context) (*rsa.PrivateKey, []byte, error) {
	var (
		priv   *rsa.PrivateKey
		pubPEM []byte
	)
	err := m.ms.WithDistributedLock(ctx, 5*time.Second, 5, func(ctx context.Context) error {
		var existing model.SigningKey
		err := m.db.DB(ctx).Where("active = ?", true).First(&existing).Error
		if err == nil {
			parsed, derr := auth.DecodePrivateKeyPEM([]byte(existing.PrivateKeyPem))
			if derr != nil {
				return derr
			}
			priv = parsed
			pubPEM = []byte(existing.PublicKeyPem)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		// No key yet: generate and persist one.
		generated, gerr := auth.GenerateKeyPair()
		if gerr != nil {
			return gerr
		}
		privPEM, perr := auth.EncodePrivateKeyPEM(generated)
		if perr != nil {
			return perr
		}
		pub, perr := auth.EncodePublicKeyPEM(&generated.PublicKey)
		if perr != nil {
			return perr
		}
		record := model.SigningKey{Active: true, PrivateKeyPem: string(privPEM), PublicKeyPem: string(pub)}
		if cerr := m.db.DB(ctx).Create(&record).Error; cerr != nil {
			return cerr
		}
		log.Info().Msg("Generated and persisted a new instance JWT signing key.")
		priv = generated
		pubPEM = pub
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return priv, pubPEM, nil
}
