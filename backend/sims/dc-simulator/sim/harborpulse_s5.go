// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"crypto/ecdsa"
	"sync"
	"time"
)

// ScenarioS5 declares the operations expected of the platform. It contains no key or credential material.
// Restore drains and replacement evidence are populated by the later S5 beats.
type ScenarioS5 struct {
	OTA           ScenarioOTA            `json:"ota"`
	RestoreDrains []ScenarioRestoreDrain `json:"restoreDrains,omitempty"`
	Replacement   *ScenarioReplacement   `json:"replacement,omitempty"`
}

type ScenarioOTA struct {
	Artifact         string  `json:"artifact"`
	Digest           string  `json:"digest"`
	Version          string  `json:"version"`
	SizeBytes        int     `json:"sizeBytes"`
	Signer           string  `json:"signer"`
	KeyID            string  `json:"keyId"`
	Rollout          string  `json:"rollout,omitempty"`
	Group            string  `json:"group"`
	MaxInFlight      int     `json:"maxInFlight"`
	FailureThreshold int     `json:"failureThreshold"`
	ScheduleHour     float64 `json:"scheduleHour"`
	CreatedAt        string  `json:"createdAt,omitempty"`
	StartedAt        string  `json:"startedAt,omitempty"`
	RefusalCode      string  `json:"refusalCode,omitempty"`
}

// ScenarioRestoreDrain holds per-day accounting, never a platform success numerator.
type ScenarioRestoreDrain struct {
	Day           int    `json:"day"`
	Device        string `json:"device"`
	Buffered      int    `json:"buffered"`
	Accepted      int    `json:"accepted"`
	Shed          int    `json:"shed"`
	Backpressured int    `json:"backpressured"`
	Ambiguous     int    `json:"ambiguous"`
}

type ScenarioReplacement struct {
	Device                  string   `json:"device"`
	At                      string   `json:"at"`
	UnitIdentifier          string   `json:"unitIdentifier"`
	RetiredCredentialTokens []string `json:"retiredCredentialTokens"`
	NewCredentialToken      string   `json:"newCredentialToken"`
}

type harborS5State struct {
	mu          sync.Mutex
	ledger      *ScenarioS5
	key         *ecdsa.PrivateKey // process-local only; never marshalled or logged
	publicPEM   string
	rolloutDone bool
}

func (s *harborpulse) s5Ledger() *ScenarioS5 {
	s.s5.mu.Lock()
	defer s.s5.mu.Unlock()
	if s.s5.ledger == nil {
		return nil
	}
	out := *s.s5.ledger
	out.RestoreDrains = append([]ScenarioRestoreDrain(nil), out.RestoreDrains...)
	if out.Replacement != nil {
		r := *out.Replacement
		r.RetiredCredentialTokens = append([]string(nil), r.RetiredCredentialTokens...)
		out.Replacement = &r
	}
	return &out
}

// s5Beats retries failed operations on subsequent ticks and never repeats a successful beat daily.
func (s *harborpulse) s5Beats(ctx context.Context, rt *Runtime, now time.Time) error {
	started := s.startedAt()
	if started.IsZero() || now.Before(harborWall(started, s.dayLength, 0, HarborpulseRolloutHour)) {
		return nil
	}
	s.s5.mu.Lock()
	defer s.s5.mu.Unlock()
	if s.s5.rolloutDone {
		return nil
	}
	if s.s5.ledger == nil {
		return nil
	} // unit-driven telemetry without bootstrap has no OTA population
	if err := s.rolloutChargers(ctx, rt, started); err != nil {
		return err
	}
	s.s5.rolloutDone = true
	return nil
}
