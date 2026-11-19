// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dc "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

// A carried latch suppresses the successor's next rising edge, so it is only sound for
// an episode that is provably continuous and unresolved. Every test here asserts the
// failure that matters: a successor breach that must dispatch, does.

func continuityRule(t *testing.T, version, token string, kind rules.RuleType) runtime.ScopedRule {
	t.Helper()
	threshold := 80.0
	r := rules.Rule{ID: "acme/p1@" + version + "/" + token, Name: "heat", Type: kind}
	if kind == rules.TypeAbsence {
		r.Ttl = rules.Duration(time.Hour)
	} else {
		r.When = rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: &threshold}
	}
	cr, err := rules.Compile(r, rules.Limits{})
	require.NoError(t, err)
	return runtime.ScopedRule{Tenant: "acme", ProfileVersionToken: "p1@" + version, Compiled: cr}
}

// continuityActivate publishes a version holding exactly the given rules (none: every
// rule disabled, so the published fact carries no rule at all).
func continuityActivate(t *testing.T, rp *ResolvedEventsProcessor, version string, at time.Duration, scoped ...runtime.ScopedRule) {
	t.Helper()
	a := runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@" + version, PublishedAt: testBase.Add(at)}
	require.NoError(t, rp.ProfileActiveStore.Upsert(context.Background(), &model.ProfileActive{
		Tenant: a.Tenant, ProfileToken: a.ProfileToken, ActiveVersionToken: a.ActiveVersionToken, PublishedAt: a.PublishedAt}))
	rp.applyRuleUpdate(ruleUpdate{upserts: scoped, active: &a})
}

// continuityRestart rebuilds the processor and its transient armer from the persisted
// row and the projections, replays msgs up to head, and reconciles.
func continuityRestart(t *testing.T, previous *ResolvedEventsProcessor, k int, head uint64, scoped []runtime.ScopedRule, msgs ...messaging.Message) (*ResolvedEventsProcessor, *derivedCapture) {
	t.Helper()
	previous.closeDetector()
	rp, w := handoffProcessor(t, k, scoped...)
	rp.closeDetector()
	rp.Store = previous.Store
	rp.RosterStore = previous.RosterStore
	rp.ProfileActiveStore = previous.ProfileActiveStore
	require.NoError(t, rp.restore(context.Background()))
	require.NoError(t, rp.startDeadmanGate(context.Background()))
	rp.Replay = &fakeReplayOpener{msgs: msgs, head: head}
	require.NoError(t, rp.replayToHead())
	require.NoError(t, rp.reconcileDeadmanArming(context.Background()))
	return rp, w
}

// requireSuccessorRaises applies a v-version breach at seq and requires it to publish a
// fresh Raised under that version: the action the carry must never swallow.
func requireSuccessorRaises(t *testing.T, rp *ResolvedEventsProcessor, w *derivedCapture, seq uint64, version, msg string) {
	t.Helper()
	before := len(w.events)
	handoffApply(t, rp, handoffMessage(t, seq, version, "dev1", "90", ""))
	require.True(t, rp.checkpoint(context.Background()))
	require.Len(t, w.events, before+1, msg)
	require.Equal(t, runtime.EdgeRaised, w.events[before].Edge, msg)
	require.Equal(t, "acme/p1@"+version+"/r1", w.events[before].RuleID, msg)
}

// Finding 1 (live): v1's raise is still unpublished when v2 is published, so the carry
// defers. v2 then reads HEALTHY. Being unlatched, it emits nothing, so no edge cancels
// the deferral, and once v1's raise publishes its latch would be carried onto a
// successor that has already been seen healthy.
func TestPublishDeferredCarryCancelledByHealthySuccessor(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeThreshold, true))
			handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
			require.Len(t, rp.pendingDets, 1)
			handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, true))
			v2 := dc.SeriesKey{Rule: "acme/p1@v2/r1", Series: "dev1"}
			require.False(t, rp.engine.IsRaised(v2), "pending v1 output defers the carry")
			handoffApply(t, rp, handoffMessage(t, 2, "v2", "dev1", "20", ""))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1, "only v1's own raise is owed")
			require.False(t, rp.engine.IsRaised(v2), "a successor already read healthy must stay unlatched")
			requireSuccessorRaises(t, rp, w, 3, "v2", "the first v2 breach after a healthy v2 reading must dispatch")
		})
	}
}

