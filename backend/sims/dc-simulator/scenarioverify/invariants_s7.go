// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

const InvTenantIsolation = "tenant-isolation"
const InvMCPFleetQA = "mcp-fleet-qa"

type S7Device struct {
	ID          string
	Token       string
	ExternalID  string
	Description string
}

type S7Alarm struct {
	Token           string
	OriginatorID    string
	OriginatorToken string
	AlarmKey        string
	State           string
	Severity        string
}

type S7Event struct {
	ID          string
	DeviceToken string
	Name        string
	Value       *float64
}

// Comparison holds two independently acquired platform answers, never sim counters.
type S7Comparison struct {
	Expected json.RawMessage
	Actual   json.RawMessage
}

type S7Truth struct {
	Devices []S7Device
	Alarms  []S7Alarm
	Events  []S7Event
}

type S7 struct {
	TenantA          string
	TenantB          string
	A                S7Truth
	B                S7Truth
	ObservedA        S7Truth
	IsolationReads   map[string]S7Comparison
	QAReads          map[string]S7Comparison
	Flooding         []string
	ExpectedFlooding []string
	Pumps            []string
	ExpectedPumps    []string
	PumpFrom         time.Time
	PumpTo           time.Time
}

func init() {
	Controls[InvTenantIsolation] = sim.HarborpulseDefectIsolationCrossRead
	Controls[InvMCPFleetQA] = sim.HarborpulseDefectMCPWrongAnswer
}

func s7Checks() []struct {
	name string
	fn   func(Observations) error
} {
	return []struct {
		name string
		fn   func(Observations) error
	}{{InvTenantIsolation, tenantIsolation}, {InvMCPFleetQA, mcpFleetQA}}
}

func tenantIsolation(o Observations) error {
	s := o.S7
	if s == nil || s.TenantA == "" || s.TenantB == "" || s.TenantA == s.TenantB {
		return fmt.Errorf("two distinct tenant identities are required")
	}
	if sim.HarborpulseTenantMarker(s.TenantA) == sim.HarborpulseTenantMarker(s.TenantB) {
		return fmt.Errorf("tenant witness collision")
	}
	if len(s.A.Devices) == 0 || len(s.B.Devices) == 0 || len(s.A.Alarms) == 0 || len(s.B.Alarms) == 0 || len(s.A.Events) == 0 || len(s.B.Events) == 0 {
		return fmt.Errorf("missing platform inventory, alarm or event evidence")
	}
	a, b := map[string]S7Device{}, map[string]S7Device{}
	ids := map[string]bool{}
	for _, d := range s.A.Devices {
		if d.ID == "" || d.Token == "" || ids[d.ID] || a[d.Token].ID != "" || d.Description != sim.HarborpulseTenantDescription(s.TenantA) {
			return fmt.Errorf("tenant A inventory contains foreign or duplicate state")
		}
		ids[d.ID], a[d.Token] = true, d
	}
	for _, d := range s.B.Devices {
		if d.ID == "" || d.Token == "" || ids[d.ID] || b[d.Token].ID != "" || d.Description != sim.HarborpulseTenantDescription(s.TenantB) {
			return fmt.Errorf("tenant B inventory contains tenant A or duplicate state")
		}
		ids[d.ID], b[d.Token] = true, d
	}
	for token, d := range a {
		peer, ok := b[token]
		if !ok && token == sim.HarborpulseReplacementSeparateDevice && d.ExternalID == "PAW-GAUGE-NORTH-3-NEW" {
			continue
		}
		if !ok || d.ExternalID != peer.ExternalID {
			return fmt.Errorf("fleets do not reuse device tokens and external ids")
		}
	}
	// S5's negative replacement control may add one extra device to A. Every
	// ordinary B token must still exist in A; the extra is already judged by S5.
	for token := range b {
		if _, ok := a[token]; !ok {
			return fmt.Errorf("tenant B token absent from tenant A")
		}
	}
	if !reflect.DeepEqual(s.ObservedA, s.A) {
		return fmt.Errorf("tenant A read observed another tenant's records")
	}
	for _, pair := range []struct {
		truth   S7Truth
		devices map[string]S7Device
		tenant  string
	}{{s.A, a, s.TenantA}, {s.B, b, s.TenantB}} {
		owned := map[string]bool{}
		for _, d := range pair.devices {
			owned[d.ID] = true
		}
		for _, alarm := range pair.truth.Alarms {
			if !owned[alarm.OriginatorID] {
				return fmt.Errorf("alarm originator is outside its tenant inventory")
			}
		}
		markers := 0
		for _, event := range pair.truth.Events {
			if event.Name == sim.HarborpulseTenantMarkerKey {
				markers++
			}
			if _, ok := pair.devices[event.DeviceToken]; !ok || event.ID == "" {
				return fmt.Errorf("stored event is outside its tenant inventory")
			}
			if event.Name == sim.HarborpulseTenantMarkerKey && (event.Value == nil || *event.Value != sim.HarborpulseTenantMarker(pair.tenant)) {
				return fmt.Errorf("stored event contains another tenant's measurement")
			}
		}
		if markers == 0 {
			return fmt.Errorf("no stored tenant measurement witness")
		}
	}
	alarmIDs, eventIDs := map[string]bool{}, map[string]bool{}
	for _, alarm := range s.A.Alarms {
		alarmIDs[alarm.Token] = true
	}
	for _, event := range s.A.Events {
		eventIDs[event.ID] = true
	}
	for _, alarm := range s.B.Alarms {
		if alarm.Token == "" || alarmIDs[alarm.Token] {
			return fmt.Errorf("tenant B read tenant A alarm")
		}
	}
	for _, event := range s.B.Events {
		if eventIDs[event.ID] {
			return fmt.Errorf("tenant B read tenant A event")
		}
	}
	return s7Compare(s.IsolationReads, []string{"list_devices", "list_alarms", "get_device_state", "aggregate_measurements"})
}

