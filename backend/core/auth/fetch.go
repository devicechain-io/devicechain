// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/rsa"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/rs/zerolog/log"
)

// maxKeyDocBytes caps the JWKS response read so a misbehaving endpoint cannot
// exhaust memory. A handful of PKIX RSA-2048 JWKs is a few KiB.
const maxKeyDocBytes = 1 << 16

// Startup and refresh policy for fetching the platform JWKS.
const (
	// jwksFetchAttempts/Delay retry for ~1 minute at startup to absorb the race
	// where user-management has not finished starting.
	jwksFetchAttempts = 30
	jwksFetchDelay    = 2 * time.Second
	// jwksRefreshInterval throttles the on-demand refetch a validator does when
	// it sees an unknown kid, so tokens bearing bogus kids cannot turn into a
	// fetch storm against user-management.
	jwksRefreshInterval = 30 * time.Second
	jwksRequestTimeout  = 10 * time.Second
)

// NewValidatorForInstance builds a Validator from the user-management JWKS
// endpoint described by the instance configuration. This is the one place the
// JWKS URL convention and retry policy live, so every consuming service wires
// JWT validation with a single call. The returned validator refetches the JWKS
// on an unknown kid, so a signing-key rotation propagates without a restart.
func NewValidatorForInstance(ctx context.Context, cfg config.UserManagementConfiguration) (*Validator, error) {
	url := fmt.Sprintf("http://%s:%d/auth/jwks", cfg.Hostname, cfg.Port)
	return NewValidatorFromJWKSURL(ctx, url, jwksFetchAttempts, jwksFetchDelay)
}

// NewValidatorFromJWKSURL fetches the platform JWKS from user-management and
// returns a Validator that verifies tokens locally thereafter. It retries to
// absorb the startup race where user-management is not yet serving, and on an
// unknown kid refetches the JWKS once (throttled) to pick up a rotated-in key.
func NewValidatorFromJWKSURL(ctx context.Context, url string, attempts int, delay time.Duration) (*Validator, error) {
	if attempts < 1 {
		attempts = 1
	}
	client := &http.Client{Timeout: jwksRequestTimeout}

	// The lazy-refresh fetch uses a fresh background context: by the time an
	// unknown kid triggers it, the startup ctx is long done.
	refresh := func() (map[string]*rsa.PublicKey, error) {
		return fetchJWKSKeys(context.Background(), client, url)
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		keys, err := fetchJWKSKeys(ctx, client, url)
		if err == nil {
			log.Info().Str("url", url).Int("keys", len(keys)).Msg("Fetched platform JWKS for JWT validation.")
			return NewRefreshingValidator(keys, refresh, jwksRefreshInterval), nil
		}
		lastErr = err
		if attempt < attempts {
			log.Warn().Err(err).Str("url", url).Int("attempt", attempt).
				Msg("JWKS not yet available; retrying.")
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return nil, fmt.Errorf("auth: failed to fetch JWKS from %s after %d attempts: %w", url, attempts, lastErr)
}

func fetchJWKSKeys(ctx context.Context, client *http.Client, url string) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: JWKS endpoint returned status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxKeyDocBytes))
	if err != nil {
		return nil, err
	}
	set, err := ParseJWKS(data)
	if err != nil {
		return nil, err
	}
	return set.keyMap()
}