// Finding 1 (replay): the live run carried at the handoff and v2's healthy reading
// published a clear, then the process died before checkpointing. Replay from the
// pre-handoff checkpoint holds only that healthy v2 reading, which, with v2 unlatched,
// emits nothing for any fence to see.
func TestReplayHealthySuccessorPreventsPostReplayCarry(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			v1 := handoffRule(t, "v1", rules.TypeThreshold, false)
			v2 := handoffRule(t, "v2", rules.TypeThreshold, false)
			rp, w := handoffProcessor(t, k, v1)
			handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
			require.True(t, rp.checkpoint(context.Background()))
			handoffActivate(t, rp, v2)
			healthy := handoffMessage(t, 2, "v2", "dev1", "20", "")
			handoffApply(t, rp, healthy)
			rp.flushDetections(context.Background())
			require.Len(t, w.events, 2, "the live run cleared the alarm through v2")
			require.Equal(t, runtime.EdgeResolved, w.events[1].Edge)

			rp, w = continuityRestart(t, rp, k, 2, []runtime.ScopedRule{v1, v2}, healthy)
			requireSuccessorRaises(t, rp, w, 3, "v2", "a cleared alarm must raise again on the next v2 breach")
		})
	}
}

// Finding 3: the carry must not bridge a version in which the rule stopped existing as
// a threshold. v1 raises; v2 removes r1, disables every rule, or re-kinds r1; v3 brings
// a threshold r1 back under the original token. Its first breach is a new episode.
func TestCarryDoesNotBridgeDiscontinuousIntermediateVersion(t *testing.T) {
	intermediates := map[string]func(*testing.T) []runtime.ScopedRule{
		"removed": func(t *testing.T) []runtime.ScopedRule {
			return []runtime.ScopedRule{continuityRule(t, "v2", "r2", rules.TypeThreshold)}
		},
		"disabled": func(*testing.T) []runtime.ScopedRule { return nil },
		"absence": func(t *testing.T) []runtime.ScopedRule {
			return []runtime.ScopedRule{continuityRule(t, "v2", "r1", rules.TypeAbsence)}
		},
	}
	for name, v2 := range intermediates {
		for _, k := range []int{1, 4} {
			for _, restart := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/shards=%d/restart=%v", name, k, restart), func(t *testing.T) {
					v1 := continuityRule(t, "v1", "r1", rules.TypeThreshold)
					v3 := continuityRule(t, "v3", "r1", rules.TypeThreshold)
					rp, w := handoffProcessor(t, k, v1)
					handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
					require.True(t, rp.checkpoint(context.Background()))
					require.Len(t, w.events, 1)
					mid := v2(t)
					continuityActivate(t, rp, "v2", 10*time.Second, mid...)
					require.True(t, rp.checkpoint(context.Background()))
					continuityActivate(t, rp, "v3", 20*time.Second, v3)
					if restart {
						// Crash before the v3 handoff is checkpointed: the post-replay
						// reconcile sees every retained version at once.
						all := append([]runtime.ScopedRule{v1, v3}, mid...)
						rp, w = continuityRestart(t, rp, k, 1, all)
					}
					require.False(t, rp.engine.IsRaised(dc.SeriesKey{Rule: v3.Compiled.ID, Series: "dev1"}),
						"v1's latch must not reach v3 across a %s v2", name)
					requireSuccessorRaises(t, rp, w, 2, "v3", "the first v3 breach after a discontinuous v2 must dispatch")
				})
			}
		}
	}
}

