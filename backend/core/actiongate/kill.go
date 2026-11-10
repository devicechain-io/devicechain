// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package actiongate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// KillCause is the closed vocabulary of actuation kill triggers.
type KillCause string

const (
	KillCauseTenantDisabled    KillCause = "tenant_disabled"
	KillCauseOperatorSuspended KillCause = "operator_suspended"
)

// KillEvent applies a one-way brake to a tenant's area fences.
type KillEvent struct {
	EventID       string    `json:"eventId"`
	Tenant        string    `json:"tenant"`
	Cause         KillCause `json:"cause"`
	PolicyVersion int       `json:"policyVersion"`
	At            time.Time `json:"at"`
}

// Validate checks the wire event before it can affect a fence.
func (e KillEvent) Validate() error {
	if strings.TrimSpace(e.EventID) == "" || strings.TrimSpace(e.Tenant) == "" {
		return fmt.Errorf("kill event requires eventId and tenant")
	}
	if e.Cause != KillCauseTenantDisabled && e.Cause != KillCauseOperatorSuspended {
		return fmt.Errorf("unknown kill cause %q", e.Cause)
	}
	if e.PolicyVersion < 0 {
		return fmt.Errorf("kill policyVersion must be non-negative")
	}
	if e.At.IsZero() {
		return fmt.Errorf("kill event requires at")
	}
	return nil
}

// EncodeKillEvent validates and encodes a bounded JSON event.
func EncodeKillEvent(e KillEvent) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(e)
	if err == nil && len(b) > 4096 {
		return nil, fmt.Errorf("kill event exceeds 4 KiB")
	}
	return b, err
}

// DecodeKillEvent accepts exactly one bounded event with no unknown fields.
func DecodeKillEvent(b []byte) (KillEvent, error) {
	var e KillEvent
	if len(b) > 4096 {
		return e, fmt.Errorf("kill event exceeds 4 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil {
		return KillEvent{}, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return KillEvent{}, fmt.Errorf("kill event has trailing data")
	}
	if err := e.Validate(); err != nil {
		return KillEvent{}, err
	}
	return e, nil
}

// FenceState is an area's local kill fence, independent of storage.
type FenceState struct {
	Present               bool
	Enabled               bool
	KilledAtPolicyVersion int
}

// FenceVerdict tells an area whether execution may proceed or needs a fence update.
type FenceVerdict string

const (
	FenceOpen     FenceVerdict = "open"
	FenceReenable FenceVerdict = "reenable"
	FenceClosed   FenceVerdict = "closed"
)

// CheckFence compares a fresh decision with the last local kill.
// FenceReenable requires the caller to persist the newer enabled state atomically.
func CheckFence(s FenceState, decisionPolicyVersion int) FenceVerdict {
	if !s.Present {
		return FenceOpen
	}
	if decisionPolicyVersion > s.KilledAtPolicyVersion {
		if s.Enabled {
			return FenceOpen
		}
		return FenceReenable
	}
	return FenceClosed
}
