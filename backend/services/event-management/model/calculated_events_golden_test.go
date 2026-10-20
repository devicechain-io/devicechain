// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The preview's results are pinned byte for byte. testdata/calculated_events_golden.json was
// recorded from the preview BEFORE its evaluator was lifted into the whole-plan PlanEvaluator
// (backfill's), so a refactor that moves one sample, input, value or skip tally fails here.
// Regenerate only on purpose: DC_UPDATE_GOLDEN=1 go test -run TestCalculatedEventsGolden.

func TestCalculatedEventsGolden(t *testing.T) {
	api := newCalculatedTestApi(t)
	at := func(s int) time.Time { return calcAt.Add(time.Duration(s) * time.Second) }
	seed := func(ev string, s int, name string, v float64) {
		seedReading(t, api, "acme", "meter-1", ev, at(s), name, v)
	}
	// power: two readings at one instant, a second instant of the same event, a lone input,
	// an event with only an unread name.
	seed("a", 0, "voltage", 230)
	seed("a", 0, "current", 10)
	seed("a", 0, "temp", 21)
	seed("a", 5, "voltage", 100)
	seed("a", 5, "current", 2)
	seed("b", 10, "voltage", 240)
	seed("c", 20, "temp", 30)
	seed("d", 30, "voltage", 1e12)
	seed("d", 30, "current", 1e12)
	// ratio: zero divisor, range, INT with a fraction.
	seed("e", 40, "v", 10)
	seed("e", 40, "n", 2)
	seed("f", 50, "v", 10)
	seed("f", 50, "n", 0)
	seed("g", 60, "v", 1000)
	seed("g", 60, "n", 1)
	seed("h", 70, "v", 1)
	seed("h", 70, "n", 1.5)
	// boolean + guarded read.
	seed("i", 80, "on", 1)
	seed("i", 80, "v", 3)
	seed("j", 90, "on", 0)
	seed("j", 90, "v", 3)
	seed("k", 100, "v", 3)

	ceiling := 100.0
	floor := 0.5
	power := powerRequest()
	power.Inputs = append(power.Inputs, CalculatedInput{Key: "temp", DataType: "DOUBLE"})
	power.To = at(25)
	withConst := powerRequest()
	withConst.Expression = "voltage * current / 1000.0 * device.k"
	withConst.To = at(25)
	cases := map[string]CalculatedEventsRequest{
		"power": power,
		"power_constant": func() CalculatedEventsRequest {
			r := withConst
			r.Constants = []CalculatedConstant{{Key: "k", Value: "2"}}
			return r
		}(),
		"power_constant_absent": withConst,
		"power_constant_garbage": func() CalculatedEventsRequest {
			r := withConst
			r.Constants = []CalculatedConstant{{Key: "k", Value: "x"}}
			return r
		}(),
		"power_overflow": func() CalculatedEventsRequest {
			r := powerRequest()
			r.From, r.To = at(30), at(35)
			return r
		}(),
		"ratio_max": {DeviceToken: "meter-1", Expression: "v / double(n)", DataType: "DOUBLE", MaxValue: &ceiling,
			Inputs: []CalculatedInput{{Key: "v", DataType: "DOUBLE"}, {Key: "n", DataType: "INT"}},
			From:   at(35), To: at(75)},
		"ratio_min": {DeviceToken: "meter-1", Expression: "v / double(n)", DataType: "DOUBLE", MinValue: &floor,
			Inputs: []CalculatedInput{{Key: "v", DataType: "DOUBLE"}, {Key: "n", DataType: "INT"}},
			From:   at(35), To: at(75)},
		"int_result": {DeviceToken: "meter-1", Expression: "n + 1", DataType: "INT",
			Inputs: []CalculatedInput{{Key: "n", DataType: "INT"}}, From: at(35), To: at(75)},
		"boolean_guard": {DeviceToken: "meter-1", Expression: "on && v > 2.0", DataType: "BOOLEAN",
			Inputs: []CalculatedInput{{Key: "on", DataType: "BOOLEAN"}, {Key: "v", DataType: "DOUBLE"}},
			From:   at(75), To: at(105)},
		"empty_range": {DeviceToken: "meter-1", Expression: "v + 1.0", DataType: "DOUBLE",
			Inputs: []CalculatedInput{{Key: "v", DataType: "DOUBLE"}}, From: at(200), To: at(210)},
	}
	got := map[string]*CalculatedEventsResult{}
	for name, req := range cases {
		res, err := api.CalculatedEvents(acme(), req)
		require.NoError(t, err, name)
		got[name] = res
	}
	out, err := json.MarshalIndent(got, "", " ")
	require.NoError(t, err)
	path := filepath.Join("testdata", "calculated_events_golden.json")
	if os.Getenv("DC_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(path, out, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden missing; record it with DC_UPDATE_GOLDEN=1")
	want = bytes.ReplaceAll(want, []byte{13, 10}, []byte{10}) // a Windows checkout may convert
	require.JSONEq(t, string(want), string(out))
	require.Equal(t, string(want), string(out), "byte-identical")
}
