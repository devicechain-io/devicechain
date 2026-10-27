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
//
// 🔑 IT MAY RUN WHILE SERVING A MEMBER LIMITED TO PART OF THE TENANT (ADR-081) — a single
// enqueue — and it tells svcclient so on this one call, derived here and never left on a
// shared context. That is safe because of where it is called: CreateCommand asks only
// after it has decided the member owns the device, and the batch path that also calls it
// refuses scoped members at its resolver. A new caller that could run for a scoped member
// must decide the member's right to every device it names BEFORE asking.
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
	if err := s.client.QueryOnBehalfOfScopedCaller(ctx, s.url, tenant, commandDispatchSchedule, vars, &out); err != nil {
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
var _ model.CommandImpactResolver = (*WindowScheduler)(nil)

const commandDispatchImpact = `query($deviceTokens: [String!]!, $commandKey: String!) {
  commandDispatchSchedule(deviceTokens: $deviceTokens, commandKey: $commandKey) {
    impacts { deviceToken impact }
  }
}`

// CommandImpact implements model.CommandImpactResolver over the same owner-side decision
// enqueue asks for: the schedule the window gate is computed from also names the impact the
// profile gives the command, so the two cannot be read from different places.
//
// 🔴 A DEVICE THE OWNER NAMED NO IMPACT FOR IS AN ERROR, NEVER NORMAL. The owner lists impacts only
// for enabled devices whose type defines the command, so an absent entry means "nothing decided"
// (disabled, gone, or the command is not in the vocabulary). Reading it as NORMAL would let an
// adapter that gates on impact treat the unknown as the safe class. An absent LIST is the same
// kind of failure (an older owner that does not know the field), and is decoded through a
// pointer so it can be told apart from an empty one.
func (s *WindowScheduler) CommandImpact(ctx context.Context, deviceToken, commandKey string) (string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("verify: no tenant in context")
	}
	var out struct {
		CommandDispatchSchedule *struct {
			Impacts *[]struct {
				DeviceToken string `json:"deviceToken"`
				Impact      string `json:"impact"`
			} `json:"impacts"`
		} `json:"commandDispatchSchedule"`
	}
	vars := map[string]any{"deviceTokens": []string{deviceToken}, "commandKey": commandKey}
	if err := s.client.Query(ctx, s.url, tenant, commandDispatchImpact, vars, &out); err != nil {
		return "", err
	}
	if out.CommandDispatchSchedule == nil || out.CommandDispatchSchedule.Impacts == nil {
		return "", fmt.Errorf("verify: device-management returned no command impacts")
	}
	for _, i := range *out.CommandDispatchSchedule.Impacts {
		if i.DeviceToken == deviceToken {
			if i.Impact == "" {
				return "", fmt.Errorf("verify: device-management returned an empty impact for %q", deviceToken)
			}
			return i.Impact, nil
		}
	}
	return "", fmt.Errorf("verify: device-management decided no impact for command %q on device %q", commandKey, deviceToken)
}

const currentDispatchWindows = `query($deviceTokens: [String!]!, $commandKey: String!) {
  commandDispatchSchedule(deviceTokens: $deviceTokens, commandKey: $commandKey, alreadyHeld: true) {
    holds { deviceToken windowToken dispatchAfter dispatchUntil }
    ungated
    undefined
  }
}`

// CurrentWindows implements model.WindowAssignmentResolver over the same owner-side decision
// enqueue asks for, with no expiry (a command already waiting is not refused again, only
// rebound) and with alreadyHeld: true, so the owner answers by the device type's window alone
// and a profile version that now marks the command NORMAL does not release it.
//
// 🔴 EVERY LIST IS DECODED THROUGH A POINTER, for ScheduleDispatch's reason with the stakes
// raised: a lost `ungated` would read as "nothing is ungated", a lost `holds` as every device
// having left its window, and a lost `undefined` as every command still being defined. An
// absent field is an error, so the pass leaves every row as stamped.
func (s *WindowScheduler) CurrentWindows(ctx context.Context, deviceTokens []string,
	commandKey string) ([]model.DispatchHold, []string, []string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return nil, nil, nil, fmt.Errorf("verify: no tenant in context")
	}
	var out struct {
		CommandDispatchSchedule *struct {
			Holds *[]struct {
				DeviceToken   string `json:"deviceToken"`
				WindowToken   string `json:"windowToken"`
				DispatchAfter string `json:"dispatchAfter"`
				DispatchUntil string `json:"dispatchUntil"`
			} `json:"holds"`
			Ungated   *[]string `json:"ungated"`
			Undefined *[]string `json:"undefined"`
		} `json:"commandDispatchSchedule"`
	}
	vars := map[string]any{"deviceTokens": deviceTokens, "commandKey": commandKey}
	if err := s.client.Query(ctx, s.url, tenant, currentDispatchWindows, vars, &out); err != nil {
		return nil, nil, nil, err
	}
	if out.CommandDispatchSchedule == nil || out.CommandDispatchSchedule.Holds == nil ||
		out.CommandDispatchSchedule.Ungated == nil || out.CommandDispatchSchedule.Undefined == nil {
		return nil, nil, nil, fmt.Errorf("verify: device-management returned no current dispatch windows")
	}
	holds := make([]model.DispatchHold, 0, len(*out.CommandDispatchSchedule.Holds))
	for _, h := range *out.CommandDispatchSchedule.Holds {
		after, err := time.Parse(time.RFC3339, h.DispatchAfter)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("verify: device-management returned an unreadable dispatch time %q", h.DispatchAfter)
		}
		until, err := time.Parse(time.RFC3339, h.DispatchUntil)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("verify: device-management returned an unreadable window close %q", h.DispatchUntil)
		}
		holds = append(holds, model.DispatchHold{DeviceToken: h.DeviceToken, WindowToken: h.WindowToken,
			DispatchAfter: after, DispatchUntil: until})
	}
	return holds, *out.CommandDispatchSchedule.Ungated, *out.CommandDispatchSchedule.Undefined, nil
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
