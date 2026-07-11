// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashToken renders a stable, grammar-safe idempotency token from arbitrary input. A hex SHA-256
// is 64 chars of [0-9a-f] with a leading alphanumeric, so it satisfies the ADR-042 token grammar
// (^[A-Za-z0-9][A-Za-z0-9_-]*$, <= core.MaxTokenLen=128) by construction — no sanitization needed.
// It is used for the command idempotency token, which command-delivery stores as a per-tenant
// unique command token and dedups redeliveries on.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
