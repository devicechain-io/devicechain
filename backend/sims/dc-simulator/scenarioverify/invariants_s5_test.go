// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"github.com/devicechain-io/dc-simulator/sim"
	"strings"
	"testing"
	"time"
)

func (d *day) cleanS5() {
	token, digest, size := sim.HarborpulseFirmwareIdentity()
	signer := OTASigner{Token: "hp-signer", KeyID: "public-key-id", State: "ACTIVE"}
	s := S5{Ledger: &sim.ScenarioS5{OTA: sim.ScenarioOTA{Artifact: token, Digest: digest, Version: sim.HarborpulseFirmwareVersion, SizeBytes: size, Signer: signer.Token, KeyID: signer.KeyID, Rollout: "hp-rollout", Group: sim.HarborpulseAllChargersGroup, MaxInFlight: 2, FailureThreshold: 2, ScheduleHour: sim.HarborpulseRolloutHour}},
		Artifact: &OTAArtifact{Token: token, Digest: digest, Version: sim.HarborpulseFirmwareVersion, Component: "firmware", SizeBytes: "49152", State: "AVAILABLE", TrustStatus: "SIGNED", CompatibleProfiles: []string{sim.HarborpulseChargerProfile}, Signatures: []OTASignature{{Signer: signer, CountsTowardTrust: true}}}, Signer: &signer,
		Rollout: &OTARollout{Token: "hp-rollout", State: "COMPLETED", GroupToken: sim.HarborpulseAllChargersGroup, MaxInFlight: 2, FailureThreshold: 2, TargetCount: 6, Progress: OTAProgress{Total: 6, Succeeded: 5, Failed: 1}}, Assignments: map[string]*OTAAssignment{}}
	s.Rollout.Artifact.Token = token
	for i, device := range sim.HarborpulseChargers() {
		assignment := &OTAAssignment{Token: "a-" + device, DeviceToken: device, SignedBy: &signer}
		assignment.Attempt.State = "UPDATED"
		assignment.Attempt.TargetVersion = sim.HarborpulseFirmwareVersion
		from := at(26).Add(time.Duration(i/2) * 10 * time.Second)
		assignment.Attempt.LastReportAt = from.Add(10 * time.Second).Format(time.RFC3339Nano)
		outcome := "SUCCEEDED"
		if device == sim.HarborpulseFailedCharger {
			outcome = "FAILED"
			assignment.Attempt.State = "FAILED"
			assignment.Attempt.Failure = &struct {
				Stage, Code, Detail string
				Platform            bool
			}{Stage: "VERIFYING", Code: "VERIFICATION_FAILED"}
		}
		s.Assignments[assignment.Token] = assignment
		s.Targets = append(s.Targets, OTATarget{DeviceToken: device, Outcome: outcome, AssignmentToken: assignment.Token, AttemptState: assignment.Attempt.State, DispatchedAt: from.Format(time.RFC3339Nano)})
	}
	s.Ledger.OTA.CreatedAt = at(26).Format(time.RFC3339Nano)
	s.Ledger.OTA.StartedAt = at(26).Format(time.RFC3339Nano)
	s.Rollout.StartedAt = s.Ledger.OTA.StartedAt
	d.s5 = s
	d.cleanRestore()
	d.cleanReplacement()
}

func (d *day) cleanRestore() {
	l := sim.ScenarioRestoreDrain{Day: 0, Refused429: 2, FirstRefusalAt: at(21.25).Add(time.Second).Format(time.RFC3339Nano), CaughtUpAt: at(21.25).Add(25 * time.Second).Format(time.RFC3339Nano)}
	for i := range d.backfill {
		b := &d.backfill[i]
		for j, taken := range b.Taken {
			value := 3.7 + float64(j)/100
			b.PowerKW = append(b.PowerKW, value)
			b.PowerStored = append(b.PowerStored, Reading{Name: sim.HarborpulseChargerPowerKey, At: taken, Value: value})
		}
		// The HTTP tick spends 108, leaving 42/150 tokens. Ten four-value
		// restores fit; the eleventh receives a real 429 and is retried next tick.
		l.Refused = append(l.Refused, sim.ScenarioRestoreRefused{Device: b.Device, Times: []string{b.Taken[10].Format(time.RFC3339Nano)}})
		l.Offered += b.Buffered
		l.Accepted += b.Buffered
	}
	d.s5.Ledger.RestoreDrains = []sim.ScenarioRestoreDrain{l}
}