// A version revisited after a kind break must not reuse its earlier carried latch,
// even when no reading ever evaluated that version during its first stint.
func TestRollbackAfterKindBreakDoesNotResurrectCarriedThreshold(t *testing.T) {
	for _, k := range []int{1, 4} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("shards=%d/restart=%v", k, restart), func(t *testing.T) {
				v1 := continuityRule(t, "v1", "r1", rules.TypeThreshold)
				v2 := continuityRule(t, "v2", "r1", rules.TypeThreshold)
				v3 := continuityRule(t, "v3", "r1", rules.TypeAbsence)
				rp, w := handoffProcessor(t, k, v1)
				handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
				require.True(t, rp.checkpoint(context.Background()))
				continuityActivate(t, rp, "v2", 10*time.Second, v2)
				require.True(t, rp.checkpoint(context.Background()))
				require.True(t, rp.engine.IsRaised(dc.SeriesKey{Rule: v2.Compiled.ID, Series: "dev1"}))
				continuityActivate(t, rp, "v3", 20*time.Second, v3)
				rp.engine.Advance(testBase.Add(2 * time.Hour))
				rp.drainDetections(testBase.Add(2*time.Hour), false)
				require.True(t, rp.checkpoint(context.Background()))
				require.Len(t, w.events, 2, "v3 absence must raise before recovery")
				require.Equal(t, runtime.EdgeRaised, w.events[1].Edge)
				handoffApply(t, rp, handoffMessage(t, 7201, "v3", "dev1", "20", ""))
				require.True(t, rp.checkpoint(context.Background()))
				require.Len(t, w.events, 3)
				require.Equal(t, runtime.EdgeResolved, w.events[2].Edge, "v3 recovery clears the stable alarm")
				if restart {
					rp, w = continuityRestart(t, rp, k, 7201, []runtime.ScopedRule{v1, v2, v3})
					require.Empty(t, w.events, "empty replay after the recovery checkpoint")
				}
				continuityActivate(t, rp, "v2", 7202*time.Second, v2)
				requireSuccessorRaises(t, rp, w, 7203, "v2", "rollback after a kind break must dispatch a fresh v2 breach")
			})
		}
	}
}

// Both the armer API and the persisted roster consumer path must exclude a device
// that left p1 before the publish, then returns to p1 under the new version.
func TestImmediateCarryExcludesDepartedDevice(t *testing.T) {
	for _, k := range []int{1, 4} {
		for _, persisted := range []bool{false, true} {
			t.Run(fmt.Sprintf("shards=%d/persisted=%v", k, persisted), func(t *testing.T) {
				rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeThreshold, false))
				handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
				require.True(t, rp.checkpoint(context.Background()))
				membership := func(profile string, at time.Duration) {
					e := runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: profile, ExpectedSince: testBase.Add(at)}
					if persisted {
						require.NoError(t, rp.RosterStore.Upsert(context.Background(), &model.DeviceRoster{
							Tenant: e.Tenant, DeviceToken: e.DeviceToken, ProfileToken: e.ProfileToken, ExpectedSince: e.ExpectedSince}))
						rp.applyArmRecheck(armUpdate{tenant: e.Tenant, deviceToken: e.DeviceToken})
					} else {
						rp.armer.ApplyDeviceMembership(e, true)
					}
				}
				membership("p2", 2*time.Second)
				handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, false))
				membership("p1", 11*time.Second)
				requireSuccessorRaises(t, rp, w, 12, "v2", "a returning device's first v2 breach must dispatch")
			})
		}
	}
}

// Clearing a destination latch must preserve its unpublished Raised and leave
// the next breach free to raise again. A duplicate is safer than a missed action.
func TestRollbackClearKeepsPendingDestinationOutput(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			v2 := handoffRule(t, "v2", rules.TypeThreshold, true)
			rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeThreshold, true))
			continuityActivate(t, rp, "v2", 10*time.Second, v2)
			handoffApply(t, rp, handoffMessage(t, 11, "v2", "dev1", "90", ""))
			require.Len(t, rp.pendingDets, 1)
			require.False(t, rp.pendingDets[0].early)
			pending := append([]pendingDetection(nil), rp.pendingDets...)
			continuityActivate(t, rp, "v3", 20*time.Second)
			rp.dirty = false
			continuityActivate(t, rp, "v2", 21*time.Second, v2)
			require.True(t, rp.dirty, "a destination clear must dirty the checkpoint even without a carry")
			require.False(t, rp.engine.IsRaised(dc.SeriesKey{Rule: v2.Compiled.ID, Series: "dev1"}))
			require.Equal(t, pending, rp.pendingDets, "the pending edge and its publication order survive the clear")
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1, "the unpublished raise is still owed")
			require.Equal(t, runtime.EdgeRaised, w.events[0].Edge)
			requireSuccessorRaises(t, rp, w, 22, "v2", "uncertain historical state must allow a duplicate raise")
			require.True(t, w.events[0].OccurredTime.Before(w.events[1].OccurredTime), "publication order must be preserved")
		})
	}
}

