// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// WindowScheduler asks device-management — the owner of the device types, their maintenance
// windows and the command impact — when a command may start. It satisfies both
// model.DispatchScheduler (the enqueue-time stamp) and model.WindowOpeningResolver plus
// model.WindowAssignmentResolver (the re-stamp pass), so the calculator that decides is device-management's alone: nothing here
// re-implements a schedule, a time zone or a daylight-saving rule.
type WindowScheduler struct {
	client *svcclient.Client
	url    string
}

// NewWindowScheduler binds a scheduler to a service client and device-management's GraphQL
// endpoint URL.
func NewWindowScheduler(client *svcclient.Client, graphqlURL string) *WindowScheduler {
	return &WindowScheduler{client: client, url: graphqlURL}
}

const commandDispatchSchedule = `query($deviceTokens: [String!]!, $commandKey: String!, $expiresAt: String) {
  commandDispatchSchedule(deviceTokens: $deviceTokens, commandKey: $commandKey, expiresAt: $expiresAt) {
    holds { deviceToken windowToken dispatchAfter dispatchUntil }
    refusals { deviceToken code reason }
  }
}`

// ScheduleDispatch implements model.DispatchScheduler.
//
// 🔴 BOTH LISTS ARE DECODED THROUGH POINTERS, because on this call an empty list is the
// HEALTHY answer ("nothing waits"): a response that lost the field entirely would decode to
// nil slices and read as unanimous permission to dispatch immediately, which for a
// disruptive command is the fail-open the whole gate exists to prevent. An absent field is
// an error, not an empty verdict.
func (s *WindowScheduler) ScheduleDispatch(ctx context.Context, deviceTokens []string, commandKey string,
	expiresAt sql.NullTime) ([]model.DispatchHold, []model.BatchDeviceRefusal, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, nil, fmt.Errorf("verify: no tenant in context")
	}
	var out struct {
		CommandDispatchSchedule *struct {
			Holds *[]struct {
				DeviceToken   string `json:"deviceToken"`
				WindowToken   string `json:"windowToken"`
				DispatchAfter string `json:"dispatchAfter"`
				DispatchUntil string `json:"dispatchUntil"`
			} `json:"holds"`
			Refusals *[]struct {
				DeviceToken string `json:"deviceToken"`
				Code        string `json:"code"`
				Reason      string `json:"reason"`
			} `json:"refusals"`
		} `json:"commandDispatchSchedule"`
	}
	vars := map[string]any{"deviceTokens": deviceTokens, "commandKey": commandKey, "expiresAt": nil}
	if expiresAt.Valid {
		vars["expiresAt"] = expiresAt.Time.UTC().Format(time.RFC3339)
	}
	if err := s.client.Query(ctx, s.url, tenant, commandDispatchSchedule, vars, &out); err != nil {
		return nil, nil, err
	}
	if out.CommandDispatchSchedule == nil || out.CommandDispatchSchedule.Holds == nil ||
		out.CommandDispatchSchedule.Refusals == nil {
		return nil, nil, fmt.Errorf("verify: device-management returned no dispatch schedule")
	}

	holds := make([]model.DispatchHold, 0, len(*out.CommandDispatchSchedule.Holds))
	for _, h := range *out.CommandDispatchSchedule.Holds {
		after, err := time.Parse(time.RFC3339, h.DispatchAfter)
		if err != nil {
			return nil, nil, fmt.Errorf("verify: device-management returned an unreadable dispatch time %q", h.DispatchAfter)
		}
		until, err := time.Parse(time.RFC3339, h.DispatchUntil)
		if err != nil {
			return nil, nil, fmt.Errorf("verify: device-management returned an unreadable window close %q", h.DispatchUntil)
		}
		holds = append(holds, model.DispatchHold{DeviceToken: h.DeviceToken, WindowToken: h.WindowToken,
			DispatchAfter: after, DispatchUntil: until})
	}
	refusals := make([]model.BatchDeviceRefusal, 0, len(*out.CommandDispatchSchedule.Refusals))
	for _, r := range *out.CommandDispatchSchedule.Refusals {
		// The code is relayed exactly as the owner sent it, as the vocabulary gate does.
		refusals = append(refusals, model.BatchDeviceRefusal{
			DeviceToken: r.DeviceToken, Code: model.RejectionCode(r.Code), Reason: r.Reason,
		})
	}
	return holds, refusals, nil
}

var _ model.WindowAssignmentResolver = (*WindowScheduler)(nil)

