// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agentpolicy

import (
	"fmt"
	"slices"
	"time"
)

// Loosenings lists every way next is LOOSER than prev: more automatic execution, larger limits,
// new clients, more approvers, fewer approvals, weaker step-up, longer waits. An empty result means
// next is no looser than prev (tighter, or the same). prev nil is the tenant's state before any
// policy was published: actuation off, so anything that turns it on is a loosening.
//
// 🔴 A LOOSENING IS WHAT NEEDS A FRESH SECOND FACTOR TO PUBLISH. So this must never call a looser
// policy tight. Every field is compared on its EFFECTIVE value (defaults filled in), and wherever
// absence has no bounded meaning (a cap that is not set) absence is read as the loosest value, so
// removing a cap is a loosening rather than a no-op.
func Loosenings(prev, next *Document) []string {
	if next == nil {
		return nil // no document is actuation off: never a loosening
	}
	if prev == nil {
		prev = &Document{}
	}
	var out []string
	add := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }

	if !prev.ActuationOn() && next.ActuationOn() {
		add("actuation enabled")
	}
	for _, c := range added(prev.Clients, next.Clients) {
		add("client %q added", c)
	}
	pa, na := approvers(prev), approvers(next)
	for _, r := range added(pa.Roles, na.Roles) {
		add("approver role %q added", r)
	}
	if next.StepUpMaxAge() > prev.StepUpMaxAge() {
		add("approver step-up age raised")
	}
	if !selfConfirm(pa) && selfConfirm(na) {
		add("self-confirmation allowed")
	}
	pe, ne := expiry(prev), expiry(next)
	durUp(add, "expiry.default", pe.Default, ne.Default, DefaultExpiry)
	durUp(add, "expiry.max", pe.Max, ne.Max, MaxExpiry)
	durUp(add, "expiry.rollout", pe.Rollout, ne.Rollout, DefaultRolloutExpiry)
	if next.ActRefreshMaxAge() > prev.ActRefreshMaxAge() {
		add("act session cap raised")
	}
	if taint(prev) && !taint(next) {
		add("taint tightening removed")
	}
	if !autoPublic(prev) && autoPublic(next) {
		add("automatic execution allowed for public clients")
	}
	if !prev.AcceptRecoveryCodes() && next.AcceptRecoveryCodes() {
		add("recovery codes accepted as step-up")
	}
	for _, a := range added(next.AgentFlagRequiredFor(), prev.AgentFlagRequiredFor()) {
		add("agent flag no longer required for %q", a)
	}

	pt, nt := tools(prev), tools(next)
	pal, nal := alarms(pt), alarms(nt)
	modeUp(add, "tools.alarms.ack", pal.Ack, nal.Ack, ModeApprove)
	modeUp(add, "tools.alarms.clear", pal.Clear, nal.Clear, ModeApprove)
	capUp(add, "tools.alarms.ratePerHour", pal.RatePerHour, nal.RatePerHour)

	pc, nc := commands(pt), commands(nt)
	switch {
	case pc.Groups != nil && nc.Groups == nil:
		add("command group restriction removed")
	default:
		if nc.Groups != nil {
			for _, g := range added(pc.Groups, nc.Groups) {
				add("command group %q added", g)
			}
		}
	}
	for _, c := range added(pc.Commands, nc.Commands) {
		add("command %q allowed", c)
	}
	modeUp(add, "tools.commands.mode", pc.Mode, nc.Mode, ModeApprove)
	pca, nca := commandAuto(pc), commandAuto(nc)
	for _, i := range added(impacts(pca.Impact), impacts(nca.Impact)) {
		add("automatic execution allowed for %s commands", i)
	}
	if intOr(nca.MaxDevices, 0) > intOr(pca.MaxDevices, 0) {
		add("tools.commands.auto.maxDevices raised")
	}
	capUp(add, "tools.commands.auto.devicesPerHour", pca.DevicesPerHour, nca.DevicesPerHour)
	ptp, ntp := twoPerson(pc), twoPerson(nc)
	for _, i := range added(impacts(ntp.Impact), impacts(ptp.Impact)) {
		add("two-person rule removed for %s commands", i)
	}
	if intOr(ntp.OverDevices, DefaultTwoPersonOverDevices) > intOr(ptp.OverDevices, DefaultTwoPersonOverDevices) {
		add("tools.commands.twoPerson.overDevices raised")
	}
	pcc, ncc := caps(pc), caps(nc)
	capUp(add, "tools.commands.caps.perAction", pcc.PerAction, ncc.PerAction)
	capUp(add, "tools.commands.caps.devicesPerHour", pcc.DevicesPerHour, ncc.DevicesPerHour)

	pr, nr := rules(pt), rules(nt)
	for _, a := range added(pr.ReactActions, nr.ReactActions) {
		add("rule REACT action %q allowed", a)
	}
	modeUp(add, "tools.rules.mode", pr.Mode, nr.Mode, ModeApprove)

	pro, nro := rollouts(pt), rollouts(nt)
	capUp(add, "tools.rollouts.maxGroupSize", pro.MaxGroupSize, nro.MaxGroupSize)
	capUp(add, "tools.rollouts.maxInFlight", pro.MaxInFlight, nro.MaxInFlight)
	modeUp(add, "tools.rollouts.mode", pro.Mode, nro.Mode, ModeApprove)

	modeUp(add, "tools.annotate", pt.Annotate, nt.Annotate, ModeAuto)
	return out
}

