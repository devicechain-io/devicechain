// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

func cleanS7() *S7 {
	truth := func(tenant, id string) S7Truth {
		value := sim.HarborpulseTenantMarker(tenant)
		return S7Truth{
			Devices: []S7Device{{ID: id, Token: "same-pump", ExternalID: "same-external", Description: sim.HarborpulseTenantDescription(tenant)}},
			Alarms:  []S7Alarm{{Token: "alarm-" + id, OriginatorID: id, OriginatorToken: "same-pump"}},
			Events:  []S7Event{{ID: "event-" + id, DeviceToken: "same-pump", Name: sim.HarborpulseTenantMarkerKey, Value: &value}},
		}
	}
	s := &S7{TenantA: "a", TenantB: "b", A: truth("a", "1"), B: truth("b", "2"),
		IsolationReads: map[string]S7Comparison{}, QAReads: map[string]S7Comparison{},
		ExpectedFlooding: []string{sim.HarborpulseEastQuay}, Flooding: []string{sim.HarborpulseEastQuay},
		ExpectedPumps: []string{"same-pump"}, Pumps: []string{"same-pump"}}
	s.ObservedA = s.A
	for _, tool := range []string{"list_devices", "list_alarms", "get_device_state", "aggregate_measurements"} {
		s.IsolationReads[tool] = S7Comparison{Expected: s7JSON([]string{"tenant-b-platform-row"}), Actual: s7JSON([]string{"tenant-b-platform-row"})}
	}
	for _, tool := range []string{"list_alarms", "list_commands", "aggregate_measurements"} {
		s.QAReads[tool] = S7Comparison{Expected: s7JSON([]string{"independent-platform-answer"}), Actual: s7JSON([]string{"independent-platform-answer"})}
	}
	s.PumpFrom, s.PumpTo = at(17), at(19.5)
	commands := []s7Command{{Token: "c", DeviceToken: "same-pump", Name: sim.HarborpulseStartPumpKey, Status: "SUCCESSFUL", QueuedTime: at(18).Format("2006-01-02T15:04:05Z07:00"), RespondedTime: at(18.1).Format("2006-01-02T15:04:05Z07:00")}}
	s.QAReads["list_commands"] = S7Comparison{Expected: s7JSON(commands), Actual: s7JSON(commands)}
	return s
}

func TestS7CrossTenantDeviceCannotPass(t *testing.T) {
	o := Observations{S7: &S7{TenantA: "a", TenantB: "b"}}
	if tenantIsolation(o) == nil {
		t.Fatal("missing platform isolation evidence passed")
	}
}

func TestEachS7ControlTurnsExactlyItsInvariantRed(t *testing.T) {
	for _, control := range []struct {
		name  string
		plant func(*S7)
	}{
		{InvTenantIsolation, func(s *S7) { s.ObservedA = s.B }},
		{InvMCPFleetQA, func(s *S7) { s.Flooding = []string{sim.HarborpulseNorthQuay} }},
	} {
		t.Run(control.name, func(t *testing.T) {
			o := cleanDay().observe()
			o.S7 = cleanS7()
			for _, inv := range Evaluate(o) {
				if !inv.Passed {
					t.Fatalf("clean baseline %s: %s", inv.Name, inv.Detail)
				}
			}
			control.plant(o.S7)
			failed := []string{}
			for _, inv := range Evaluate(o) {
				if !inv.Passed {
					failed = append(failed, inv.Name)
				}
			}
			if !slices.Equal(failed, []string{control.name}) {
				t.Fatalf("red %v, want exactly %s", failed, control.name)
			}
			if Controls[control.name] == "" {
				t.Fatal("control is not registered")
			}
		})
	}
}