// Destination clearing also covers Absence, but only the current source may
// repopulate it. A continuous handoff, including rollback, still owes one recovery.
func TestRollbackAbsenceHandoffKeepsRecovery(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			v1 := continuityRule(t, "v1", "r1", rules.TypeAbsence)
			v2 := continuityRule(t, "v2", "r1", rules.TypeAbsence)
			rp, w := handoffProcessor(t, k, v1)
			require.NoError(t, rp.reconcileDeadmanArming(context.Background()))
			rp.engine.Advance(testBase.Add(2 * time.Hour))
			rp.drainDetections(testBase.Add(2*time.Hour), false)
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1)
			continuityActivate(t, rp, "v2", 7201*time.Second, v2)
			require.True(t, rp.checkpoint(context.Background()))
			continuityActivate(t, rp, "v1", 7202*time.Second, v1)
			rp.engine.Advance(testBase.Add(4 * time.Hour))
			rp.drainDetections(testBase.Add(4*time.Hour), false)
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1, "continuous absence handoffs must not repeat actions")
			handoffApply(t, rp, handoffMessage(t, 14401, "v1", "dev1", "20", ""))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 2)
			require.Equal(t, runtime.EdgeResolved, w.events[1].Edge, "the returned version must retain the recovery latch")
		})
	}
}

// An acknowledged old recovery clears the shared contributor, including when
// replay recreated the old latch. Its active successor must allow another raise.
func TestReplayOldRecoveryAllowsNextSuccessorRaise(t *testing.T) {
	for _, k := range []int{1, 4} {
		for _, liveRecovery := range []bool{false, true} {
			t.Run(fmt.Sprintf("shards=%d/liveRecovery=%v", k, liveRecovery), func(t *testing.T) {
				v1 := handoffRule(t, "v1", rules.TypeThreshold, false)
				v2 := handoffRule(t, "v2", rules.TypeThreshold, false)
				rp, _ := handoffProcessor(t, k, v1)
				handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
				require.True(t, rp.checkpoint(context.Background()))
				handoffActivate(t, rp, v2)
				require.True(t, rp.checkpoint(context.Background()))
				msgs := []messaging.Message{handoffMessage(t, 2, "v1", "dev1", "90", "")}
				head := uint64(2)
				if !liveRecovery {
					msgs = append(msgs, handoffMessage(t, 3, "v1", "dev1", "20", ""))
					head = 3
				}
				rp, w := continuityRestart(t, rp, k, head, []runtime.ScopedRule{v1, v2}, msgs...)
				if liveRecovery {
					handoffApply(t, rp, handoffMessage(t, 3, "v1", "dev1", "20", ""))
					require.True(t, rp.checkpoint(context.Background()))
				}
				require.Len(t, w.events, 2, "old raise and recovery must both publish")
				require.Equal(t, runtime.EdgeResolved, w.events[1].Edge)
				requireSuccessorRaises(t, rp, w, 4, "v2", "an acknowledged old clear must permit the next active raise")
			})
		}
	}
}

