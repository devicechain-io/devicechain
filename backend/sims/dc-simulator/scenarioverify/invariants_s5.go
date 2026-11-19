// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-simulator/otafleet"
	"github.com/devicechain-io/dc-simulator/sim"
)

const (
	InvOTAArtifactTrusted = "ota-artifact-trusted"
	InvOTATargetsExact    = "ota-targets-exact"
	InvOTAConcurrency     = "ota-concurrency"
	InvOTAOutcomes        = "ota-outcomes"
	InvBurstRateLimited   = "burst-rate-limited"
	InvBurstCaughtUp      = "burst-caught-up"
)

func init() {
	Controls[InvOTAArtifactTrusted] = sim.HarborpulseDefectOTASignatureOmitted
	Controls[InvOTATargetsExact] = sim.HarborpulseDefectOTAGroupMissingCharger
	Controls[InvOTAConcurrency] = sim.HarborpulseDefectOTACapThree
	Controls[InvOTAOutcomes] = sim.HarborpulseDefectOTAFailureDisabled
	Controls[InvBurstRateLimited] = sim.HarborpulseDefectBurstUnthrottled
	Controls[InvBurstCaughtUp] = sim.HarborpulseDefectBurstDropOn429
}

func s5Checks() []struct {
	name string
	fn   func(Observations) error
} {
	return []struct {
		name string
		fn   func(Observations) error
	}{
		{InvOTAArtifactTrusted, otaArtifactTrusted}, {InvOTATargetsExact, otaTargetsExact},
		{InvOTAConcurrency, otaConcurrency}, {InvOTAOutcomes, otaOutcomes},
		{InvBurstRateLimited, burstRateLimited}, {InvBurstCaughtUp, burstCaughtUp},
	}
}

// Two wall minutes allow 24 retry opportunities at the default cadence. The measured
// burst needs about five ordinary-tick headrooms to drain, leaving ample room for
// ingress latency without silently accepting a queue stuck for the rest of the day.
const restoreDeadline = 2 * time.Minute

func restoreDay(o Observations) *sim.ScenarioRestoreDrain {
	if o.S5.Ledger != nil {
		for i := range o.S5.Ledger.RestoreDrains {
			if o.S5.Ledger.RestoreDrains[i].Day == o.Day {
				return &o.S5.Ledger.RestoreDrains[i]
			}
		}
	}
	return nil
}

func restoreRefused(o Observations, device string) map[int64]bool {
	out := map[int64]bool{}
	if l := restoreDay(o); l != nil {
		for _, d := range l.Refused {
			if d.Device == device {
				for _, stamp := range d.Times {
					if at, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
						out[at.UnixMicro()] = true
					}
				}
			}
		}
	}
	return out
}

func burstRateLimited(o Observations) error {
	l := restoreDay(o)
	if l == nil {
		return fmt.Errorf("no restore drain ledger for sim day %d", o.Day)
	}
	if l.OrdinaryShed != 0 {
		return fmt.Errorf("ordinaryShed=%d: tier is mis-sized and sheds ordinary traffic", l.OrdinaryShed)
	}
	if l.Refused429 < 1 {
		return fmt.Errorf("no real ingress 429: refused429=%d, backpressured=%d (503s are separate), ambiguous=%d", l.Refused429, l.Backpressured, l.Ambiguous)
	}
	return nil
}

