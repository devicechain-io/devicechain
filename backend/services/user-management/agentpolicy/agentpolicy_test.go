// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agentpolicy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func mustParse(t *testing.T, s string) *Document {
	t.Helper()
	d, err := Parse([]byte(s))
	require.NoError(t, err, s)
	return d
}

// Unknown keys, trailing data and rule violations are refused; nothing is half-accepted.
func TestParseIsStrict(t *testing.T) {
	bad := []string{
		`{"actuation":"enabled","bogus":1}`,
		`{"tools":{"commands":{"mode":"approve","extra":true}}}`,
		`{"actuation":"on"}`,
		`{"actuation":"enabled"} {}`,
		`{"approvers":{"stepUpMaxAge":"2h"}}`,
		`{"expiry":{"max":"48h"}}`,
		`{"expiry":{"default":"2h","max":"1h"}}`,
		`{"tools":{"commands":{"auto":{"impact":["DESTRUCTIVE"]}}}}`,
		`{"tools":{"commands":{"mode":"auto"}}}`,
		`{"tools":{"rules":{"reactActions":["sendCommand"]}}}`,
		`{"tools":{"rules":{"mode":"auto"}}}`,
		`{"tools":{"rollouts":{"mode":"auto"}}}`,
		`{"tools":{"annotate":"approve"}}`,
		`{"tools":{"alarms":{"ack":"sometimes"}}}`,
		`{"clients":["a","a"]}`,
		`{"clients":[" a"]}`,
		`{"tools":{"commands":{"caps":{"perAction":-1}}}}`,
		`{"tools":{"commands":{"groups":[]}}}`,
		`not json`,
	}
	for _, b := range bad {
		_, err := Parse([]byte(b))
		require.ErrorIs(t, err, ErrInvalid, b)
	}
	d := mustParse(t, `{"actuation":"enabled","clients":["claude-desktop"],"approvers":{"roles":["ops-lead"],"stepUpMaxAge":"10m"},
		"tools":{"alarms":{"ack":"auto","clear":"approve","ratePerHour":200},
		"commands":{"groups":["hvac"],"commands":["setpoint"],"mode":"approve","auto":{"impact":["NORMAL"],"maxDevices":5},
		"twoPerson":{"impact":["DESTRUCTIVE"],"overDevices":50},"caps":{"perAction":500}},
		"rules":{"reactActions":["raiseAlarm"]},"rollouts":{"maxGroupSize":1000,"maxInFlight":10},"annotate":"auto"}}`)
	require.True(t, d.ActuationOn())
	require.True(t, d.ClientAllowed("claude-desktop"))
	require.False(t, d.ClientAllowed(""))
}