// Resolution and activation facts may arrive out of order. A retained rollback
// target can evaluate healthy before its activation reaches this processor.
func TestHealthyRollbackTargetBeforeActivationCancelsCarry(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			v1 := handoffRule(t, "v1", rules.TypeThreshold, false)
			v2 := handoffRule(t, "v2", rules.TypeThreshold, false)
			rp, w := handoffProcessor(t, k, v1)
			handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
			require.True(t, rp.checkpoint(context.Background()))
			continuityActivate(t, rp, "v2", 2*time.Second, v2)
			handoffApply(t, rp, handoffMessage(t, 3, "v2", "dev1", "20", ""))
			require.True(t, rp.checkpoint(context.Background()))
			continuityActivate(t, rp, "v1", 4*time.Second, v1)
			handoffApply(t, rp, handoffMessage(t, 5, "v1", "dev1", "90", ""))
			require.True(t, rp.checkpoint(context.Background()))
			handoffApply(t, rp, handoffMessage(t, 6, "v2", "dev1", "20", ""))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 3, "the early healthy v2 evaluation emits no edge")
			continuityActivate(t, rp, "v2", 6*time.Second, v2)
			requireSuccessorRaises(t, rp, w, 7, "v2", "a pre-activation evaluation must prevent a carry into that series")
		})
	}
}

// Persisted membership changes must end the previous membership's episode,
// including when a departure and return straddle a deferred or immediate carry.
func TestDeferredCarryDoesNotSurviveMembershipRoundTrip(t *testing.T) {
	for _, k := range []int{1, 4} {
		for _, deferred := range []bool{false, true} {
			for _, change := range []string{"profile", "delete", "since", "redelivery"} {
				t.Run(fmt.Sprintf("shards=%d/deferred=%v/%s", k, deferred, change), func(t *testing.T) {
					rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeThreshold, true))
					handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
					if deferred {
						handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, true))
					} else {
						require.True(t, rp.checkpoint(context.Background()))
					}
					membership := func(profile string, at time.Duration) {
						require.NoError(t, rp.RosterStore.Upsert(context.Background(), &model.DeviceRoster{
							Tenant: "acme", DeviceToken: "dev1", ProfileToken: profile, ExpectedSince: testBase.Add(at)}))
						rp.applyArmRecheck(armUpdate{tenant: "acme", deviceToken: "dev1"})
					}
					switch change {
					case "profile":
						membership("p2", 11*time.Second)
						membership("p1", 12*time.Second)
					case "delete":
						require.NoError(t, rp.RosterStore.Delete(context.Background(), "acme", "dev1", testBase.Add(11*time.Second)))
						rp.applyArmRecheck(armUpdate{tenant: "acme", deviceToken: "dev1"})
						membership("p1", 12*time.Second)
					case "since":
						membership("p1", 12*time.Second)
					case "redelivery":
						membership("p1", 0)
					}
					if !deferred {
						continuityActivate(t, rp, "v2", 13*time.Second, handoffRule(t, "v2", rules.TypeThreshold, true))
					}
					require.True(t, rp.checkpoint(context.Background()))
					next := dc.SeriesKey{Rule: "acme/p1@v2/r1", Series: "dev1"}
					if change == "redelivery" {
						require.True(t, rp.engine.IsRaised(next), "unchanged membership must preserve carry")
						handoffApply(t, rp, handoffMessage(t, 14, "v2", "dev1", "90", ""))
						require.True(t, rp.checkpoint(context.Background()))
						require.Len(t, w.events, 1, "redelivery must not duplicate the raise")
					} else {
						require.False(t, rp.engine.IsRaised(next), "a previous membership must never seed the successor")
						requireSuccessorRaises(t, rp, w, 14, "v2", "the returning membership must dispatch its first breach")
					}
				})
			}
		}
	}
}