func burstCaughtUp(o Observations) error {
	l := restoreDay(o)
	if l == nil {
		return fmt.Errorf("no restore drain ledger for sim day %d", o.Day)
	}
	if l.DeadLettered != 0 {
		return fmt.Errorf("restore deadLettered=%d", l.DeadLettered)
	}
	if l.Offered < 1 {
		return fmt.Errorf("restore offered no entries")
	}
	if err := validateRestoreLedger(o.S5.Ledger); err != nil {
		return err
	}
	known := setOf(fleetOf(sim.HarborpulseChargerType, sim.HarborpulseOutageQuay))
	refusedCount := 0
	for _, d := range l.Refused {
		if !known[d.Device] {
			return fmt.Errorf("refused entry for unexpected device %s", d.Device)
		}
		refusedCount += len(d.Times)
	}
	if l.Refused429 > 0 && refusedCount == 0 {
		return fmt.Errorf("429 drain has no refused taken times")
	}
	end := o.at(sim.HarborpulseOutageTo)
	caught, err := time.Parse(time.RFC3339Nano, l.CaughtUpAt)
	if err != nil || caught.Before(end) || caught.After(end.Add(restoreDeadline)) {
		return fmt.Errorf("restore did not catch up by %s (caughtUpAt=%q)", end.Add(restoreDeadline).Format(time.RFC3339Nano), l.CaughtUpAt)
	}
	for _, device := range fleetOf(sim.HarborpulseChargerType, sim.HarborpulseOutageQuay) {
		var b *Backfill
		for i := range o.Backfill {
			if o.Backfill[i].Device == device {
				b = &o.Backfill[i]
				break
			}
		}
		if b == nil {
			return fmt.Errorf("no taken/store evidence for %s", device)
		}
		counts := map[int64]int{}
		for _, at := range b.Stored {
			if at.Before(o.at(sim.HarborpulseOutageFrom)) || !at.Before(end) {
				continue
			}
			counts[at.UnixMicro()]++
			if counts[at.UnixMicro()] > 1 {
				return fmt.Errorf("%s duplicate stored power_kw at %s", device, at.Format(time.RFC3339Nano))
			}
		}
		refused := restoreRefused(o, device)
		for us := range refused {
			index := -1
			for i, taken := range b.Taken {
				if taken.UnixMicro() == us {
					index = i
					break
				}
			}
			if index < 0 || len(b.PowerKW) != len(b.Taken) {
				return fmt.Errorf("%s refused entry has no taken value at %d", device, us)
			}
			if counts[us] != 1 {
				return fmt.Errorf("%s refused entry at %d stored %d times, want exactly once", device, us, counts[us])
			}
			matches := 0
			for _, r := range b.PowerStored {
				if r.At.UnixMicro() == us && r.Value == b.PowerKW[index] {
					matches++
				}
			}
			if matches != 1 {
				return fmt.Errorf("%s refused power_kw at %d differs from taken value %v", device, us, b.PowerKW[index])
			}
		}
	}
	return nil
}

func missingRollout(s S5) error {
	code := ""
	if s.Ledger != nil {
		code = s.Ledger.OTA.RefusalCode
	}
	return fmt.Errorf("no platform rollout (refusal code %q)", code)
}

func otaArtifactTrusted(o Observations) error {
	s := o.S5
	if s.Ledger == nil {
		return fmt.Errorf("no S5 declaration")
	}
	l, a, signer := s.Ledger.OTA, s.Artifact, s.Signer
	token, digest, size := sim.HarborpulseFirmwareIdentity()
	if l.Artifact != token || l.Digest != digest || l.Version != sim.HarborpulseFirmwareVersion || l.SizeBytes != size {
		return fmt.Errorf("artifact declaration does not identify the synthetic firmware")
	}
	if a == nil || a.Token != token || a.Digest != digest || a.SizeBytes != strconv.Itoa(size) || a.Version != sim.HarborpulseFirmwareVersion || a.Component != otafleet.DefaultComponent || a.State != "AVAILABLE" || a.TrustStatus != "SIGNED" || len(a.CompatibleProfiles) != 1 || a.CompatibleProfiles[0] != sim.HarborpulseChargerProfile {
		return fmt.Errorf("artifact is absent, mismatched, unavailable or UNVERIFIED (rollout refusal %q)", l.RefusalCode)
	}
	if signer == nil || l.Signer == "" || l.KeyID == "" || signer.Token != l.Signer || signer.KeyID != l.KeyID || signer.State != "ACTIVE" {
		return fmt.Errorf("expected artifact signer is not ACTIVE with the declared keyId")
	}
	pinned := func(p *OTASigner) bool {
		return p != nil && p.Token == l.Signer && p.KeyID == l.KeyID && p.State == "ACTIVE"
	}
	trusted := false
	for _, sig := range a.Signatures {
		if sig.CountsTowardTrust && pinned(&sig.Signer) {
			trusted = true
		}
	}
	if !trusted {
		return fmt.Errorf("artifact has no trusted signature by the expected signer")
	}
	for _, target := range s.Targets {
		if target.AssignmentToken == "" {
			continue
		}
		assignment := s.Assignments[target.AssignmentToken]
		if assignment == nil || assignment.Token != target.AssignmentToken || assignment.DeviceToken != target.DeviceToken || !pinned(assignment.SignedBy) {
			return fmt.Errorf("assignment for %s does not pin the expected signer", target.DeviceToken)
		}
	}
	return nil
}

func otaTargetsExact(o Observations) error {
	s := o.S5
	r := s.Rollout
	if r == nil {
		return missingRollout(s)
	}
	if s.Ledger == nil || r.Token != s.Ledger.OTA.Rollout || r.Artifact.Token != s.Ledger.OTA.Artifact || r.GroupToken != sim.HarborpulseAllChargersGroup || r.TargetCount != 6 || len(s.Targets) != 6 {
		return fmt.Errorf("rollout group, artifact or frozen target count differs from the six chargers")
	}
	want := setOf(sim.HarborpulseChargers())
	seen := map[string]bool{}
	for _, target := range s.Targets {
		if !want[target.DeviceToken] || seen[target.DeviceToken] {
			return fmt.Errorf("unexpected or duplicate frozen target %s", target.DeviceToken)
		}
		seen[target.DeviceToken] = true
	}
	for token := range want {
		if !seen[token] {
			return fmt.Errorf("charger %s is missing from the frozen targets", token)
		}
	}
	return nil
}