func TestS7IsolationRejectsForeignPlatformRowsAndMissingReads(t *testing.T) {
	for _, alter := range []func(*S7){
		func(s *S7) { s.TenantB = s.TenantA },
		func(s *S7) { s.B.Devices[0].ID = s.A.Devices[0].ID },
		func(s *S7) { s.B.Devices[0].Description = s.A.Devices[0].Description },
		func(s *S7) { s.B.Devices[0].ExternalID = "different" },
		func(s *S7) { s.B.Alarms[0].OriginatorID = s.A.Devices[0].ID },
		func(s *S7) { s.B.Alarms[0].Token = s.A.Alarms[0].Token },
		func(s *S7) { s.B.Events[0].ID = s.A.Events[0].ID },
		func(s *S7) { s.B.Events[0].Value = s.A.Events[0].Value },
		func(s *S7) { s.B.Events = nil },
	} {
		s := cleanS7()
		alter(s)
		if tenantIsolation(Observations{S7: s}) == nil {
			t.Fatal("foreign or missing platform evidence passed")
		}
	}
	for tool := range cleanS7().IsolationReads {
		s := cleanS7()
		delete(s.IsolationReads, tool)
		if tenantIsolation(Observations{S7: s}) == nil {
			t.Errorf("missing %s passed", tool)
		}
		s = cleanS7()
		read := s.IsolationReads[tool]
		read.Actual = s7JSON([]string{"tenant-a-platform-row"})
		s.IsolationReads[tool] = read
		if tenantIsolation(Observations{S7: s}) == nil {
			t.Errorf("cross-tenant %s passed", tool)
		}
	}
}

func TestS7QARequiresEveryToolAndBothAnswers(t *testing.T) {
	for tool := range cleanS7().QAReads {
		s := cleanS7()
		read := s.QAReads[tool]
		read.Actual = s7JSON("wrong answer")
		s.QAReads[tool] = read
		if mcpFleetQA(Observations{S7: s}) == nil {
			t.Errorf("wrong %s passed", tool)
		}
	}
	s := cleanS7()
	s.Pumps = nil
	if mcpFleetQA(Observations{S7: s}) == nil {
		t.Fatal("missing running pumps answer passed")
	}
	s = cleanS7()
	s.ExpectedFlooding, s.Flooding = nil, nil
	if mcpFleetQA(Observations{S7: s}) == nil {
		t.Fatal("empty flood truth passed")
	}
}

func TestS7ConnectivityWitnessRequiresDistinctCompleteAdmissions(t *testing.T) {
	own := []s7State{{DeviceToken: "charger", PresenceSource: "ASSERTED", LastConnectTime: "2026-11-22T10:00:00Z"}}
	foreign := []s7State{{DeviceToken: "charger", PresenceSource: "ASSERTED", LastConnectTime: "2026-11-22T10:00:01Z"}}
	if !s7StateWitness(own, foreign, []string{"charger"}) {
		t.Fatal("distinct platform admissions refused")
	}
	for _, rows := range [][]s7State{nil, own, {{DeviceToken: "charger", PresenceSource: "INFERRED"}}, {{DeviceToken: "charger", PresenceSource: "ASSERTED", LastConnectTime: "bad-time"}}} {
		if s7StateWitness(own, rows, []string{"charger"}) {
			t.Fatal("missing, identical, inferred or malformed admissions passed")
		}
	}
}

func TestS7ReadDocumentsMatchServedSchemas(t *testing.T) {
	for _, d := range []struct {
		area string
		doc  string
		vars map[string]any
	}{
		{"device-management", queryS7Devices, map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 100}}},
		{"device-management", queryS7Alarms, map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 100}}},
		{"event-management", queryS7Events, map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 100}}},
		{"event-management", queryS7Buckets, map[string]any{"c": map[string]any{"deviceToken": "d", "name": sim.HarborpulseTenantMarkerKey, "startTime": "2026-11-22T00:00:00Z", "endTime": "2026-11-22T01:00:00Z", "intervalSeconds": 3600}}},
		{"device-state", queryS7States, map[string]any{"tokens": []string{"d"}}},
		{"command-delivery", queryS7Commands, map[string]any{"c": map[string]any{"pageNumber": 1, "pageSize": 100}}},
	} {
		if errs := servedSchema(t, d.area).ValidateWithVariables(d.doc, d.vars); len(errs) > 0 {
			t.Errorf("%s: %v", d.doc, errs)
		}
	}
}