// The loosen/tighten classifier: every loosening is caught, and a pure tightening is not flagged.
func TestLoosenClassifier(t *testing.T) {
	base := `{"actuation":"enabled","clients":["a"],"approvers":{"roles":["r"],"stepUpMaxAge":"10m","allowSelfConfirm":false},
		"expiry":{"default":"1h","max":"2h","rollout":"2h"},"session":{"actRefreshMaxAge":"4h"},
		"taint":{"neverAutoWhenTaintedOrUnknown":true},"serviceAccounts":{"agentFlagRequiredFor":["command:write"]},
		"tools":{"alarms":{"ack":"approve","clear":"approve","ratePerHour":10},
		"commands":{"groups":["g"],"commands":["c"],"mode":"approve","auto":{"impact":["NORMAL"],"maxDevices":2,"devicesPerHour":5},
		"twoPerson":{"impact":["DISRUPTIVE"],"overDevices":10},"caps":{"perAction":20,"devicesPerHour":50}},
		"rules":{"reactActions":["raiseAlarm"],"mode":"approve"},"rollouts":{"maxGroupSize":10,"maxInFlight":2,"mode":"approve"},"annotate":"refuse"}}`
	loosen := map[string]string{
		"actuation":        `{"actuation":"disabled"}`, // prev: disabled -> base enabled (handled separately below)
		"client added":     `{"clients":["a","b"]}`,
		"approver added":   `{"approvers":{"roles":["r","s"],"stepUpMaxAge":"10m","allowSelfConfirm":false}}`,
		"step-up age":      `{"approvers":{"roles":["r"],"stepUpMaxAge":"20m","allowSelfConfirm":false}}`,
		"self confirm":     `{"approvers":{"roles":["r"],"stepUpMaxAge":"10m"}}`,
		"expiry":           `{"expiry":{"default":"2h","max":"2h","rollout":"2h"}}`,
		"session":          `{"session":{"actRefreshMaxAge":"8h"}}`,
		"taint":            `{"taint":{}}`,
		"public auto":      `{"auto":{"allowAutoForPublicClients":true}}`,
		"recovery":         `{"stepUp":{"acceptRecoveryCodes":true}}`,
		"flag requirement": `{"serviceAccounts":{}}`,
		"ack mode":         `{"tools":{"alarms":{"ack":"auto"}}}`,
		"rate removed":     `{"tools":{"alarms":{"ratePerHour":null}}}`,
		"groups removed":   `{"tools":{"commands":{"groups":null}}}`,
		"group added":      `{"tools":{"commands":{"groups":["g","h"]}}}`,
		"command added":    `{"tools":{"commands":{"commands":["c","d"]}}}`,
		"auto impact":      `{"tools":{"commands":{"auto":{"impact":["NORMAL","DISRUPTIVE"],"maxDevices":2,"devicesPerHour":5}}}}`,
		"auto devices":     `{"tools":{"commands":{"auto":{"impact":["NORMAL"],"maxDevices":3,"devicesPerHour":5}}}}`,
		"two person":       `{"tools":{"commands":{"twoPerson":{"overDevices":10}}}}`,
		"two person over":  `{"tools":{"commands":{"twoPerson":{"impact":["DISRUPTIVE"],"overDevices":11}}}}`,
		"per action":       `{"tools":{"commands":{"caps":{"perAction":21,"devicesPerHour":50}}}}`,
		"react":            `{"tools":{"rules":{"reactActions":[]}}}`, // not a loosening: see below
		"rollout size":     `{"tools":{"rollouts":{"maxGroupSize":11,"maxInFlight":2,"mode":"approve"}}}`,
		"annotate":         `{"tools":{"annotate":"auto"}}`,
	}
	prev := mustParse(t, base)
	for name, patch := range loosen {
		next := applyPatch(t, base, patch)
		got := Loosenings(prev, next)
		switch name {
		case "actuation":
			// Disabling is a tightening; ENABLING from it is the loosening.
			require.Empty(t, got, name)
			require.NotEmpty(t, Loosenings(next, prev), "enabling actuation must be a loosening")
		case "react":
			require.Empty(t, got, "removing an allowed REACT action is a tightening")
		default:
			require.NotEmpty(t, got, "%s must be classified as a loosening", name)
		}
	}
	// A pure tightening is not a loosening.
	tight := applyPatch(t, base, `{"clients":[],"tools":{"alarms":{"ack":"refuse","clear":"refuse","ratePerHour":1}}}`)
	require.Empty(t, Loosenings(prev, tight))
	// The first publish: enabling from nothing is a loosening; a disabled first policy is not.
	require.NotEmpty(t, Loosenings(nil, prev))
	require.Empty(t, Loosenings(nil, mustParse(t, `{"actuation":"disabled","tools":{"alarms":{"ack":"refuse"}}}`)))
}

