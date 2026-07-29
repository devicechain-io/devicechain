// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Receipt is what seed hands to verify: everything needed to find the secret
// again in a cluster that has no memory of the run that created it, plus the
// expected plaintext.
//
// It carries the plaintext deliberately. The alternative — a fixed literal both
// halves know — would make the drill pass against a leftover row from an earlier
// run, which is exactly the false pass a restore drill must not be able to
// produce. The value is freshly random per run, so a match means THIS run's
// ciphertext was opened. The file is written 0600 and is throwaway drill
// material; it is not a place to put anything real.
type Receipt struct {
	// Instance is the instance the secret was seeded into, recorded so verify can
	// say so when the operator points it at the wrong dump.
	Instance string `json:"instance"`
	// Tenant, Schema and SecretName locate the row: the store's handle is
	// (tenant, scope=tenant, name), and the row lives in the seeding service's
	// functional-area schema.
	Tenant     string `json:"tenant"`
	Schema     string `json:"schema"`
	SecretName string `json:"secretName"`
	// ChannelToken/ChannelID identify the notification channel the secret hangs
	// off. The token is what the API query needs; the ID is what the secret
	// handle is keyed by (ChannelSecretName — the ID, because a token is mutable).
	ChannelToken string `json:"channelToken"`
	ChannelID    uint   `json:"channelId"`
	// Identity/Password is the scoped identity seed minted, so verify can ask the
	// restored instance whether it still sees the channel.
	Identity string `json:"identity"`
	Password string `json:"password"`
	// Secret is the expected plaintext. See the type comment.
	Secret string `json:"secret"`

	// Events is the EVENT-STORE half of the drill, written by `seed-events` and
	// read by `verify-events`. It is a pointer because the two halves are
	// separable operations on separable databases: an instance can be recovered
	// core-only (an operational instance with no history) or event-only (history
	// with no control plane), and a receipt from a run that seeded no telemetry
	// must not make `verify` fail. nil means "this run did not drill the event
	// store", which verify-events reports as a setup error rather than a verdict.
	Events *EventSeed `json:"events,omitempty"`
}

// EventSeed is what seed-events records about the telemetry it wrote, so that
// verify-events can tell a restore that brought the rows back from one that
// merely brought a schema back.
//
// Every count here is an EXACT expectation, not a lower bound. A ">= 1" check
// passes against a restore that recovered one chunk of many, which is the
// partial-recovery failure most worth catching — and the one a hypertable makes
// possible, since its data is spread across chunks that are separate physical
// relations.
type EventSeed struct {
	// Tenant, DeviceToken and EventType locate the rows. The tenant is the same
	// one the secret half seeds under (the tenant id in the event store is the
	// tenant TOKEN — rdb.TenantScoped: "the stable tenant token"), so a single
	// tenant-scoped session can read both halves back.
	Tenant      string `json:"tenant"`
	DeviceToken string `json:"deviceToken"`
	EventType   int    `json:"eventType"`
	// Name is the measurement name, and Window is the closed time range the rows
	// were written into, RFC3339. Both are what the API query filters on.
	Name  string `json:"name"`
	Start string `json:"start"`
	End   string `json:"end"`
	// RawCount is how many measurement rows were written, and Sum is the sum of
	// their values. The sum is carried because a count alone cannot distinguish
	// the restored rows from a same-sized set of different ones — the same
	// argument the secret half makes for carrying the plaintext.
	RawCount int     `json:"rawCount"`
	Sum      float64 `json:"sum"`
	// MaterializedCount is the number of rows in the continuous aggregate's
	// MATERIALIZATION hypertable at seed time, read directly rather than through
	// the view.
	//
	// 🔴 It has to be read that way. measurement_rollups is created with
	// `timescaledb.materialized_only = false`, so a read through the view
	// recomputes anything above the aggregate's watermark live from the raw
	// hypertable and reports correct numbers for it whether or not those rows were
	// ever materialized. The watermark is restored state as well, so a view read
	// conflates two findings with opposite meanings. See materializationTable.
	MaterializedCount int `json:"materializedCount"`
}

// Validate rejects an event seed that cannot drive a verify-events, for the same
// reason Receipt.Validate exists: a malformed file must not degrade into a
// finding about the restore three steps later.
func (e EventSeed) Validate() error {
	missing := []string{}
	for name, value := range map[string]string{
		"events.tenant":      e.Tenant,
		"events.deviceToken": e.DeviceToken,
		"events.name":        e.Name,
		"events.start":       e.Start,
		"events.end":         e.End,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	// Zero is rejected rather than tolerated: a seed that wrote no rows would let
	// verify-events pass against a cluster where nothing was restored, by
	// comparing nothing to nothing.
	if e.RawCount == 0 {
		missing = append(missing, "events.rawCount")
	}
	if e.MaterializedCount == 0 {
		missing = append(missing, "events.materializedCount")
	}
	if len(missing) > 0 {
		return fmt.Errorf("receipt is missing %s", strings.Join(sortedStrings(missing), ", "))
	}
	return nil
}

// Validate rejects a receipt that cannot drive a verify. It is checked on READ
// rather than only on write, because the file crosses a cluster's lifetime and a
// truncated or hand-edited one must not degrade into a confusing decrypt error.
func (r Receipt) Validate() error {
	missing := []string{}
	for name, value := range map[string]string{
		"instance":   r.Instance,
		"tenant":     r.Tenant,
		"schema":     r.Schema,
		"secretName": r.SecretName,
		"secret":     r.Secret,
		// The API precheck needs these three. Without them here, a receipt that
		// lacked channelToken reached that check and reported "the restored
		// instance does not have channel \"\"" — blaming the restore for a
		// malformed file.
		"channelToken": r.ChannelToken,
		"identity":     r.Identity,
		"password":     r.Password,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if r.ChannelID == 0 {
		missing = append(missing, "channelId")
	}
	if len(missing) > 0 {
		// Sorted so the message is stable across map iteration order.
		return fmt.Errorf("receipt is missing %s", strings.Join(sortedStrings(missing), ", "))
	}
	// Only when present: the event half is optional (see Receipt.Events), but a
	// half-written one is a broken file rather than an absent drill.
	if r.Events != nil {
		if err := r.Events.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// WriteReceipt persists the receipt at 0600.
func WriteReceipt(path string, r Receipt) error {
	if err := r.Validate(); err != nil {
		return fmt.Errorf("refusing to write an unusable receipt: %w", err)
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o600)
}

// ReadReceipt loads and validates a receipt.
func ReadReceipt(path string) (Receipt, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Receipt{}, err
	}
	var r Receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		return Receipt{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := r.Validate(); err != nil {
		return Receipt{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}
