// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// A tenant token is held for at least 12 hours after the tenant is deleted (see the
// tenant-deletion page), so the hosted playground can never reset by deleting its
// tenants and creating them again under the same name: the second create is refused. It
// resets by ROTATION instead. The playground owns N slots; at each reset it creates the
// scenarios in the slot that is current for the new day, then destroys every other slot.
// A slot is destroyed one reset after it was last used and recreated N-1 resets later, so
// with the minimum of three slots a token has been free for two full days before it is
// asked for again, and a night on which a destroy failed still leaves a day to retry.
const MinSlots = 3

// slotLetters name the slots. A letter, not a number, so a sim name never ends in a
// digit that could be mistaken for a count.
const slotLetters = "abcdefghij"

// MaxSlots is bounded by the letters above.
const MaxSlots = len(slotLetters)

// AllowedScenarios are the dc-simulator scenarios the playground will seed. towerpulse
// is left out on purpose: it needs its own tier with calculated metrics switched on,
// which no other scenario needs and which the public playground has no reason to pay for.
var AllowedScenarios = []string{"sitepulse", "buildingpulse", "fleetpulse", "widgetlab", "devicepulse"}

var nameGrammar = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Config is what a rotation needs to know. Every field has a validated range, so a
// value that would make the reset a no-op is refused at startup instead of running.
type Config struct {
	Scenarios []string
	Slots     int
	// ResetHour is the UTC hour at which the playground's day begins.
	ResetHour int
	// Seed is passed to every sim, so a slot's content is the same every day.
	Seed int64
}

// Validate refuses a configuration that would not rotate or would seed something the
// playground does not allow.
func (c Config) Validate() error {
	if c.Slots < MinSlots || c.Slots > MaxSlots {
		return fmt.Errorf("slots must be between %d and %d, got %d (a deleted tenant's token is held for 12 hours, so fewer than %d slots would ask for a token that is still taken)", MinSlots, MaxSlots, c.Slots, MinSlots)
	}
	if c.ResetHour < 0 || c.ResetHour > 23 {
		return fmt.Errorf("reset hour must be 0-23 (UTC), got %d", c.ResetHour)
	}
	if len(c.Scenarios) == 0 {
		return fmt.Errorf("at least one scenario is required")
	}
	seen := map[string]bool{}
	for _, s := range c.Scenarios {
		ok := false
		for _, a := range AllowedScenarios {
			ok = ok || a == s
		}
		if !ok {
			return fmt.Errorf("scenario %q is not allowed on the playground (allowed: %s)", s, strings.Join(AllowedScenarios, ", "))
		}
		if seen[s] {
			return fmt.Errorf("scenario %q is listed twice", s)
		}
		seen[s] = true
	}
	return nil
}

// Day is the playground day number of t: whole days since the Unix epoch, counted from
// the reset hour rather than from midnight. Two instants on either side of the reset
// hour are on different days; the same instant is on the same day in every time zone.
func Day(t time.Time, resetHour int) int64 {
	shifted := t.Add(-time.Duration(resetHour) * time.Hour)
	return shifted.Unix() / 86400
}

// Slot is the slot current at t.
func Slot(t time.Time, c Config) int {
	return int(Day(t, c.ResetHour) % int64(c.Slots))
}

// SimName is the dcctl sim name of a scenario in a slot. The tenant token is derived
// from it by dcctl (sim-<name>), so the slot is part of the token.
func SimName(scenario string, slot int) string {
	return fmt.Sprintf("pg-%s-%c", scenario, slotLetters[slot])
}

// Plan is one rotation: what to create and run, and what to tear down.
type Plan struct {
	Slot int
	// Create are the sims of the current slot, in scenario order.
	Create []string
	// Destroy are the sims of every other slot.
	Destroy []string
}

// PlanRotation is the whole reset policy, as data.
func PlanRotation(t time.Time, c Config) Plan {
	cur := Slot(t, c)
	p := Plan{Slot: cur}
	for slot := 0; slot < c.Slots; slot++ {
		for _, sc := range c.Scenarios {
			name := SimName(sc, slot)
			if slot == cur {
				p.Create = append(p.Create, name)
			} else {
				p.Destroy = append(p.Destroy, name)
			}
		}
	}
	return p
}

// ValidSimName reports whether name is a legal dcctl sim name. Every name this tool
// builds is checked, so a scenario list can never yield a name dcctl would refuse.
func ValidSimName(name string) bool { return nameGrammar.MatchString(name) }
