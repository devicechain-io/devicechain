// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// Startup retry policy for infrastructure connects (Postgres, NATS stream/KV). A cluster restart or rollout often brings a microservice up a few
// seconds before its dependencies are reachable; without a retry the process
// exits and crash-loops, amplifying the outage. ~1 minute of bounded retry
// absorbs that lag and aligns with the auth gate's degrade-don't-die posture
// (ADR-022 review A6 / decision 3).
var (
	infraConnectAttempts = 30
	infraConnectDelay    = 2 * time.Second
)

// RetryInfraConnect runs op with bounded retries, logging each failed attempt.
// It returns nil on the first success, the context error immediately if ctx is
// cancelled (so shutdown is never blocked by the retry budget), or the last
// error wrapped after the attempts are exhausted. `what` names the dependency
// for log/error context.
func RetryInfraConnect(ctx context.Context, what string, op func(context.Context) error) error {
	var lastErr error
	for attempt := 1; attempt <= infraConnectAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := op(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < infraConnectAttempts {
			log.Warn().Err(lastErr).Str("dependency", what).Int("attempt", attempt).
				Msg("Infrastructure dependency not ready; retrying.")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(infraConnectDelay):
			}
		}
	}
	return fmt.Errorf("core: %s not reachable after %d attempts: %w", what, infraConnectAttempts, lastErr)
}
