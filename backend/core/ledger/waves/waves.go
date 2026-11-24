// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package waves provides pure cohort validation and frozen-fleet assignment.
// It has no storage, execution or served surface.
package waves

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
)

// Cohort selects explicit canaries or a cumulative percentage of the entire
// frozen fleet. Promotion policy belongs to the caller and does not affect math.
type Cohort struct {
	Name         string
	DeviceTokens []string
	Percentage   int32
}

// Validate checks the plan independently of fleet membership. An absent plan is
// allowed. Otherwise canaries must precede increasing percentages ending at 100.
// Limits match the update rollout contract: 20 cohorts, 128-byte names and at
// most 10,000 canaries per cohort, with the shared entity-token grammar.
func Validate(plan []Cohort) error {
	if len(plan) > 20 {
		return fmt.Errorf("at most 20 cohorts are allowed")
	}
	names, devices := map[string]bool{}, map[string]bool{}
	lastPercentage := int32(0)
	for _, c := range plan {
		if c.Name == "" || len(c.Name) > 128 || names[c.Name] || (len(c.DeviceTokens) == 0) == (c.Percentage == 0) {
			return fmt.Errorf("cohorts need unique names and exactly one selector")
		}
		names[c.Name] = true
		if len(c.DeviceTokens) > 10_000 {
			return fmt.Errorf("too many canary devices")
		}
		for _, d := range c.DeviceTokens {
			if core.ValidateToken(d) != nil || devices[d] || lastPercentage != 0 {
				return fmt.Errorf("canaries must be unique and precede percentage cohorts")
			}
			devices[d] = true
		}
		if c.Percentage != 0 {
			if c.Percentage <= lastPercentage || c.Percentage > 100 {
				return fmt.Errorf("percentages must increase through 100")
			}
			lastPercentage = c.Percentage
		}
	}
	if len(plan) > 0 && lastPercentage != 100 {
		return fmt.Errorf("the last cohort must cover 100 percent")
	}
	return nil
}

// Assign freezes zero-based cohort indexes for a plan that has passed Validate
// and an ordered, unique fleet. Percentages round up cumulatively, counting prior
// canaries toward the limit. Remaining devices retain fleet order; inputs are
// never modified. An outside canary or empty cohort refuses the whole assignment.
// An absent plan returns an empty map; callers may use implicit cohort zero.
func Assign(plan []Cohort, devices []string) (map[string]int, error) {
	out := map[string]int{}
	members := map[string]bool{}
	for _, d := range devices {
		members[d] = true
	}
	for i, c := range plan {
		before := len(out)
		for _, d := range c.DeviceTokens {
			if !members[d] {
				return nil, fmt.Errorf("canary %q is outside the frozen fleet", d)
			}
			out[d] = i
		}
		if c.Percentage > 0 {
			limit := (len(devices)*int(c.Percentage) + 99) / 100
			for _, d := range devices {
				if len(out) >= limit {
					break
				}
				if _, assigned := out[d]; !assigned {
					out[d] = i
				}
			}
		}
		if len(out) == before {
			return nil, fmt.Errorf("cohort %q is empty for this fleet", c.Name)
		}
	}
	return out, nil
}
