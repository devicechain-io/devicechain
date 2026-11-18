// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"

	"github.com/rs/zerolog"
)

// MaxInstanceIdLen is the maximum canonical instance id length, as in the chart schema.
const MaxInstanceIdLen = 50

// CheckInstanceIdGrammar checks the canonical instance grammar enforced by the
// chart and dcctl: 1..50 characters matching ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$.
// Underscore separates the instance from derived stream and bucket names, so an
// underscore in the id makes its namespace ambiguous: foo's advisory prefix
// foo_ also matches foo_bar's streams. Instance ids also name shared databases
// and logins, where this grammar keeps them clear of reserved role namespaces.
// Services only warn on this check, after ValidateToken's safety gate, because
// previously accepted ids must keep starting through rolling upgrades.
func CheckInstanceIdGrammar(id string) error {
	if len(id) == 0 {
		return fmt.Errorf("instance id must not be empty")
	}
	if len(id) > MaxInstanceIdLen {
		return fmt.Errorf("instance id is too long: maximum length is %d", MaxInstanceIdLen)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '_':
			return fmt.Errorf("instance id contains an underscore, reserved as a namespace separator")
		case c >= 'A' && c <= 'Z':
			return fmt.Errorf("instance id contains an uppercase letter; only lowercase letters are allowed")
		case c == '-':
			if i == 0 || i == len(id)-1 {
				return fmt.Errorf("instance id has a leading/trailing hyphen")
			}
		default:
			return fmt.Errorf("instance id contains another character class (%q); only lowercase ASCII letters, digits and interior hyphens are allowed", c)
		}
	}
	return nil
}

// reportInstanceIdGrammar logs the advisory to logger and sets the gauge. The logger is a
// parameter so a test can observe the warning without reassigning the global logger.
func (ms *Microservice) reportInstanceIdGrammar(logger *zerolog.Logger) {
	err := CheckInstanceIdGrammar(ms.InstanceId)
	if err != nil {
		logger.Warn().Err(err).Str("instanceId", ms.InstanceId).Msg("Non-canonical instance id accepted for compatibility; names derived from it can collide with another instance's on a shared broker or database. The chart and dcctl refuse such ids; recreate under a canonical id")
	}
	// NewMicroservice attaches its registry before startup; struct-literal callers
	// may attach theirs with UseMetricsRegistry before InitializeAndStart.
	ms.instanceIdGrammarOnce.Do(func() {
		ms.instanceIdNoncanonical = ms.NewGauge("instance_id_noncanonical", "Whether the instance id is non-canonical (1) or canonical (0).")
	})
	value := float64(0)
	if err != nil {
		value = 1
	}
	ms.instanceIdNoncanonical.Set(value)
}