func TestEachS5BurstControlModelsLiveEvidence(t *testing.T) {
	t.Run(sim.HarborpulseDefectBurstUnthrottled, func(t *testing.T) {
		evaluateOneRed(t, InvBurstRateLimited, "no real ingress 429", func(d *day) {
			l := &d.s5.Ledger.RestoreDrains[0]
			l.Refused429 = 0
			l.FirstRefusalAt = ""
			l.Refused = nil
			l.CaughtUpAt = at(21.25).Add(time.Second).Format(time.RFC3339Nano)
		})
	})
	t.Run(sim.HarborpulseDefectBurstDropOn429, func(t *testing.T) {
		evaluateOneRed(t, InvBurstCaughtUp, "stored 0 times", func(d *day) {
			// Only the FIRST 429 entry of the day's whole drain is discarded.
			b := &d.backfill[0]
			b.Stored = append(b.Stored[:10], b.Stored[11:]...)
			b.PowerStored = append(b.PowerStored[:10], b.PowerStored[11:]...)
			d.s5.Ledger.RestoreDrains[0].Accepted--
		})
	})
	t.Run(sim.HarborpulseDefectDropBackfill, func(t *testing.T) {
		evaluateOneRed(t, InvBackfillStored, "missing", func(d *day) {
			l := &d.s5.Ledger.RestoreDrains[0]
			for i := range d.backfill {
				b := &d.backfill[i]
				var stored []time.Time
				var rows []Reading
				for j, stamp := range b.Stored {
					if (j+1)%5 != 0 {
						stored = append(stored, stamp)
						rows = append(rows, b.PowerStored[j])
					} else {
						l.Offered--
						l.Accepted--
					}
				}
				b.Stored, b.PowerStored = stored, rows
				// Its first ten offers now skip the lost fifth/tenth samples;
				// the eleventh offer is original sample 13, which was retained.
				l.Refused[i].Times = []string{b.Taken[12].Format(time.RFC3339Nano)}
			}
		})
	})
}

func TestS5BurstStoreDeadlineAndRateGuards(t *testing.T) {
	for name, mutate := range map[string]func(*day){
		"duplicate": func(d *day) {
			b := &d.backfill[0]
			b.Stored = append(b.Stored, b.Stored[0])
			b.PowerStored = append(b.PowerStored, b.PowerStored[0])
		},
		"wrong value": func(d *day) { d.backfill[0].PowerStored[10].Value = 99 },
		"late": func(d *day) {
			d.s5.Ledger.RestoreDrains[0].CaughtUpAt = at(21.25).Add(restoreDeadline + time.Second).Format(time.RFC3339Nano)
		},
		"dead letter": func(d *day) { d.s5.Ledger.RestoreDrains[0].DeadLettered = 1 },
	} {
		t.Run(name, func(t *testing.T) { evaluateOneRed(t, InvBurstCaughtUp, "", mutate) })
	}
	evaluateOneRed(t, InvBurstRateLimited, "mis-sized", func(d *day) { d.s5.Ledger.RestoreDrains[0].OrdinaryShed = 1 })
	evaluateOneRed(t, InvBurstRateLimited, "backpressured=9", func(d *day) { l := &d.s5.Ledger.RestoreDrains[0]; l.Refused429 = 0; l.Backpressured = 9 })
}

// evaluateRed models unavoidable collateral without weakening evaluateOneRed.
func evaluateRed(t *testing.T, want, because string, alsoRed []string, mutate func(*day)) {
	t.Helper()
	d := cleanDay()
	mutate(d)
	red := setOf(append([]string{want}, alsoRed...))
	for _, inv := range Evaluate(d.observe()) {
		if red[inv.Name] == inv.Passed {
			t.Fatalf("%s passed=%v (%s), expected reds %v", inv.Name, inv.Passed, inv.Detail, sortedKeys(red))
		}
		if inv.Name == want && !strings.Contains(inv.Detail, because) {
			t.Fatalf("%s failed for wrong reason: %s", want, inv.Detail)
		}
	}
}