// The production published-rule definition contains the REACT actions. Changing
// their alarm destinations must allow the first breach to dispatch anew.
func TestCarryNotAcrossAlarmKeyChange(t *testing.T) {
	for _, k := range []int{1, 4} {
		for _, change := range []string{"key", "add", "remove", "template", "guard", "unknown", "severity", "non-alarm", "default", "unchanged-default", "order"} {
			t.Run(fmt.Sprintf("shards=%d/%s", k, change), func(t *testing.T) {
				alarm := func(key, template, guard string) rules.Action {
					return rules.Action{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: key, AlarmKeyTemplate: template}, Guard: guard}
				}
				oldActions := []rules.Action{alarm("alarm-a", "", "")}
				newActions := []rules.Action{alarm("alarm-b", "", "")}
				switch change {
				case "add":
					oldActions = nil
				case "remove":
					newActions = nil
				case "template":
					oldActions = []rules.Action{alarm("", `"alarm-a-" + series`, "")}
					newActions = []rules.Action{alarm("", `"alarm-b-" + series`, "")}
				case "guard":
					oldActions = []rules.Action{alarm("alarm-a", "", "value > 95.0")}
					newActions = []rules.Action{alarm("alarm-a", "", "")}
				case "severity", "unknown":
					newActions = oldActions
				case "non-alarm":
					newActions = append([]rules.Action{oldActions[0]}, rules.Action{Type: rules.ActionSendCommand, SendCommand: &rules.SendCommandAction{Command: "fan"}})
				case "default":
					oldActions = []rules.Action{alarm("", "", "")}
					newActions = []rules.Action{alarm("r1", "", "")}
				case "unchanged-default":
					oldActions = []rules.Action{alarm("", "", "")}
					newActions = oldActions
				case "order":
					oldActions = []rules.Action{alarm("alarm-a", "", ""), alarm("alarm-b", "", "")}
					newActions = []rules.Action{oldActions[1], oldActions[0]}
				}
				published := func(version string, actions []rules.Action) runtime.ScopedRule {
					threshold := 80.0
					severity := rules.SeverityWarning
					if version == "v2" && change == "severity" {
						severity = rules.SeverityMajor
					}
					body, err := json.Marshal(rules.Rule{Name: "heat", Type: rules.TypeThreshold, Severity: severity,
						When: rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: &threshold}, Actions: actions})
					require.NoError(t, err)
					scoped, failed := runtime.CompilePublishedRules("acme", "p1@"+version, []dmmodel.PublishedDetectionRule{{Token: "r1", Definition: string(body)}})
					require.Zero(t, failed)
					require.Len(t, scoped, 1)
					return scoped[0]
				}
				v1, v2 := published("v1", oldActions), published("v2", newActions)
				if change == "unknown" {
					v2.Compiled.AlarmCarryActions = nil
				}
				rp, w := handoffProcessor(t, k, v1)
				handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
				require.True(t, rp.checkpoint(context.Background()))
				handoffActivate(t, rp, v2)
				switch change {
				case "severity", "non-alarm", "unchanged-default", "order":
					handoffApply(t, rp, handoffMessage(t, 11, "v2", "dev1", "90", ""))
					require.True(t, rp.checkpoint(context.Background()))
					require.Len(t, w.events, 1, "unchanged alarm destinations may carry")
					handoffApply(t, rp, handoffMessage(t, 12, "v2", "dev1", "20", ""))
					require.True(t, rp.checkpoint(context.Background()))
					require.Len(t, w.events, 2)
					require.Equal(t, runtime.EdgeResolved, w.events[1].Edge)
				default:
					requireSuccessorRaises(t, rp, w, 11, "v2", "changed alarm actions must dispatch on the first v2 breach")
				}
			})
		}
	}
}

// Departure clears old latches, but another late event may still legitimately
// raise the old rule before the handoff. Membership must independently gate carry.
func TestImmediateCarryExcludesLateDepartedDevice(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeThreshold, false))
			require.NoError(t, rp.RosterStore.Upsert(context.Background(), &model.DeviceRoster{
				Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p2", ExpectedSince: testBase.Add(time.Second)}))
			rp.applyArmRecheck(armUpdate{tenant: "acme", deviceToken: "dev1"})
			handoffApply(t, rp, handoffMessage(t, 2, "v1", "dev1", "90", ""))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1, "a late old-version event still owes its action")
			handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, false))
			require.NoError(t, rp.RosterStore.Upsert(context.Background(), &model.DeviceRoster{
				Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase.Add(11 * time.Second)}))
			rp.applyArmRecheck(armUpdate{tenant: "acme", deviceToken: "dev1"})
			requireSuccessorRaises(t, rp, w, 12, "v2", "late output from a departed membership must not seed the successor")
		})
	}
}

