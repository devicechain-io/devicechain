// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// The alarm stream filtered by an anchor applies the filter to EVERY pushed event, against the
// stamp each event carries, beside the subscriber's scope. The fixture is one shared area and
// one alarm per customer in it, plus an acme alarm outside it and an alarm with no stamp.
func TestTheAlarmStreamFilteredByAnAreaDeliversOnlyTheScopesAlarmsInIt(t *testing.T) {
	f := newScopeFixture(t)
	evs := map[string]*model.AlarmStateChangeEvent{
		"acme-shared":    {AlarmToken: "acme-shared", ScopeAnchors: []string{"customer:acme", "area:shared"}},
		"globex-shared":  {AlarmToken: "globex-shared", ScopeAnchors: []string{"customer:globex", "area:shared"}},
		"acme-elsewhere": {AlarmToken: "acme-elsewhere", ScopeAnchors: []string{"customer:acme", "area:acme-only"}},
		"unstamped":      {AlarmToken: "unstamped"},
	}
	deliver := func(who string, kind, token string) map[string]bool {
		ctx := f.operator()
		if who != "operator" {
			ctx = f.member("customer:" + who)
		}
		scope, _ := auth.ScopeFromContext(ctx)
		filter, err := newAlarmEventFilter(ctx, scope, alarmStreamArgs{Anchor: &model.AlarmAnchor{Type: kind, Token: token}})
		if err != nil {
			t.Fatalf("%s filtering by %s:%s: %v", who, kind, token, err)
		}
		got := map[string]bool{}
		for name, ev := range evs {
			if filter.matches(ev) {
				got[name] = true
			}
		}
		return got
	}
	same := func(got map[string]bool, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("delivered %v, want %v", got, want)
		}
		for _, w := range want {
			if !got[w] {
				t.Fatalf("delivered %v, want %v", got, want)
			}
		}
	}

	same(deliver("acme", "area", "shared"), "acme-shared")
	same(deliver("globex", "area", "shared"), "globex-shared")
	// Negative control: the unscoped subscriber gets both in the shared area, and only those.
	same(deliver("operator", "area", "shared"), "acme-shared", "globex-shared")
	// A place the subscriber has nothing in delivers nothing, as for a place that exists nowhere.
	same(deliver("globex", "area", "acme-only"))
	same(deliver("acme", "area", "no-such-area"))
	// Its own customer anchor is fine.
	same(deliver("acme", "customer", "acme"), "acme-shared", "acme-elsewhere")
}

// A customer outside the subscriber's scope is refused at subscribe time, exactly as the
// alarm queries refuse it, and an unknown anchor kind is an error, not a stream that never
// delivers. The unscoped subscriber may ask about any customer.
func TestTheAlarmStreamRefusesACustomerAnchorOutsideTheScope(t *testing.T) {
	f := newScopeFixture(t)
	acme := f.member("customer:acme")
	scope, _ := auth.ScopeFromContext(acme)

	for _, token := range []string{"globex", "no-such-customer"} {
		_, err := newAlarmEventFilter(acme, scope, alarmStreamArgs{Anchor: &model.AlarmAnchor{Type: "customer", Token: token}})
		if !errors.Is(err, auth.ErrForbidden) {
			t.Errorf("customer %q: %v, want forbidden", token, err)
		}
		// Through the resolver itself: refused before any feed is opened.
		_, err = (&SchemaResolver{}).AlarmStream(acme, alarmStreamArgs{Anchor: &model.AlarmAnchor{Type: "customer", Token: token}})
		if !errors.Is(err, auth.ErrForbidden) {
			t.Errorf("AlarmStream, customer %q: %v, want forbidden", token, err)
		}
	}
	if _, err := newAlarmEventFilter(acme, scope, alarmStreamArgs{Anchor: &model.AlarmAnchor{Type: "customer", Token: "acme"}}); err != nil {
		t.Errorf("acme's own customer anchor: %v", err)
	}
	if _, err := newAlarmEventFilter(acme, scope, alarmStreamArgs{Anchor: &model.AlarmAnchor{Type: "device", Token: "acme-pump"}}); err == nil {
		t.Error("an anchor kind alarms are not filtered by was accepted")
	}
	// Control: the unscoped subscriber may ask about another customer.
	op := f.operator()
	if _, err := newAlarmEventFilter(op, auth.Scope{}, alarmStreamArgs{Anchor: &model.AlarmAnchor{Type: "customer", Token: "globex"}}); err != nil {
		t.Errorf("the operator filtering by customer globex: %v", err)
	}
}
