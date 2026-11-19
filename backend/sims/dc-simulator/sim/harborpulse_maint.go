// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/rs/zerolog/log"
)

const (
	// HarborpulseDefectSuppressionWrongDevice holds a neighbouring lamp that never flickers.
	HarborpulseDefectSuppressionWrongDevice = "suppression-wrong-device"
	HarborpulseLampServicePrefix            = "hp-lamp-service-"
	HarborpulseJobLampService               = "lamp-service"
	HarborpulseMaintenanceLamp              = harborFlickerLamp
	HarborpulseControlLamp                  = "hp-light-north-3"
	HarborpulseLampServiceFrom              = 10.75
	HarborpulseLampServiceTo                = 11.5
	harborLampServiceLead                   = 10 * time.Second

	mutationCreateLampWindow = `mutation HarborLampWindow($request: AlarmSuppressionWindowCreateRequest!) {
  createAlarmSuppressionWindow(request: $request) { token }
 }`
	mutationDeleteLampWindow = `mutation HarborDeleteLampWindow($token: String!) {
  deleteAlarmSuppressionWindow(token: $token)
 }`
	queryLampWindowByToken = `query HarborLampWindowByToken($tokens: [String!]!) {
  alarmSuppressionWindowsByToken(tokens: $tokens) { token }
 }`
	queryLampWindows = `query HarborLampWindows($c: AlarmSuppressionWindowSearchCriteria!) {
  alarmSuppressionWindows(criteria: $c) { results { token } pagination { totalRecords } }
 }`
)

// lampService plans exact wall instants, without the minute rounding CALENDAR triggers need.
func (s *harborpulse) lampService(started time.Time, day int) (ScenarioJob, map[string]any) {
	device := HarborpulseMaintenanceLamp
	if s.defects.suppressionWrongDevice {
		device = "hp-light-east-2"
	}
	opens := harborWall(started, s.dayLength, day, HarborpulseLampServiceFrom)
	closes := harborWall(started, s.dayLength, day, HarborpulseLampServiceTo)
	token := fmt.Sprintf("%s%d-d%d", HarborpulseLampServicePrefix, started.Unix(), day)
	job := ScenarioJob{Day: day, Name: HarborpulseJobLampService, Window: token, Device: device,
		WindowOpens: opens.UTC().Format(time.RFC3339Nano), WindowCloses: closes.UTC().Format(time.RFC3339Nano)}
	return job, map[string]any{"token": token, "name": "East Quay lamp driver replacement",
		"reason": "Replace the intermittent lamp driver while the Works crew is on site.",
		"kind":   "ONE_SHOT", "startTime": job.WindowOpens, "endTime": job.WindowCloses,
		"targets": []any{map[string]any{"type": "DEVICE", "token": device}}, "alarmKeys": []string{HarborpulseFlickerAlarmKey}}
}

// lampWindowExists reports whether the window this run planned is on the platform.
func lampWindowExists(ctx context.Context, rt *Runtime, token string) (bool, error) {
	var out struct {
		Windows []struct {
			Token string `json:"token"`
		} `json:"alarmSuppressionWindowsByToken"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryLampWindowByToken, map[string]any{"tokens": []string{token}}, &out); err != nil {
		return false, err
	}
	for _, w := range out.Windows {
		if w.Token == token {
			return true, nil
		}
	}
	return false, nil
}

func deleteLampWindow(ctx context.Context, rt *Runtime, token string) error {
	var out struct {
		Deleted bool `json:"deleteAlarmSuppressionWindow"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationDeleteLampWindow, map[string]any{"token": token}, &out); err != nil {
		return fmt.Errorf("delete lamp window %s: %w", token, err)
	}
	if !out.Deleted {
		return fmt.Errorf("delete lamp window %s returned false", token)
	}
	return nil
}