func TestAbsenceHandoffNotAcrossAlarmKeyChange(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			published := func(version, key string) runtime.ScopedRule {
				scoped, failed := runtime.CompilePublishedRules("acme", "p1@"+version, []dmmodel.PublishedDetectionRule{{Token: "r1",
					Definition: fmt.Sprintf(`{"name":"silent","type":"absence","timeout":"1h","severity":"warning","actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":%q}}]}`, key)}})
				require.Zero(t, failed)
				return scoped[0]
			}
			v1, v2 := published("v1", "alarm-a"), published("v2", "alarm-b")
			rp, w := handoffProcessor(t, k, v1)
			require.NoError(t, rp.reconcileDeadmanArming(context.Background()))
			rp.engine.Advance(testBase.Add(2 * time.Hour))
			rp.drainDetections(testBase.Add(2*time.Hour), false)
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1)
			continuityActivate(t, rp, "v2", 7201*time.Second, v2)
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 2, "the old alarm key must receive its disarm recovery")
			require.Equal(t, runtime.EdgeResolved, w.events[1].Edge)
			rp.engine.Advance(testBase.Add(4 * time.Hour))
			rp.drainDetections(testBase.Add(4*time.Hour), false)
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 3, "the new absence alarm key must receive a raise")
			require.Equal(t, runtime.EdgeRaised, w.events[2].Edge)
			require.Equal(t, v2.Compiled.ID, w.events[2].RuleID)
		})
	}
}

// --- version-compatibility checkpoints ------------------------------------------------
//
// testdata/carry_v17 holds checkpoints written by the ACTUAL v1.7 code (origin/release-1.7)
// and by this branch, produced by testdata/carry_v17/generate_v17_test.go.txt (see its
// header) and TestGenerateV18CarryFixtures below. Decoding is not the claim: each test
// restores the checkpoint, and requires the next successor breach to dispatch.

type carryFixture struct {
	Producer  string          `json:"producer"`
	Scenario  string          `json:"scenario"`
	Shards    int             `json:"shards"`
	StreamSeq int64           `json:"streamSeq"`
	Watermark time.Time       `json:"watermark"`
	Payload   json.RawMessage `json:"payload"`
	Events    []string        `json:"events"`
}

func loadCarryFixture(t *testing.T, name string) carryFixture {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "carry_v17", name))
	require.NoError(t, err)
	var f carryFixture
	require.NoError(t, json.Unmarshal(b, &f))
	return f
}

