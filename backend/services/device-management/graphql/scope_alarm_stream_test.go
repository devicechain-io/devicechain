// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// The live alarm stream never touches the database, so the scope the alarms query applies
// there (Alarm.ScopedBy) is applied here to each event's stamp: a scoped subscriber gets
// an event only when the alarm shares an anchor with its scope, an unstamped alarm never,
// and an unscoped subscriber everything, as before.
func TestTheAlarmStreamDeliversOnlyTheScopesAlarms(t *testing.T) {
	scope := func(anchors ...string) auth.Scope {
		s, err := auth.NewScope(anchors)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	acmeAlarm := &model.AlarmStateChangeEvent{AlarmToken: "a", ScopeAnchors: []string{"customer:acme"}}
	coOwned := &model.AlarmStateChangeEvent{AlarmToken: "c", ScopeAnchors: []string{"customer:globex", "customer:acme"}}
	globexAlarm := &model.AlarmStateChangeEvent{AlarmToken: "g", ScopeAnchors: []string{"customer:globex"}}
	unstamped := &model.AlarmStateChangeEvent{AlarmToken: "u"}

	acme := alarmEventFilter{scope: scope("customer:acme")}
	for _, c := range []struct {
		ev   *model.AlarmStateChangeEvent
		want bool
	}{{acmeAlarm, true}, {coOwned, true}, {globexAlarm, false}, {unstamped, false}} {
		if got := acme.matches(c.ev); got != c.want {
			t.Errorf("acme's subscriber, alarm %s: delivered=%v, want %v", c.ev.AlarmToken, got, c.want)
		}
	}
	if (alarmEventFilter{scope: scope("group:acme")}).matches(acmeAlarm) {
		t.Error("an anchor of another kind matched")
	}
	// Negative control: an unscoped subscriber gets every alarm, stamped or not.
	for _, ev := range []*model.AlarmStateChangeEvent{acmeAlarm, globexAlarm, unstamped} {
		if !(alarmEventFilter{}).matches(ev) {
			t.Errorf("the unscoped subscriber was denied alarm %s", ev.AlarmToken)
		}
	}
}