// applyPatch merges a shallow JSON patch over base, at the top level and one level into tools and
// tools.commands, enough for the table above.
func applyPatch(t *testing.T, base, patch string) *Document {
	t.Helper()
	d := mustParse(t, base)
	p := mustParseLoose(t, patch)
	if p.Actuation != "" {
		d.Actuation = p.Actuation
	}
	if p.Clients != nil {
		d.Clients = p.Clients
	}
	if p.Approvers != nil {
		d.Approvers = p.Approvers
	}
	if p.Expiry != nil {
		d.Expiry = p.Expiry
	}
	if p.Session != nil {
		d.Session = p.Session
	}
	if p.Taint != nil {
		d.Taint = p.Taint
	}
	if p.Auto != nil {
		d.Auto = p.Auto
	}
	if p.StepUp != nil {
		d.StepUp = p.StepUp
	}
	if p.ServiceAccounts != nil {
		d.ServiceAccounts = p.ServiceAccounts
	}
	if pt := p.Tools; pt != nil {
		if pt.Alarms != nil {
			a := *d.Tools.Alarms
			if pt.Alarms.Ack != nil {
				a.Ack = pt.Alarms.Ack
			}
			if pt.Alarms.Clear != nil {
				a.Clear = pt.Alarms.Clear
			}
			if patchHas(patch, `"ratePerHour"`) {
				a.RatePerHour = pt.Alarms.RatePerHour
			}
			d.Tools.Alarms = &a
		}
		if pt.Commands != nil {
			c := *d.Tools.Commands
			if patchHas(patch, `"groups"`) {
				c.Groups = pt.Commands.Groups
			}
			if pt.Commands.Commands != nil {
				c.Commands = pt.Commands.Commands
			}
			if pt.Commands.Auto != nil {
				c.Auto = pt.Commands.Auto
			}
			if pt.Commands.TwoPerson != nil {
				c.TwoPerson = pt.Commands.TwoPerson
			}
			if pt.Commands.Caps != nil {
				c.Caps = pt.Commands.Caps
			}
			d.Tools.Commands = &c
		}
		if pt.Rules != nil {
			d.Tools.Rules = pt.Rules
		}
		if pt.Rollouts != nil {
			d.Tools.Rollouts = pt.Rollouts
		}
		if pt.Annotate != nil {
			d.Tools.Annotate = pt.Annotate
		}
	}
	require.NoError(t, d.Validate())
	return d
}

func mustParseLoose(t *testing.T, s string) *Document {
	t.Helper()
	d, err := Parse([]byte(s))
	require.NoError(t, err, s)
	return d
}

func patchHas(patch, key string) bool {
	for i := 0; i+len(key) <= len(patch); i++ {
		if patch[i:i+len(key)] == key {
			return true
		}
	}
	return false
}

func enabledState(doc string) State {
	return State{InstanceEnabled: true, Policy: mustParseNoT(doc), Version: 3, EligibleApprovers: 5}
}

func mustParseNoT(s string) *Document {
	d, err := Parse([]byte(s))
	if err != nil {
		panic(err)
	}
	return d
}

const openPolicy = `{"actuation":"enabled","clients":["agent"],"tools":{"alarms":{"ack":"auto","clear":"approve"},
	"commands":{"commands":["reboot","wipe"],"auto":{"impact":["NORMAL","DISRUPTIVE"],"maxDevices":5},"caps":{"perAction":100}},
	"rollouts":{"maxGroupSize":500}}}`

// No published policy, an unreadable one, a disabled one, the instance flag off, or the operator's
// suspension: every one refuses, whatever is asked.
func TestDecisionFailsClosedOnMissingPolicy(t *testing.T) {
	req := Request{Kind: KindAlarmAck, TargetCount: 1, Client: "agent", Attested: true}
	cases := map[string]State{
		"no policy":        {InstanceEnabled: true, Policy: nil, Version: 0},
		"version zero":     {InstanceEnabled: true, Policy: mustParseNoT(openPolicy), Version: 0},
		"instance off":     {InstanceEnabled: false, Policy: mustParseNoT(openPolicy), Version: 3},
		"suspended":        {InstanceEnabled: true, OperatorSuspended: true, Policy: mustParseNoT(openPolicy), Version: 3},
		"disabled":         enabledState(`{"actuation":"disabled","clients":["agent"],"tools":{"alarms":{"ack":"auto"}}}`),
		"absent actuation": enabledState(`{"clients":["agent"],"tools":{"alarms":{"ack":"auto"}}}`),
	}
	for name, st := range cases {
		d := Decide(st, req)
		require.Equal(t, VerdictRefuse, d.Verdict, name)
		require.NotEmpty(t, d.Reasons, name)
	}
	require.Equal(t, VerdictAuto, Decide(enabledState(openPolicy), req).Verdict, "control: the open policy does allow it")
}