// deleteHarborLampWindows reads ALL pages before deleting: deleting a page while paging would
// shift subsequent offsets and leave earlier runs' future windows suppressing this run's pages.
func deleteHarborLampWindows(ctx context.Context, rt *Runtime) error {
	var stale []string
	for page := 1; ; page++ {
		if page > 1000 {
			return fmt.Errorf("lamp window cleanup exceeded 1000 pages")
		}
		var out struct {
			Windows struct {
				Results []struct {
					Token string `json:"token"`
				} `json:"results"`
				Pagination struct {
					Total *int `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"alarmSuppressionWindows"`
		}
		if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, queryLampWindows,
			map[string]any{"c": map[string]any{"pageNumber": page, "pageSize": 100}}, &out); err != nil {
			return fmt.Errorf("list lamp windows: %w", err)
		}
		if out.Windows.Pagination.Total == nil {
			return fmt.Errorf("list lamp windows returned no totalRecords")
		}
		for _, w := range out.Windows.Results {
			if strings.HasPrefix(w.Token, HarborpulseLampServicePrefix) {
				stale = append(stale, w.Token)
			}
		}
		if page*100 >= *out.Windows.Pagination.Total || len(out.Windows.Results) < 100 {
			break
		}
	}
	for _, token := range stale {
		if err := deleteLampWindow(ctx, rt, token); err != nil {
			return err
		}
	}
	return nil
}

// scheduleMaintenance has its own per-day state: the campaign deadline is hours too late for
// this beat. A failed create leaves the day retryable; Tick still emits all of its telemetry.
func (s *harborpulse) scheduleMaintenance(ctx context.Context, rt *Runtime, now time.Time) error {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.maintenanceUnavailable {
		return nil
	}
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if started.IsZero() {
		return nil
	}
	if rt.Endpoints.DeviceMgmtGraphQL == "" {
		return fmt.Errorf("lamp maintenance needs deviceMgmtGraphQL")
	}
	day := harborDayOf(now.Sub(started), s.dayLength)
	// Prune on every pass, including already-scheduled and skipped days. Failed deletes stay
	// retryable, and <= catches windows whose exact day-3 pass was missed.
	if s.maintenancePruned == nil {
		s.maintenancePruned = map[string]bool{}
	}
	for _, old := range s.jobs {
		if old.Day <= day-3 && old.Window != "" && !s.maintenancePruned[old.Window] {
			if err := deleteLampWindow(ctx, rt, old.Window); err != nil && !userclient.AllHaveCode(err, "NOT_FOUND") {
				log.Warn().Err(err).Msg("prune old lamp window failed; retrying next tick")
			} else {
				s.maintenancePruned[old.Window] = true
			}
		}
	}
	if s.maintenanceScheduled == nil {
		s.maintenanceScheduled = map[int]bool{}
	}
	if s.maintenanceScheduled[day] {
		return nil
	}
	job, request := s.lampService(started, day)
	opens := harborWall(started, s.dayLength, day, HarborpulseLampServiceFrom)
	_, attempted := s.maintenanceScheduled[day]
	if !now.Before(opens) || (!attempted && !now.Add(harborLampServiceLead).Before(opens)) {
		s.maintenanceScheduled[day] = true
		log.Warn().Int("day", day).Msg("too late to schedule this sim day's lamp maintenance; it starts tomorrow")
		return nil
	}
	s.maintenanceScheduled[day] = false // presence records an eligible attempt, even if create fails
	var out struct {
		Window *struct {
			Token string `json:"token"`
		} `json:"createAlarmSuppressionWindow"`
	}
	if err := rt.Session.Query(ctx, rt.Endpoints.DeviceMgmtGraphQL, mutationCreateLampWindow, map[string]any{"request": request}, &out); err != nil {
		// A create whose answer was lost committed anyway: the retry would be refused as a token
		// in use for the rest of the day. The window this run planned is the one to keep.
		if exists, lerr := lampWindowExists(ctx, rt, job.Window); lerr != nil || !exists {
			return fmt.Errorf("create lamp window %s: %w", job.Window, err)
		}
		out.Window = &struct {
			Token string `json:"token"`
		}{Token: job.Window}
	}
	if out.Window == nil || out.Window.Token != job.Window {
		return fmt.Errorf("create lamp window %s returned no matching window", job.Window)
	}
	s.maintenanceScheduled[day] = true
	s.jobs = append(s.jobs, job)
	log.Info().Int("day", day).Str("window", job.Window).Str("opens", job.WindowOpens).Str("closes", job.WindowCloses).Msg("scheduled lamp maintenance")
	return nil
}

// bootstrapMaintenance keeps telemetry available on instances predating suppression windows.
// Other cleanup failures remain fatal: a stale window could silently hold this run's pages.
func (s *harborpulse) bootstrapMaintenance(ctx context.Context, rt *Runtime) error {
	err := deleteHarborLampWindows(ctx, rt)
	if err != nil && !lampWindowsUnsupported(err) {
		return err
	}
	s.jobsMu.Lock()
	s.maintenanceUnavailable = err != nil
	s.jobsMu.Unlock()
	if err != nil {
		log.Error().Err(err).Msg("lamp-maintenance beat is unavailable on this instance: alarm suppression windows are unsupported; continuing telemetry without scheduling windows")
	}
	return nil
}

func lampWindowsUnsupported(err error) bool {
	// Reuse the client's typed GraphQL refusal classification; transport/status errors and
	// resolver failures mentioning suppression are not evidence of an older schema.
	var gql *userclient.GraphQLError
	if !errors.As(err, &gql) || len(gql.Messages) == 0 {
		return false
	}
	for _, message := range gql.Messages {
		if !strings.HasPrefix(message, `Cannot query field "alarmSuppressionWindows" on type `) &&
			!strings.HasPrefix(message, `Unknown type "AlarmSuppressionWindowSearchCriteria"`) {
			return false
		}
	}
	return true
}
