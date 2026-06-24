// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/rs/zerolog/log"
)

// maxPublicKeyBytes caps the public-key response read so a misbehaving endpoint
// cannot exhaust memory. A PKIX RSA-2048 PEM is well under 1 KiB.
const maxPublicKeyBytes = 1 << 16

// Standard startup fetch policy for the platform public key: retry for ~1 minute
// to absorb the race where user-management has not finished starting.
const (
	publicKeyFetchAttempts = 30
	publicKeyFetchDelay    = 2 * time.Second
)

// NewValidatorForInstance builds a Validator from the user-management public-key
// endpoint described by the instance configuration. This is the one place the
// public-key URL convention and retry policy live, so every consuming service
// wires JWT validation with a single call.
func NewValidatorForInstance(ctx context.Context, cfg config.UserManagementConfiguration) (*Validator, error) {
	url := fmt.Sprintf("http://%s:%d/auth/public-key", cfg.Hostname, cfg.Port)
	return NewValidatorFromURL(ctx, url, publicKeyFetchAttempts, publicKeyFetchDelay)
}

// NewValidatorFromURL fetches the platform public key (PKIX PEM) from the
// user-management public-key endpoint and returns a Validator. The signing key
// is issued by user-management (ADR-008); every other service fetches it once
// at startup, so verification thereafter is purely local. It retries to absorb
// the startup race where user-management is not yet serving.
func NewValidatorFromURL(ctx context.Context, url string, attempts int, delay time.Duration) (*Validator, error) {
	if attempts < 1 {
		attempts = 1
	}
	client := &http.Client{Timeout: 10 * time.Second}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		pem, err := fetchPublicKeyPEM(ctx, client, url)
		if err == nil {
			validator, perr := NewValidatorFromPEM(pem)
			if perr == nil {
				log.Info().Str("url", url).Msg("Fetched platform public key for JWT validation.")
				return validator, nil
			}
			err = perr
		}
		lastErr = err
		if attempt < attempts {
			log.Warn().Err(err).Str("url", url).Int("attempt", attempt).
				Msg("Public key not yet available; retrying.")
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return nil, fmt.Errorf("auth: failed to fetch public key from %s after %d attempts: %w", url, attempts, lastErr)
}

func fetchPublicKeyPEM(ctx context.Context, client *http.Client, url string) ([]byte, error) {
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
		return nil, fmt.Errorf("auth: public key endpoint returned status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxPublicKeyBytes))
}