// The client allowlist: an unlisted client, and a token with no client at all, are refused.
func TestDecisionRefusesUnlistedClient(t *testing.T) {
	st := enabledState(openPolicy)
	for _, c := range []string{"", "other"} {
		d := Decide(st, Request{Kind: KindAlarmAck, TargetCount: 1, Client: c, Attested: true})
		require.Equal(t, VerdictRefuse, d.Verdict, c)
	}
}

// The code floors hold whatever the policy says.
func TestDecisionCodeFloors(t *testing.T) {
	st := enabledState(openPolicy)
	// DESTRUCTIVE: never AUTO, always two.
	d := Decide(st, Request{Kind: KindCommandSend, Impact: ImpactDestructive, Command: "wipe", TargetCount: 1, Client: "agent", Attested: true})
	require.Equal(t, VerdictApprove, d.Verdict)
	require.Equal(t, 2, d.RequiredApprovals)
	// NORMAL inside the auto block: AUTO.
	d = Decide(st, Request{Kind: KindCommandSend, Impact: ImpactNormal, Command: "reboot", TargetCount: 1, Client: "agent", Attested: true})
	require.Equal(t, VerdictAuto, d.Verdict)
	// Over the auto device limit: approval; over the default two-person size: two.
	d = Decide(st, Request{Kind: KindCommandBatch, Impact: ImpactNormal, Command: "reboot", TargetCount: 60, Client: "agent", Attested: true})
	require.Equal(t, VerdictApprove, d.Verdict)
	require.Equal(t, 2, d.RequiredApprovals)
	// Over the per-action cap: refused.
	require.Equal(t, VerdictRefuse, Decide(st, Request{Kind: KindCommandBatch, Command: "reboot", TargetCount: 101, Client: "agent", Attested: true}).Verdict)
	// A command not on the allowlist, or no command named: refused.
	require.Equal(t, VerdictRefuse, Decide(st, Request{Kind: KindCommandSend, Command: "format", TargetCount: 1, Client: "agent", Attested: true}).Verdict)
	require.Equal(t, VerdictRefuse, Decide(st, Request{Kind: KindCommandSend, TargetCount: 1, Client: "agent", Attested: true}).Verdict)
	// Rule changesets and rollouts never AUTO; a rollout over 100 devices needs two.
	require.Equal(t, VerdictApprove, Decide(st, Request{Kind: KindRuleChangeset, Client: "agent", Attested: true}).Verdict)
	d = Decide(st, Request{Kind: KindRolloutCreate, TargetCount: 101, Client: "agent", Attested: true})
	require.Equal(t, 2, d.RequiredApprovals)
	require.Equal(t, VerdictRefuse, Decide(st, Request{Kind: KindRolloutCreate, TargetCount: 501, Client: "agent", Attested: true}).Verdict)
	// Two approvers needed but only one eligible: refused, never downgraded.
	st.EligibleApprovers = 1
	require.Equal(t, VerdictRefuse, Decide(st, Request{Kind: KindCommandSend, Impact: ImpactDestructive, Command: "wipe", TargetCount: 1, Client: "agent", Attested: true}).Verdict)
	// Unknown kind and impact refuse.
	require.Equal(t, VerdictRefuse, Decide(st, Request{Kind: "DELETE_TENANT", Client: "agent"}).Verdict)
	require.Equal(t, VerdictRefuse, Decide(st, Request{Kind: KindCommandSend, Impact: "SPICY", Command: "reboot", TargetCount: 1, Client: "agent"}).Verdict)
}

// A public client's proposals never auto-execute unless the policy opts in.
func TestDecisionPublicClientNotAuto(t *testing.T) {
	st := enabledState(openPolicy)
	d := Decide(st, Request{Kind: KindAlarmAck, TargetCount: 1, Client: "agent", Attested: false})
	require.Equal(t, VerdictApprove, d.Verdict)
	require.Contains(t, d.Reasons, ReasonPublicClientNoAuto)
	st.Policy.Auto = &Auto{AllowAutoForPublicClients: true}
	require.Equal(t, VerdictAuto, Decide(st, Request{Kind: KindAlarmAck, TargetCount: 1, Client: "agent", Attested: false}).Verdict)
}
