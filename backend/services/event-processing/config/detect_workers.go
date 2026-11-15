// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
)

// MaxDetectWorkers retains the existing process maximum of 64 DETECT shards.
// The group limit is 16, so a full 16-group instance can run at most K=4.
// Multiplying both maxima (16*64) would silently multiply the old resource budget.
const MaxDetectWorkers = detectcore.MaxShards

// ErrDetectWorkerCeiling identifies a refused process worker budget at startup.
var ErrDetectWorkerCeiling = errors.New("DETECT worker ceiling exceeded")

// ValidateDetectWorkers is used before starting workers, including the internal
// group assembly path. Zero shards means the legacy default of one.
func ValidateDetectWorkers(groups, shards int) error {
	if groups < 1 || groups > 16 || shards < 0 || shards > detectcore.MaxShards || groups > MaxDetectWorkers/max(shards, 1) {
		return fmt.Errorf("%w: groups=%d detectShards=%d; maximum N*K=%d", ErrDetectWorkerCeiling, groups, shards, MaxDetectWorkers)
	}
	return nil
}
