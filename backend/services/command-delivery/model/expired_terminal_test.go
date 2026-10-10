// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// Every status has a deliberate answer: the four live states map to the terminal that says
// whether the platform or the device ran out of time; a terminal status or an unknown one
// is an error, never a guess that blames the device.
func TestExpiredTerminalForEveryStatus(t *testing.T) {
	want := map[CommandStatus]CommandStatus{
		CommandQueued: CommandExpired,
		CommandHeld:   CommandExpired,
		CommandParked: CommandExpired,
		CommandSent:   CommandTimeout,
	}
	for _, s := range nonTerminalStatuses {
		got, err := expiredTerminalFor(s.String())
		if err != nil || got != want[s].String() {
			t.Errorf("%s -> %q, %v; want %s", s, got, err, want[s])
		}
	}
	for _, s := range terminalStatuses {
		if got, err := expiredTerminalFor(s.String()); err == nil {
			t.Errorf("terminal status %s must be an error, got %q", s, got)
		}
	}
	for _, s := range []string{"", "BOGUS", "DELIVERED", "queued"} {
		if got, err := expiredTerminalFor(s); err == nil {
			t.Errorf("unknown status %q must be an error, got %q", s, got)
		}
	}
}

// A row in a status this build cannot name is left as it is, and the sweep still expires
// the rows around it.
func TestExpireStaleLeavesAnUnknownStatusAlone(t *testing.T) {
	api := newTestApi(t)
	sysctx := core.WithSystemContext(core.WithTenant(context.Background(), "A"))
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	for _, tok := range []string{"before", "odd", "after"} {
		if _, err := api.CreateCommand(sysctx, &CommandCreateRequest{
			Token: tok, DeviceToken: "d", Name: "x", ExpiresAt: &past,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := api.RDB.DB(sysctx).Model(&Command{}).Where("token = ?", "odd").
		Update("status", "BOGUS").Error; err != nil {
		t.Fatal(err)
	}

	count, _, err := api.ExpireStale(sysctx, time.Now())
	if err != nil {
		t.Fatalf("an unknown status must not fail the sweep: %v", err)
	}
	if count != 2 {
		t.Fatalf("the two recognised rows must expire, got %d", count)
	}
	var odd Command
	if err := api.RDB.DB(sysctx).Where("token = ?", "odd").First(&odd).Error; err != nil {
		t.Fatal(err)
	}
	if odd.Status != "BOGUS" {
		t.Fatalf("the unknown-status row must be untouched, got %q", odd.Status)
	}
}
