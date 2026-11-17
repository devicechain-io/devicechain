// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package react

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

// fenceAlarmRule is raiseAlarmRule with a condition that tests geofence containment.
func fenceAlarmRule() rules.Rule {
	r := raiseAlarmRule("left-yard")
	r.When = rules.Condition{CEL: `!geo.inFence("yard")`}
	return r
}

// TestRaiseAlarmCarriesPositionDependency: an alarm raised by a rule that tests geofence
// containment says so on BOTH edges, so device-management can withhold it from a caller without
// location:read; an ordinary threshold rule's alarm does not. The resolved edge matters as much
// as the raised one: a clear is the device crossing the fence the other way.
func TestRaiseAlarmCarriesPositionDependency(t *testing.T) {
	for _, c := range []struct {
		name string
		rule rules.Rule
		want bool
	}{
		{"geofence rule", fenceAlarmRule(), true},
		{"threshold rule", raiseAlarmRule("over-temp"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The rule must COMPILE: an uncompilable one is also answered true (fail closed), which
			// would pass the geofence case without testing the judgement.
			if _, err := rules.Compile(c.rule, rules.DefaultLimits()); err != nil {
				t.Fatalf("fixture does not compile: %v", err)
			}
			alarms := &fakeAlarmSink{}
			d := NewDispatcher(fakeResolver{rule: c.rule, found: true}, nil, alarms, nil, nil, newFakeMetrics())
			d.Dispatch(context.Background(), evt())
			d.Dispatch(context.Background(), resolvedEvt())
			if len(alarms.raised) != 2 {
				t.Fatalf("want a raise and a clear, got %d requests", len(alarms.raised))
			}
			for _, r := range alarms.raised {
				if r.RequiresPosition != c.want {
					t.Fatalf("%s edge: RequiresPosition = %v, want %v", r.Edge, r.RequiresPosition, c.want)
				}
			}
		})
	}
}

// TestRaiseAlarmPositionFailsClosed: a rule that does not compile here (it did at publish, so this
// is a defect path) is treated as position-derived, never as safe to show.
func TestRaiseAlarmPositionFailsClosed(t *testing.T) {
	broken := raiseAlarmRule("k")
	broken.When = rules.Condition{CEL: `this is not cel`}
	alarms := &fakeAlarmSink{}
	d := NewDispatcher(fakeResolver{rule: broken, found: true}, nil, alarms, nil, nil, newFakeMetrics())
	d.Dispatch(context.Background(), evt())
	if len(alarms.raised) != 1 || !alarms.raised[0].RequiresPosition {
		t.Fatalf("an uncompilable rule must be judged position-derived: %+v", alarms.raised)
	}
}

// TestRaiseAlarmPositionCacheKeysOnTheDefinition: a rule id reused for a different definition (a
// profile deleted and created again under the same token restarts its versions) is judged on its
// new definition, not answered from the old one's cached verdict.
func TestRaiseAlarmPositionCacheKeysOnTheDefinition(t *testing.T) {
	alarms := &fakeAlarmSink{}
	res := &switchingResolver{rule: raiseAlarmRule("k")}
	d := NewDispatcher(res, nil, alarms, nil, nil, newFakeMetrics())
	d.Dispatch(context.Background(), evt())
	res.rule = fenceAlarmRule()
	res.rule.Actions[0].RaiseAlarm.AlarmKey = "k"
	d.Dispatch(context.Background(), evt())
	if len(alarms.raised) != 2 || alarms.raised[0].RequiresPosition || !alarms.raised[1].RequiresPosition {
		t.Fatalf("the verdict must follow the definition under one rule id: %+v", alarms.raised)
	}
}

type switchingResolver struct{ rule rules.Rule }

func (s *switchingResolver) Resolve(context.Context, string) (rules.Rule, bool, error) {
	return s.rule, true, nil
}
