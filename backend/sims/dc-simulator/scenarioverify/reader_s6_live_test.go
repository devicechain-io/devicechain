// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// The live read-back ran four minutes past midnight. The platform's latest rows
// then correctly described the NEXT day: enabled/online and a new max_kw=22 revision.
func TestS6ReadbackPreservesWatchedDayAcrossMidnight(t *testing.T) {
	o := cleanDay().observe()
	fixture := o.S6
	rolled := false
	volatileReads := 0
	r := s5ReaderServer(t, func(doc string, vars map[string]json.RawMessage) (any, int) {
		switch {
		case strings.Contains(doc, "deviceStatesByDeviceToken"):
			volatileReads++
			var states []S6Presence
			for _, p := range fixture.States {
				if rolled {
					p.Active = true
				}
				states = append(states, p)
			}
			return map[string]any{"deviceStatesByDeviceToken": states}, 0
		case strings.Contains(doc, "deviceCredentialsByToken"):
			var creds []S6Credential
			for _, device := range sim.HarborpulseX509Chargers() {
				c := fixture.Credentials[fixture.Ledger.Credentials[device]]
				if rolled {
					c.Enabled = true
				}
				fallback, _ := sim.DeriveDeviceCredential(0, device)
				creds = append(creds, c, S6Credential{Token: fallback})
			}
			return map[string]any{"deviceCredentialsByToken": creds}, 0
		case strings.Contains(doc, "deviceTrustAnchorsByToken"):
			return map[string]any{"deviceTrustAnchorsByToken": []any{map[string]any{"token": fixture.Ledger.Anchor, "enabled": true}}}, 0
		case strings.Contains(doc, "deviceConfiguration("):
			var device string
			_ = json.Unmarshal(vars["device"], &device)
			c := fixture.Configurations[device]
			if rolled {
				d := *c.Desired
				d.CreatedAt, d.Document = at(24).Format(time.RFC3339Nano), `{"max_kw":22}`
				c.Desired = &d
			}
			return map[string]any{"deviceConfiguration": c}, 0
		case strings.Contains(doc, "deviceTypesByToken"):
			return map[string]any{"deviceTypesByToken": []any{map[string]any{"token": sim.HarborpulsePullLightType, "commandChannel": "HTTP_PULL"}}}, 0
		case strings.Contains(doc, "measurementEvents("):
			var criteria struct{ DeviceToken string }
			_ = json.Unmarshal(vars["c"], &criteria)
			var rows []any
			for _, row := range fixture.Readings[criteria.DeviceToken] {
				rows = append(rows, map[string]any{"deviceToken": criteria.DeviceToken, "name": row.Name, "value": row.Value, "occurredTime": row.At.Format(time.RFC3339Nano)})
			}
			return map[string]any{"measurementEvents": map[string]any{"results": rows, "pagination": map[string]any{"totalRecords": len(rows)}}}, 0
		case strings.Contains(doc, "commands("):
			return map[string]any{"commands": map[string]any{"results": []any{}, "pagination": map[string]any{"totalRecords": 0}}}, 0
		default:
			t.Errorf("unexpected query %s", doc)
			return nil, 500
		}
	})
	r.endpoints.DeviceMgmtGraphQL, r.endpoints.DeviceStateGraphQL = r.endpoints.UpdateMgmtGraphQL, r.endpoints.UpdateMgmtGraphQL
	r.events, r.tenant = r.endpoints.UpdateMgmtGraphQL, "tenant"
	r.endpoints.CommandMgmtGraphQL = r.endpoints.UpdateMgmtGraphQL
	o.S6 = S6{}
	if err := captureS6(context.Background(), r, &o, fixture.Ledger, at(21)); err != nil {
		t.Fatal(err)
	}
	if volatileReads != 0 {
		t.Fatal("snapshot captured before the day's final S6 observation")
	}
	if err := captureS6(context.Background(), r, &o, fixture.Ledger, at(22)); err != nil {
		t.Fatal(err)
	}
	rolled = true
	if err := captureS6(context.Background(), r, &o, fixture.Ledger, at(26)); err != nil {
		t.Fatal(err)
	}
	if err := readS6(context.Background(), r, &o); err != nil {
		t.Fatal(err)
	}
	o.S6.Rejections = fixture.Rejections
	if err := chargerX509Admitted(o); err != nil {
		t.Error(err)
	}
	if err := configApplied(o); err != nil {
		t.Error(err)
	}
	if volatileReads != 1 {
		t.Fatalf("read mutable state %d times; midnight overwrote the watched day", volatileReads)
	}
}

func TestS6CannotTakeItsFirstSnapshotAfterMidnight(t *testing.T) {
	o := cleanDay().observe()
	ledger := o.S6.Ledger
	o.S6 = S6{}
	if err := captureS6(context.Background(), nil, &o, ledger, at(24)); err == nil {
		t.Fatal("next day's state accepted as the watched day")
	}
	if err := readS6(context.Background(), nil, &o); err == nil {
		t.Fatal("read-back accepted a missing watched-day snapshot")
	}
}

func TestS6PlantedRejectionMustRemainExactlyConfigRed(t *testing.T) {
	o := cleanDay().observe()
	o.S6.Configurations[sim.HarborpulseRejectedConfigCharger()].Reported.Status = "APPLIED"
	var failed []string
	for _, inv := range Evaluate(o) {
		if !inv.Passed {
			failed = append(failed, inv.Name)
		}
	}
	if len(failed) != 1 || failed[0] != InvConfigApplied {
		t.Fatalf("missing planted REJECTED report turned %v red, want exactly config-applied", failed)
	}
}

func TestS6BootstrapAdmissionCanPrecedeTheScenarioClock(t *testing.T) {
	o := cleanDay().observe()
	device := sim.HarborpulseRevokedCharger()
	state := o.S6.States[device]
	state.LastConnectTime = o.Clock.StartedAt.Add(-time.Second).Format(time.RFC3339Nano)
	o.S6.States[device] = state
	if err := chargerX509Admitted(o); err != nil {
		t.Fatalf("bootstrap opens the certificate session before the first tick starts the clock: %v", err)
	}
}
