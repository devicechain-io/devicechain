// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-simulator/sim"
)

const (
	queryMaintenanceWindow = `query ScenarioMaintenanceWindow($tokens: [String!]!) {
  alarmSuppressionWindowsByToken(tokens: $tokens) { token startTime endTime targets { type token } }
 }`
	queryLampCycle = `query ScenarioLampCycle($c: AlarmSearchCriteria!) {
  alarms(criteria: $c) {
   results { token originatorToken originatorType originatorId alarmKey state raisedTime raisedDuringSuppression }
   pagination { totalRecords }
  }
 }`
)

func (r *Reader) MaintenanceWindow(ctx context.Context, token string) (*AlarmWindow, error) {
	var out struct {
		Windows []AlarmWindow `json:"alarmSuppressionWindowsByToken"`
	}
	if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryMaintenanceWindow, map[string]any{"tokens": []string{token}}, &out); err != nil {
		return nil, fmt.Errorf("read lamp suppression window %s: %w", token, err)
	}
	if len(out.Windows) == 0 {
		return nil, nil
	}
	if len(out.Windows) != 1 || out.Windows[0].Token != token {
		return nil, fmt.Errorf("read lamp window %s: platform returned different windows", token)
	}
	return &out.Windows[0], nil
}

func (r *Reader) LampCycle(ctx context.Context, device string) (*LampCycle, error) {
	var out struct {
		Alarms struct {
			Results    []LampCycle `json:"results"`
			Pagination struct {
				Total *int `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"alarms"`
	}
	vars := map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 2,
		"originatorType": "device", "originatorToken": device, "alarmKey": sim.HarborpulseFlickerAlarmKey}}
	if err := r.session.Query(ctx, r.endpoints.DeviceMgmtGraphQL, queryLampCycle, vars, &out); err != nil {
		return nil, fmt.Errorf("read lamp cycle %s: %w", device, err)
	}
	if out.Alarms.Pagination.Total == nil {
		return nil, fmt.Errorf("read lamp cycle %s: null totalRecords", device)
	}
	if *out.Alarms.Pagination.Total == 0 && len(out.Alarms.Results) == 0 {
		return nil, nil
	}
	if *out.Alarms.Pagination.Total != 1 || len(out.Alarms.Results) != 1 {
		return nil, fmt.Errorf("read lamp cycle %s: want one alarm row, got %d", device, *out.Alarms.Pagination.Total)
	}
	a := &out.Alarms.Results[0]
	if a.Device != device || a.Key != sim.HarborpulseFlickerAlarmKey || a.Token == "" {
		return nil, fmt.Errorf("read lamp cycle %s: returned a different or unnamed alarm", device)
	}
	return a, nil
}

// readMaintenance runs after 13:00 on the same day: a reused alarm row still describes the
// judged cycle. The invariant checks raisedTime explicitly, refusing a previous day's cycle.
func readMaintenance(ctx context.Context, r *Reader, obs *Observations) error {
	obs.Maint = Maintenance{Lamps: map[string]*LampCycle{}}
	for _, job := range obs.S4.Jobs {
		if job.Name != sim.HarborpulseJobLampService || job.Day != obs.Day {
			continue
		}
		if obs.Maint.Window != nil {
			return fmt.Errorf("multiple lamp-service windows ledgered for day %d", obs.Day)
		}
		if job.Window == "" {
			continue
		}
		window, err := r.MaintenanceWindow(ctx, job.Window)
		if err != nil {
			return err
		}
		obs.Maint.Window = window
	}
	for _, device := range []string{sim.HarborpulseMaintenanceLamp, sim.HarborpulseControlLamp} {
		alarm, err := r.LampCycle(ctx, device)
		if err != nil {
			return err
		}
		obs.Maint.Lamps[device] = alarm
		if alarm == nil {
			continue
		}
		attempts, err := r.Attempts(ctx, alarm.Token)
		if err != nil {
			return err
		}
		obs.S3.Attempts[alarm.Token] = attempts
	}
	return nil
}