func mcpFleetQA(o Observations) error {
	s := o.S7
	if s == nil {
		return fmt.Errorf("missing MCP session evidence")
	}
	if err := s7Compare(s.QAReads, []string{"list_alarms", "list_commands", "aggregate_measurements"}); err != nil {
		return err
	}
	if err := s7PumpCommandEvidence(s); err != nil {
		return err
	}
	if len(s.ExpectedFlooding) == 0 || len(s.ExpectedPumps) == 0 {
		return fmt.Errorf("no critical flood or running pump evidence from GraphQL")
	}
	if !slices.Equal(s.Flooding, s.ExpectedFlooding) || !slices.Equal(s.Pumps, s.ExpectedPumps) {
		return fmt.Errorf("scripted fleet answer differs from GraphQL truth")
	}
	if !slices.Equal(s.ExpectedFlooding, []string{sim.HarborpulseEastQuay}) {
		return fmt.Errorf("critical flood answer did not identify East Quay")
	}
	return nil
}

// A pump answer needs both stored running telemetry and a successful start
// command in the same platform observation window, independently on each plane.
func s7PumpCommandEvidence(s *S7) error {
	if s.PumpFrom.IsZero() || !s.PumpTo.After(s.PumpFrom) {
		return fmt.Errorf("missing pump command observation window")
	}
	for _, answer := range []struct {
		pumps []string
		raw   json.RawMessage
	}{{s.ExpectedPumps, s.QAReads["list_commands"].Expected}, {s.Pumps, s.QAReads["list_commands"].Actual}} {
		var commands []s7Command
		if json.Unmarshal(answer.raw, &commands) != nil {
			return fmt.Errorf("missing successful pump command history")
		}
		for _, pump := range answer.pumps {
			supported := false
			for _, command := range commands {
				queued, qerr := time.Parse(time.RFC3339Nano, command.QueuedTime)
				responded, rerr := time.Parse(time.RFC3339Nano, command.RespondedTime)
				if command.Token != "" && command.DeviceToken == pump && command.Name == sim.HarborpulseStartPumpKey && command.Status == "SUCCESSFUL" &&
					qerr == nil && rerr == nil && !queued.Before(s.PumpFrom) && !responded.Before(queued) && !responded.After(s.PumpTo) {
					supported = true
				}
			}
			if !supported {
				return fmt.Errorf("reported pump has no relevant successful start command")
			}
		}
	}
	return nil
}

func s7Compare(reads map[string]S7Comparison, tools []string) error {
	for _, tool := range tools {
		read, ok := reads[tool]
		var expected, actual any
		if !ok || json.Unmarshal(read.Expected, &expected) != nil || json.Unmarshal(read.Actual, &actual) != nil {
			return fmt.Errorf("%s has no independently read platform evidence", tool)
		}
		if expected == nil || !reflect.DeepEqual(expected, actual) {
			return fmt.Errorf("%s answer differs from tenant GraphQL truth", tool)
		}
	}
	return nil
}