// restoreCarryFixture builds a v1.8 processor over a checkpoint row written by another
// binary, with v1 and v2 registered and v2 active, and replays msgs to head.
func restoreCarryFixture(t *testing.T, k int, f carryFixture, head uint64, msgs ...messaging.Message) (*ResolvedEventsProcessor, *derivedCapture) {
	t.Helper()
	ctx := context.Background()
	rp, w := handoffProcessor(t, k,
		handoffRule(t, "v1", rules.TypeThreshold, false),
		handoffRule(t, "v2", rules.TypeThreshold, false))
	rp.closeDetector()
	require.NoError(t, rp.Store.Save(ctx, &model.DetectSnapshot{
		PartitionId: "singleton", StreamSeq: f.StreamSeq, Watermark: f.Watermark, Payload: []byte(f.Payload)}))
	require.NoError(t, rp.ProfileActiveStore.Upsert(ctx, &model.ProfileActive{
		Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v2", PublishedAt: testBase.Add(10 * time.Second)}))
	require.NoError(t, rp.restore(ctx))
	require.NoError(t, rp.startDeadmanGate(ctx))
	rp.Replay = &fakeReplayOpener{msgs: msgs, head: head}
	require.NoError(t, rp.replayToHead())
	require.NoError(t, rp.reconcileDeadmanArming(ctx))
	return rp, w
}

// Finding 2: a v1.7 lease holder took the v2 publish, saw v2 raise and recover (or only
// ever read healthy), and checkpointed with v1's historical latch still raised. v1.8
// takes the lease at the stream head: a zero-message replay leaves no fence, and the
// restored v1 latch must not be read as an unfinished handoff.
func TestV17CheckpointRestoresWithoutCarry(t *testing.T) {
	for _, scenario := range []string{"v17-successor-recovered", "v17-successor-healthy", "v17-from-v18"} {
		for _, writer := range []int{1, 4} {
			for _, k := range []int{1, 4} {
				t.Run(fmt.Sprintf("%s/written-k%d/read-k%d", scenario, writer, k), func(t *testing.T) {
					f := loadCarryFixture(t, fmt.Sprintf("%s-k%d.json", scenario, writer))
					require.Equal(t, "v1.7", f.Producer)
					var snap struct {
						Raised []struct{ Rule, Series string } `json:"raised"`
					}
					require.NoError(t, json.Unmarshal(f.Payload, &snap))
					require.Equal(t, []struct{ Rule, Series string }{{"acme/p1@v1/r1", "dev1"}}, snap.Raised,
						"fixture premise: v1.7 left v1's historical latch raised and v2 clear")
					rp, w := restoreCarryFixture(t, k, f, uint64(f.StreamSeq))
					require.Empty(t, w.events, "zero-message replay")
					require.False(t, rp.engine.IsRaised(dc.SeriesKey{Rule: "acme/p1@v2/r1", Series: "dev1"}))
					requireSuccessorRaises(t, rp, w, uint64(f.StreamSeq)+1, "v2", "the next v2 breach after a v1.7 checkpoint must dispatch")
				})
			}
		}
	}
}

// The v1.8 side of v1.8 -> v1.7 -> v1.8: v1.7 (in TestV17CheckpointRestoresWithoutCarry,
// scenario v17-from-v18) restored this branch's pre-handoff checkpoint. Here the same
// pre-handoff checkpoint restores in v1.8 itself, with a non-empty replay holding a
// recovered successor episode, and with a zero-message replay.
func TestV18PreHandoffCheckpointReplay(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprintf("recovered-in-replay/%d", k), func(t *testing.T) {
			f := loadCarryFixture(t, "v18-pre-handoff-k1.json")
			require.Equal(t, "v1.8", f.Producer)
			rp, w := restoreCarryFixture(t, k, f, 3,
				handoffMessage(t, 2, "v2", "dev1", "90", ""),
				handoffMessage(t, 3, "v2", "dev1", "20", ""))
			require.Len(t, w.events, 2)
			requireSuccessorRaises(t, rp, w, 4, "v2", "a successor that recovered in replay must raise again")
		})
		t.Run(fmt.Sprintf("healthy-in-replay/%d", k), func(t *testing.T) {
			f := loadCarryFixture(t, "v18-pre-handoff-k1.json")
			rp, w := restoreCarryFixture(t, k, f, 2, handoffMessage(t, 2, "v2", "dev1", "20", ""))
			requireSuccessorRaises(t, rp, w, 3, "v2", "a successor read healthy in replay must raise on its next breach")
		})
	}
}

// TestGenerateV18CarryFixtures writes this branch's pre-handoff checkpoint, which the
// v1.7 generator then restores. It runs only when DC_CARRY_FIXTURE_DIR is set.
func TestGenerateV18CarryFixtures(t *testing.T) {
	dir := os.Getenv("DC_CARRY_FIXTURE_DIR")
	if dir == "" {
		t.Skip("DC_CARRY_FIXTURE_DIR not set")
	}
	rp, w := handoffProcessor(t, 1, handoffRule(t, "v1", rules.TypeThreshold, false))
	handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
	require.True(t, rp.checkpoint(context.Background()))
	snap, found, err := rp.Store.Load(context.Background(), "singleton")
	require.NoError(t, err)
	require.True(t, found)
	f := carryFixture{Producer: "v1.8", Scenario: "v18-pre-handoff", Shards: 1, StreamSeq: snap.StreamSeq,
		Watermark: snap.Watermark.UTC(), Payload: json.RawMessage(snap.Payload)}
	for _, e := range w.events {
		f.Events = append(f.Events, fmt.Sprintf("%s %s", e.RuleID, e.Edge))
	}
	b, err := json.MarshalIndent(f, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "v18-pre-handoff-k1.json"), append(b, '\n'), 0o644))
}