func TestS7AllowsOnlyKnownS5ExtraDevice(t *testing.T) {
	o := cleanDay().observe()
	o.S5.Replacement.Records = nil
	o.S7.A.Devices = append(o.S7.A.Devices, S7Device{ID: "s5-extra", Token: sim.HarborpulseReplacementSeparateDevice, ExternalID: "PAW-GAUGE-NORTH-3-NEW", Description: sim.HarborpulseTenantDescription(o.S7.TenantA)})
	o.S7.ObservedA = o.S7.A
	var red []string
	for _, inv := range Evaluate(o) {
		if !inv.Passed {
			red = append(red, inv.Name)
		}
	}
	if !slices.Equal(red, []string{InvReplacementIdentity}) {
		t.Fatalf("combined S5/S7 observations red %v, want only replacement identity", red)
	}
	for _, token := range []string{"unexpected-device", ""} {
		o.S7.A.Devices[1].Token = token
		o.S7.ObservedA = o.S7.A
		if tenantIsolation(o) == nil {
			t.Fatalf("unrecognized extra token %q passed", token)
		}
	}
}

func TestS7PumpAnswersRequireSupportingCommands(t *testing.T) {
	for _, rows := range [][]s7Command{
		{},
		{{Token: "c", DeviceToken: "another-pump", Name: sim.HarborpulseStartPumpKey, Status: "SUCCESSFUL", QueuedTime: at(18).Format("2006-01-02T15:04:05Z07:00"), RespondedTime: at(18.1).Format("2006-01-02T15:04:05Z07:00")}},
	} {
		s := cleanS7()
		s.QAReads["list_commands"] = S7Comparison{Expected: s7JSON(rows), Actual: s7JSON(rows)}
		if mcpFleetQA(Observations{S7: s}) == nil {
			t.Fatal("equal histories without a successful start command for the reported pump passed")
		}
	}
}

func TestS7PumpCommandRelevance(t *testing.T) {
	for name, alter := range map[string]func(*s7Command){
		"no-token":       func(c *s7Command) { c.Token = "" },
		"other-command":  func(c *s7Command) { c.Name = sim.HarborpulseStopPumpKey },
		"failed":         func(c *s7Command) { c.Status = "FAILED" },
		"missing-reply":  func(c *s7Command) { c.RespondedTime = "" },
		"bad-queue-time": func(c *s7Command) { c.QueuedTime = "bad" },
		"stale":          func(c *s7Command) { c.QueuedTime = at(16).Format(time.RFC3339Nano) },
		"future":         func(c *s7Command) { c.RespondedTime = at(20).Format(time.RFC3339Nano) },
		"reply-before-queue": func(c *s7Command) {
			c.RespondedTime = at(17.9).Format(time.RFC3339Nano)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := cleanS7()
			var commands []s7Command
			if err := json.Unmarshal(s.QAReads["list_commands"].Expected, &commands); err != nil {
				t.Fatal(err)
			}
			alter(&commands[0])
			s.QAReads["list_commands"] = S7Comparison{Expected: s7JSON(commands), Actual: s7JSON(commands)}
			if mcpFleetQA(Observations{S7: s}) == nil {
				t.Fatal("irrelevant or unsuccessful command supported a pump answer")
			}
		})
	}
	s := cleanS7()
	s.Pumps, s.ExpectedPumps = []string{"same-pump", "second-pump"}, []string{"same-pump", "second-pump"}
	if mcpFleetQA(Observations{S7: s}) == nil {
		t.Fatal("one pump's history supported a second reported pump")
	}
	var commands []s7Command
	if err := json.Unmarshal(s.QAReads["list_commands"].Expected, &commands); err != nil {
		t.Fatal(err)
	}
	next := commands[0]
	next.Token, next.DeviceToken = "second-command", "second-pump"
	commands = append(commands, next)
	s.QAReads["list_commands"] = S7Comparison{Expected: s7JSON(commands), Actual: s7JSON(commands)}
	if err := mcpFleetQA(Observations{S7: s}); err != nil {
		t.Fatalf("independently supported pump answers refused: %v", err)
	}
}

func TestS7ExtraDeviceExemptionRemainsNarrow(t *testing.T) {
	for _, extra := range []S7Device{
		{ID: "extra", Token: sim.HarborpulseReplacementSeparateDevice, ExternalID: "wrong"},
		{ID: "extra", Token: "same-pump", ExternalID: "same-external"},
	} {
		s := cleanS7()
		extra.Description = sim.HarborpulseTenantDescription(s.TenantA)
		s.A.Devices = append(s.A.Devices, extra)
		s.ObservedA = s.A
		if tenantIsolation(Observations{S7: s}) == nil {
			t.Fatal("malformed or duplicate extra device passed")
		}
	}
}