// added returns the members of next not in prev.
func added(prev, next []string) []string {
	var out []string
	for _, v := range next {
		if !slices.Contains(prev, v) {
			out = append(out, v)
		}
	}
	return out
}

func impacts(in []Impact) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func modeUp(add func(string, ...any), name string, prev, next *Mode, def Mode) {
	if modeOr(next, def).rank() > modeOr(prev, def).rank() {
		add("%s loosened to %s", name, modeOr(next, def))
	}
}

// capUp: an unset cap is unbounded, so unsetting one, or raising one, is a loosening.
func capUp(add func(string, ...any), name string, prev, next *int) {
	switch {
	case next == nil && prev != nil:
		add("%s removed", name)
	case next != nil && prev != nil && *next > *prev:
		add("%s raised", name)
	}
}

func durUp(add func(string, ...any), name string, prev, next *Duration, def time.Duration) {
	if next.D(def) > prev.D(def) {
		add("%s raised", name)
	}
}

func approvers(d *Document) Approvers {
	if d.Approvers == nil {
		return Approvers{}
	}
	return *d.Approvers
}

func selfConfirm(a Approvers) bool { return a.AllowSelfConfirm == nil || *a.AllowSelfConfirm }

func expiry(d *Document) Expiry {
	if d.Expiry == nil {
		return Expiry{}
	}
	return *d.Expiry
}

func taint(d *Document) bool { return d.Taint != nil && d.Taint.NeverAutoWhenTaintedOrUnknown }

func autoPublic(d *Document) bool { return d.Auto != nil && d.Auto.AllowAutoForPublicClients }

func tools(d *Document) Tools {
	if d.Tools == nil {
		return Tools{}
	}
	return *d.Tools
}

func alarms(t Tools) AlarmTools {
	if t.Alarms == nil {
		return AlarmTools{}
	}
	return *t.Alarms
}

func commands(t Tools) CommandTools {
	if t.Commands == nil {
		return CommandTools{}
	}
	return *t.Commands
}

func commandAuto(c CommandTools) CommandAuto {
	if c.Auto == nil {
		return CommandAuto{}
	}
	return *c.Auto
}

func twoPerson(c CommandTools) TwoPerson {
	if c.TwoPerson == nil {
		return TwoPerson{}
	}
	return *c.TwoPerson
}

func caps(c CommandTools) CommandCaps {
	if c.Caps == nil {
		return CommandCaps{}
	}
	return *c.Caps
}

func rules(t Tools) RuleTools {
	if t.Rules == nil {
		return RuleTools{}
	}
	return *t.Rules
}

func rollouts(t Tools) RolloutTools {
	if t.Rollouts == nil {
		return RolloutTools{}
	}
	return *t.Rollouts
}