type otaEdge struct {
	at    time.Time
	delta int
}

// otaPeak uses platform receive instants. core/ota.Apply rejects reports after terminal
// states, and Reconcile only moves UNKNOWN, so lastReportAt cannot drift after UPDATED/FAILED.
// At equal instants, release a completed slot before admitting a new dispatch.
func otaPeak(s S5) (int, error) {
	var edges []otaEdge
	for _, target := range s.Targets {
		if target.DispatchedAt == "" {
			continue
		}
		assignment := s.Assignments[target.AssignmentToken]
		if assignment == nil {
			return 0, fmt.Errorf("no attempt for dispatched target %s", target.DeviceToken)
		}
		from, err := time.Parse(time.RFC3339Nano, target.DispatchedAt)
		if err != nil {
			return 0, err
		}
		switch assignment.Attempt.State {
		case "UPDATED", "FAILED", "TIMED_OUT", "CANCELLED":
		default:
			return 0, fmt.Errorf("attempt for %s has no terminal instant", target.DeviceToken)
		}
		to, err := time.Parse(time.RFC3339Nano, assignment.Attempt.LastReportAt)
		if err != nil {
			return 0, err
		}
		if to.Before(from) {
			return 0, fmt.Errorf("attempt for %s ended before dispatch", target.DeviceToken)
		}
		if to.Equal(from) {
			continue
		} // empty half-open interval occupies no slot
		edges = append(edges, otaEdge{from, 1}, otaEdge{to, -1})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].at.Equal(edges[j].at) {
			return edges[i].delta < edges[j].delta
		}
		return edges[i].at.Before(edges[j].at)
	})
	active, peak := 0, 0
	for _, edge := range edges {
		active += edge.delta
		if active > peak {
			peak = active
		}
	}
	return peak, nil
}

func otaConcurrency(o Observations) error {
	s := o.S5
	if s.Rollout == nil {
		return missingRollout(s)
	}
	if s.Rollout.MaxInFlight != 2 {
		return fmt.Errorf("platform maxInFlight=%d, want 2", s.Rollout.MaxInFlight)
	}
	peak, err := otaPeak(s)
	if err != nil {
		return err
	}
	if peak != 2 {
		return fmt.Errorf("platform dispatch timeline peaked at %d, want exactly 2", peak)
	}
	if err := S5Coverage(s); err != nil {
		return err
	} // Run promotes unavailable sample coverage to no verdict
	for _, sample := range s.Samples {
		if sample.InFlight < 0 || sample.InFlight > 2 {
			return fmt.Errorf("live progress.inFlight=%d at %s", sample.InFlight, sample.At.Format(time.RFC3339Nano))
		}
	}
	return nil
}

func otaOutcomes(o Observations) error {
	s := o.S5
	r := s.Rollout
	if r == nil {
		return missingRollout(s)
	}
	if r.State != "COMPLETED" {
		return fmt.Errorf("platform rollout is %s, want COMPLETED", r.State)
	}
	counts := OTAProgress{Total: len(s.Targets)}
	for _, target := range s.Targets {
		a := s.Assignments[target.AssignmentToken]
		if a == nil || a.Token != target.AssignmentToken || a.DeviceToken != target.DeviceToken || target.SkipCode != "" {
			return fmt.Errorf("target %s has no matching assignment or was skipped", target.DeviceToken)
		}
		if target.DeviceToken == sim.HarborpulseFailedCharger {
			if target.Outcome != "FAILED" || target.AttemptState != "FAILED" || a.Attempt.State != "FAILED" || a.Attempt.Failure == nil || a.Attempt.Failure.Code != "VERIFICATION_FAILED" {
				return fmt.Errorf("north-1 did not fail VERIFICATION_FAILED")
			}
			counts.Failed++
		} else {
			if target.Outcome != "SUCCEEDED" || target.AttemptState != "UPDATED" || a.Attempt.State != "UPDATED" || a.Attempt.TargetVersion != sim.HarborpulseFirmwareVersion {
				return fmt.Errorf("target %s did not confirm UPDATED 2.4.1", target.DeviceToken)
			}
			counts.Succeeded++
		}
	}
	// Membership belongs solely to ota-targets-exact: compare counts to this frozen list.
	if r.Progress != counts {
		return fmt.Errorf("rollout progress %+v differs from frozen target outcomes %+v", r.Progress, counts)
	}
	return nil
}