func TestEachS5OTAControlModelsLiveEvidence(t *testing.T) {
	t.Run(sim.HarborpulseDefectOTASignatureOmitted, func(t *testing.T) {
		evaluateRed(t, InvOTAArtifactTrusted, "UNVERIFIED", []string{InvOTATargetsExact, InvOTAConcurrency, InvOTAOutcomes}, func(d *day) {
			d.s5.Ledger.OTA.RefusalCode = "UNVERIFIED_ARTIFACT_REFUSED"
			d.s5.Ledger.OTA.CreatedAt = ""
			d.s5.Ledger.OTA.StartedAt = ""
			d.s5.Artifact.TrustStatus = "UNVERIFIED"
			d.s5.Artifact.Signatures = nil
			d.s5.Rollout = nil
			d.s5.Targets = nil
			d.s5.Assignments = nil
			d.s5.Samples = nil
		})
	})
	t.Run(sim.HarborpulseDefectOTAGroupMissingCharger, func(t *testing.T) {
		evaluateOneRed(t, InvOTATargetsExact, "frozen target count", func(d *day) {
			var targets []OTATarget
			for _, target := range d.s5.Targets {
				if target.DeviceToken != sim.HarborpulseMissingCharger {
					targets = append(targets, target)
				} else {
					delete(d.s5.Assignments, target.AssignmentToken)
				}
			}
			d.s5.Targets = targets
			d.s5.Rollout.TargetCount = 5
			d.s5.Rollout.Progress.Total = 5
			d.s5.Rollout.Progress.Succeeded = 4
		})
	})
	t.Run(sim.HarborpulseDefectOTACapThree, func(t *testing.T) {
		evaluateOneRed(t, InvOTAConcurrency, "maxInFlight=3", func(d *day) {
			d.s5.Ledger.OTA.MaxInFlight = 3
			d.s5.Rollout.MaxInFlight = 3
			for i := range d.s5.Targets {
				from := at(26).Add(time.Duration(i/3) * 10 * time.Second)
				target := &d.s5.Targets[i]
				target.DispatchedAt = from.Format(time.RFC3339Nano)
				d.s5.Assignments[target.AssignmentToken].Attempt.LastReportAt = from.Add(10 * time.Second).Format(time.RFC3339Nano)
			}
			d.s5.LiveSampled = true
			d.s5.SampleFrom = at(26)
			d.s5.SampleTo = at(26).Add(20 * time.Second)
			for now := d.s5.SampleFrom; !now.After(d.s5.SampleTo); now = now.Add(time.Second) {
				inFlight := 3
				if now.Equal(d.s5.SampleTo) {
					inFlight = 0
				}
				d.s5.Samples = append(d.s5.Samples, OTASample{At: now, InFlight: inFlight})
			}
			if peak, err := otaPeak(d.s5); err != nil || peak != 3 {
				t.Fatalf("cap-three fixture peak %d, %v", peak, err)
			}
		})
	})
	t.Run(sim.HarborpulseDefectOTAFailureDisabled, func(t *testing.T) {
		evaluateOneRed(t, InvOTAOutcomes, "north-1", func(d *day) {
			for i := range d.s5.Targets {
				target := &d.s5.Targets[i]
				if target.DeviceToken == sim.HarborpulseFailedCharger {
					target.Outcome = "SUCCEEDED"
					target.AttemptState = "UPDATED"
					a := d.s5.Assignments[target.AssignmentToken]
					a.Attempt.State = "UPDATED"
					a.Attempt.Failure = nil
				}
			}
			d.s5.Rollout.Progress.Succeeded = 6
			d.s5.Rollout.Progress.Failed = 0
		})
	})
}