const currentDispatchWindows = `query($deviceTokens: [String!]!, $commandKey: String!) {
  commandDispatchSchedule(deviceTokens: $deviceTokens, commandKey: $commandKey) {
    holds { deviceToken windowToken dispatchAfter dispatchUntil }
    ungated
  }
}`

// CurrentWindows implements model.WindowAssignmentResolver over the same owner-side decision
// enqueue asks for, with no expiry: a command already waiting is not refused again, only rebound.
//
// 🔴 BOTH LISTS ARE DECODED THROUGH POINTERS, for ScheduleDispatch's reason with the stakes
// raised: a lost `ungated` would read as "nothing is ungated" and a lost `holds` as every device
// having left its window. An absent field is an error, so the pass leaves every row as stamped.
func (s *WindowScheduler) CurrentWindows(ctx context.Context, deviceTokens []string,
	commandKey string) ([]model.DispatchHold, []string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, nil, fmt.Errorf("verify: no tenant in context")
	}
	var out struct {
		CommandDispatchSchedule *struct {
			Holds *[]struct {
				DeviceToken   string `json:"deviceToken"`
				WindowToken   string `json:"windowToken"`
				DispatchAfter string `json:"dispatchAfter"`
				DispatchUntil string `json:"dispatchUntil"`
			} `json:"holds"`
			Ungated *[]string `json:"ungated"`
		} `json:"commandDispatchSchedule"`
	}
	vars := map[string]any{"deviceTokens": deviceTokens, "commandKey": commandKey}
	if err := s.client.Query(ctx, s.url, tenant, currentDispatchWindows, vars, &out); err != nil {
		return nil, nil, err
	}
	if out.CommandDispatchSchedule == nil || out.CommandDispatchSchedule.Holds == nil ||
		out.CommandDispatchSchedule.Ungated == nil {
		return nil, nil, fmt.Errorf("verify: device-management returned no current dispatch windows")
	}
	holds := make([]model.DispatchHold, 0, len(*out.CommandDispatchSchedule.Holds))
	for _, h := range *out.CommandDispatchSchedule.Holds {
		after, err := time.Parse(time.RFC3339, h.DispatchAfter)
		if err != nil {
			return nil, nil, fmt.Errorf("verify: device-management returned an unreadable dispatch time %q", h.DispatchAfter)
		}
		until, err := time.Parse(time.RFC3339, h.DispatchUntil)
		if err != nil {
			return nil, nil, fmt.Errorf("verify: device-management returned an unreadable window close %q", h.DispatchUntil)
		}
		holds = append(holds, model.DispatchHold{DeviceToken: h.DeviceToken, WindowToken: h.WindowToken,
			DispatchAfter: after, DispatchUntil: until})
	}
	return holds, *out.CommandDispatchSchedule.Ungated, nil
}

const maintenanceWindowOpening = `query($token: String!, $at: String) {
  maintenanceWindowOpening(token: $token, at: $at) { found openNow dispatchAfter closes }
}`

// WindowOpening implements model.WindowOpeningResolver.
func (s *WindowScheduler) WindowOpening(ctx context.Context, windowToken string,
	at time.Time) (model.WindowOpening, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return model.WindowOpening{}, fmt.Errorf("verify: no tenant in context")
	}
	var out struct {
		MaintenanceWindowOpening *struct {
			Found         bool   `json:"found"`
			OpenNow       bool   `json:"openNow"`
			DispatchAfter string `json:"dispatchAfter"`
			Closes        string `json:"closes"`
		} `json:"maintenanceWindowOpening"`
	}
	vars := map[string]any{"token": windowToken, "at": at.UTC().Format(time.RFC3339)}
	if err := s.client.Query(ctx, s.url, tenant, maintenanceWindowOpening, vars, &out); err != nil {
		return model.WindowOpening{}, err
	}
	if out.MaintenanceWindowOpening == nil {
		return model.WindowOpening{}, fmt.Errorf("verify: device-management returned no window opening")
	}
	o := out.MaintenanceWindowOpening
	if !o.Found {
		return model.WindowOpening{Found: false}, nil
	}
	after, err := time.Parse(time.RFC3339, o.DispatchAfter)
	if err != nil {
		return model.WindowOpening{}, fmt.Errorf("verify: device-management returned an unreadable opening %q", o.DispatchAfter)
	}
	closes, err := time.Parse(time.RFC3339, o.Closes)
	if err != nil {
		return model.WindowOpening{}, fmt.Errorf("verify: device-management returned an unreadable close %q", o.Closes)
	}
	return model.WindowOpening{Found: true, OpenNow: o.OpenNow, DispatchAfter: after, Closes: closes}, nil
}
