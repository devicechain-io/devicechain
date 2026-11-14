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
	"github.com/devicechain-io/dc-user-management/model"
	"github.com/rs/zerolog/log"
)

const (
	signingKeyFollowMinInterval = 2 * time.Second
	signingKeyFollowInterval    = 30 * time.Second
	signingKeyStartupGrace      = 15 * time.Minute
)

// errSigningKeyChanged asks a follower to retry after a rotation committed between
// its row snapshot and its secret resolve. It does not diagnose a broken seal.
var errSigningKeyChanged = errors.New("the active signing key changed while following; retry")

// followSigningKeys shares one minimum interval across tokens, JWKS and the periodic
// pass. It only reads: a missing or corrupt row must never trigger a mint or prune.
func (m *Manager) followSigningKeys(ctx context.Context) (changed bool, err error) {
	m.followMu.Lock()
	defer m.followMu.Unlock()
	now := m.clock()
	if !m.lastFollowAttempt.IsZero() && now.Sub(m.lastFollowAttempt) < signingKeyFollowMinInterval {
		return false, nil
	}
	m.lastFollowAttempt = now
	missingKid := ""
	defer func() {
		if err != nil {
			if m.followFailures != nil {
				m.followFailures.Inc()
			}
			if missingKid != "" && missingKid != m.lastMissingKid {
				log.Warn().Err(err).Msg("Signing-key follow raced a rotation; keeping the last known keys.")
			} else {
				log.Error().Err(err).Msg("Signing-key follow failed; keeping the last known keys.")
			}
		}
		m.lastMissingKid = missingKid
	}()
	var rows []model.SigningKey
	if err := m.db.DB(core.WithSystemContext(ctx)).Find(&rows).Error; err != nil {
		return false, err
	}
	publics := make([]*rsa.PublicKey, 0, len(rows))
	keyMap := make(map[string]*rsa.PublicKey, len(rows))
	var active *model.SigningKey
	activeKid := ""
	for i := range rows {
		pub, err := auth.DecodePublicKeyPEM([]byte(rows[i].PublicKeyPem))
		if err != nil {
			return false, fmt.Errorf("parsing a signing-key public half: %w", err)
		}
		kid := auth.Thumbprint(pub)
		publics = append(publics, pub)
		keyMap[kid] = pub
		if rows[i].Active {
			if active != nil {
				return false, errors.New("multiple active signing keys")
			}
			active, activeKid = &rows[i], kid
		}
	}
	if active == nil {
		return false, errors.New("no active signing key to follow")
	}
	m.mu.RLock()
	same := activeKid == m.activeKid && len(keyMap) == len(m.publicKeys)
	if same {
		for _, pub := range m.publicKeys {
			if keyMap[auth.Thumbprint(pub)] == nil {
				same = false
				break
			}
		}
	}
	m.mu.RUnlock()
	if same {
		return false, nil
	}
	if m.secrets == nil {
		return false, errors.New("no signing-key secret store")
	}
	priv, err := openPrivateHalf(ctx, m.secrets, active)
	if errors.Is(err, errActiveKeyNotSealed) {
		missingKid = activeKid
		return false, fmt.Errorf("%w (kid %s)", errSigningKeyChanged, activeKid)
	}
	if err != nil {
		return false, err
	}
	m.applyKeys(&signingKeySet{active: priv, publicKeys: publics})
	return true, nil
}

// refreshSigningKeys is the validator's unknown-kid refresh. It returns the set this
// manager holds NOW, whether or not the follow changed it, so a call always reinstalls
// the current set. That is what bounds the one race it has: the validator installs the
// returned map after this returns, so an applyKeys that lands in between (a peer's
// rotation adopted by the periodic follow) is overwritten by the older copy. The next
// token carrying the new kid misses, and the refresh after the validator's minimum
// interval reinstalls the current set; JWKS and signing read the manager's own fields and
// are never affected.
func (m *Manager) refreshSigningKeys() (map[string]*rsa.PublicKey, error) {
	if _, err := m.followSigningKeys(context.Background()); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make(map[string]*rsa.PublicKey, len(m.publicKeys))
	for _, pub := range m.publicKeys {
		keys[auth.Thumbprint(pub)] = pub
	}
	return keys, nil
}

// signingKeyFollowTick leaves older replicas time to finish a rolling upgrade
// before age-based rotation starts. The manager's clock and start time are injectable.
func (m *Manager) signingKeyFollowTick(ctx context.Context, maxAge, retention time.Duration) error {
	_, err := m.followSigningKeys(ctx)
	if errors.Is(err, errSigningKeyChanged) {
		// The follower already logged this miss at the appropriate level. Retry
		// on the next pass instead of having the periodic task log it as an error.
		return nil
	}
	if err != nil {
		return err
	}
	if m.clock().Sub(m.startedAt) < signingKeyStartupGrace {
		return nil
	}
	return m.MaybeRotateOnAge(ctx, maxAge, retention)
}

// NewSigningKeyFollowTask follows even when automatic rotation is disabled.
func (m *Manager) NewSigningKeyFollowTask(maxAge, retention time.Duration) *core.PeriodicTask {
	m.followFailures = m.ms.NewCounter("signing_key_follow_failures_total", "Failed attempts to follow the instance signing keys.")
	return core.NewPeriodicTask(m.ms.FunctionalArea, "signing-key-follow", signingKeyFollowInterval,
		func(ctx context.Context) error { return m.signingKeyFollowTick(ctx, maxAge, retention) },
		core.NewNoOpLifecycleCallbacks())
}