func TestS5TimelineCompletesBeforeDispatchAtEqualInstant(t *testing.T) {
	s := cleanDay().s5
	// Each pair finishes at the exact instant the next pair starts. Sorting dispatches first
	// would falsely reach 3 (or 4). Offset spellings of the same instant must compare equal too.
	s.Targets[2].DispatchedAt = at(26).Add(10 * time.Second).In(time.FixedZone("east", 3600)).Format(time.RFC3339Nano)
	if peak, err := otaPeak(s); err != nil || peak != 2 {
		t.Fatalf("peak=%d, err=%v", peak, err)
	}
	if err := otaConcurrency(Observations{S5: s}); err != nil {
		t.Fatal(err)
	}
}

func TestS5LiveSamplesAbsentVersusGappy(t *testing.T) {
	s := cleanDay().s5
	if err := S5Coverage(s); err != nil {
		t.Fatal("historical, not sampled:", err)
	}
	s.LiveSampled = true
	s.SampleFrom = at(26)
	s.SampleTo = at(26).Add(10 * time.Second)
	if err := S5Coverage(s); err == nil {
		t.Fatal("requested live sampling with no samples claimed a bound")
	}
	s.Samples = []OTASample{{At: s.SampleFrom, InFlight: 2}, {At: s.SampleTo, InFlight: 0}}
	if err := S5Coverage(s); err == nil {
		t.Fatal("gappy sampling claimed a bound")
	}
	s.Samples = nil
	for now := s.SampleFrom; !now.After(s.SampleTo); now = now.Add(time.Second) {
		s.Samples = append(s.Samples, OTASample{At: now, InFlight: 2})
	}
	if err := S5Coverage(s); err != nil {
		t.Fatal(err)
	}
	s.Samples[3].InFlight = 3
	if err := otaConcurrency(Observations{S5: s}); err == nil {
		t.Fatal("live cap violation was ignored")
	}
}

func TestS5TrustedArtifactAndAssignmentGuards(t *testing.T) {
	tests := map[string]func(*S5){
		"digest":    func(s *S5) { s.Artifact.Digest = "sha256:wrong" },
		"size":      func(s *S5) { s.Artifact.SizeBytes = "0" },
		"component": func(s *S5) { s.Artifact.Component = "other" },
		"profile":   func(s *S5) { s.Artifact.CompatibleProfiles = []string{"other"} },
		"state":     func(s *S5) { s.Artifact.State = "UPLOADING" },
		"signer":    func(s *S5) { s.Signer.State = "REVOKED" },
		"signature": func(s *S5) { s.Artifact.Signatures = nil },
		"pin": func(s *S5) {
			for _, a := range s.Assignments {
				a.SignedBy = nil
				break
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := cleanDay().s5
			mutate(&s)
			if err := otaArtifactTrusted(Observations{S5: s}); err == nil {
				t.Fatal("untrusted evidence passed")
			}
		})
	}
}

func TestS5StuckRolloutFailsAndProgressMustMatch(t *testing.T) {
	s := cleanDay().s5
	s.Rollout.State = "RUNNING"
	if err := otaOutcomes(Observations{S5: s}); err == nil {
		t.Fatal("answering but stuck rollout passed")
	}
	s.Rollout.State = "COMPLETED"
	s.Rollout.Progress.Pending = 1
	if err := otaOutcomes(Observations{S5: s}); err == nil {
		t.Fatal("inconsistent progress passed")
	}
}

func TestS5NorthFailureRequiresVerificationCode(t *testing.T) {
	s := cleanDay().s5
	for _, a := range s.Assignments {
		if a.DeviceToken == sim.HarborpulseFailedCharger {
			a.Attempt.Failure.Code = "OTHER_FAILURE"
		}
	}
	if err := otaOutcomes(Observations{S5: s}); err == nil {
		t.Fatal("north-1 failure code was not checked")
	}
}

func TestS5BackfillRejectsInventedReadingOutsideTakenSpan(t *testing.T) {
	evaluateOneRed(t, InvBackfillStored, "never took", func(d *day) {
		b := &d.backfill[0]
		b.Stored = append(b.Stored, at(sim.HarborpulseOutageFrom).Add(time.Microsecond))
	})
}
