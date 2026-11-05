// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"os"
	"strings"
	"testing"

	"gorm.io/datatypes"
)

// The Fleet Pulse simulator's set-speed-limit parameter schema, held to the platform's own
// validator.
//
// dc-simulator cannot import this module (it is deliberately an untrusted external client),
// so the schema it publishes lives in a file this test reads by path -- the very bytes the
// simulator embeds and creates the command definition from. The claim under test is that an
// out-of-range limit is refused by the PLATFORM when the command is queued, with the
// simulator playing no part: the vehicle itself never range-checks.
const fleetpulseSpeedLimitSchemaPath = "../../../sims/dc-simulator/sim/fleetpulse_speed_limit_schema.json"

func TestFleetpulseSpeedLimitIsRefusedByThePlatform(t *testing.T) {
	raw, err := os.ReadFile(fleetpulseSpeedLimitSchemaPath)
	if err != nil {
		t.Fatalf("read %s: %v (the simulator embeds this file; if it moved, re-point this test "+
			"rather than deleting it)", fleetpulseSpeedLimitSchemaPath, err)
	}
	// The same strict decode the platform applies when the definition is created.
	specs, err := decodeParameterSchemaStrict(raw)
	if err != nil {
		t.Fatalf("the platform rejects the schema the simulator publishes: %v", err)
	}
	if err := ValidateParameterSchema(specs); err != nil {
		t.Fatalf("the platform rejects the schema the simulator publishes: %v", err)
	}
	j := datatypes.JSON(raw)
	def := &CommandDefinition{CommandKey: "set-speed-limit", ParameterSchema: &j}

	for _, ok := range []string{`{"limit_mps":5}`, `{"limit_mps":15}`, `{"limit_mps":30}`, `{"limit_mps":12.5}`} {
		if err := ValidateCommandPayload(def, []byte(ok)); err != nil {
			t.Errorf("%s was refused: %v", ok, err)
		}
	}
	for payload, want := range map[string]string{
		`{"limit_mps":50}`:           "above the maximum",
		`{"limit_mps":4.9}`:          "below the minimum",
		`{"limit_mps":-3}`:           "below the minimum",
		`{}`:                         "required parameter",
		`{"limit_mps":"fast"}`:       "",
		`{"limit_mps":15,"extra":1}`: "",
	} {
		err := ValidateCommandPayload(def, []byte(payload))
		if err == nil {
			t.Errorf("%s was accepted", payload)
			continue
		}
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Errorf("%s refused with %q, want it to mention %q", payload, err, want)
		}
	}
}
