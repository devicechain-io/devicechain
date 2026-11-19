// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// S5 separates declarations (the sim ledger) from evidence read from the platform.
// Later S5 readers add restore and replacement evidence here.
type S5 struct {
	Replacement          ReplacementEvidence
	Ledger               *sim.ScenarioS5
	Artifact             *OTAArtifact
	Signer               *OTASigner
	Rollout              *OTARollout
	Targets              []OTATarget
	Assignments          map[string]*OTAAssignment
	Samples              []OTASample
	LiveSampled          bool // false when the rollout completed before this watch
	SampleFrom, SampleTo time.Time
}

type OTASigner struct{ Token, KeyID, State string }
type OTASignature struct {
	Signer            OTASigner
	CountsTowardTrust bool
}
type OTAArtifact struct {
	Token, Digest, Version, Component, SizeBytes, State, TrustStatus string
	CompatibleProfiles                                               []string
	Signatures                                                       []OTASignature
}
type OTAProgress struct{ Total, Pending, InFlight, Succeeded, Failed, Unknown, Cancelled, Skipped int }
type OTARollout struct {
	Token, State, GroupToken, StartedAt        string
	Artifact                                   struct{ Token string }
	MaxInFlight, FailureThreshold, TargetCount int
	Progress                                   OTAProgress
}
type OTATarget struct{ DeviceToken, Outcome, AssignmentToken, AttemptState, SkipCode, DispatchedAt string }
type OTAAssignment struct {
	Token, DeviceToken string
	SignedBy           *OTASigner
	Attempt            struct {
		State, TargetVersion, LastReportAt string
		Failure                            *struct {
			Stage, Code, Detail string
			Platform            bool
		}
	}
}
type OTASample struct {
	At       time.Time
	InFlight int
}

const queryS5Artifact = `query S5Artifact($token:String!){artifact(token:$token){token digest version component sizeBytes state trustStatus compatibleProfiles signatures{signer{token keyId state} countsTowardTrust}}}`
const queryS5Signer = `query S5Signer($token:String!){artifactSigner(token:$token){token keyId state}}`
const queryS5Rollout = `query S5Rollout($token:String!){updateRollout(token:$token){token state groupToken maxInFlight failureThreshold targetCount startedAt artifact{token} progress{total pending inFlight succeeded failed unknown cancelled skipped}}}`
const queryS5Targets = `query S5Targets($token:String!,$c:UpdateRolloutTargetSearchCriteria!){updateRolloutTargets(token:$token,criteria:$c){results{deviceToken outcome assignmentToken attemptState skipCode dispatchedAt} pagination{totalRecords}}}`
const queryS5Assignment = `query S5Assignment($token:String!){updateAssignment(token:$token){token deviceToken signedBy{token keyId state} attempt{state targetVersion lastReportAt failure{stage code detail platform}}}}`

func (r *Reader) Rollout(ctx context.Context, token string) (*OTARollout, error) {
	var got struct{ UpdateRollout *OTARollout }
	if err := r.session.Query(ctx, r.endpoints.UpdateMgmtGraphQL, queryS5Rollout, map[string]any{"token": token}, &got); err != nil {
		return nil, fmt.Errorf("read S5 rollout: %w", err)
	}
	if got.UpdateRollout != nil && got.UpdateRollout.StartedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, got.UpdateRollout.StartedAt); err != nil {
			return nil, fmt.Errorf("rollout startedAt: %w", err)
		}
	}
	return got.UpdateRollout, nil
}

func readS5(ctx context.Context, r *Reader, o *Observations, ledger *sim.ScenarioS5) error {
	o.S5.Ledger = ledger
	if ledger == nil {
		return fmt.Errorf("the sim's /status carries no S5 ledger")
	}
	if err := validateRestoreLedger(ledger); err != nil {
		return err
	}
	l := ledger.OTA
	var artifact struct{ Artifact *OTAArtifact }
	if err := r.session.Query(ctx, r.endpoints.UpdateMgmtGraphQL, queryS5Artifact, map[string]any{"token": l.Artifact}, &artifact); err != nil {
		return err
	}
	o.S5.Artifact = artifact.Artifact
	var signer struct{ ArtifactSigner *OTASigner }
	if err := r.session.Query(ctx, r.endpoints.UpdateMgmtGraphQL, queryS5Signer, map[string]any{"token": l.Signer}, &signer); err != nil {
		return err
	}
	o.S5.Signer = signer.ArtifactSigner
	rollout, err := r.Rollout(ctx, l.Rollout)
	if err != nil {
		return err
	}
	o.S5.Rollout = rollout
	if rollout == nil {
		return nil
	} // a readable absence is a failed invariant, not an outage
	o.S5.Assignments = map[string]*OTAAssignment{}
	o.S5.Targets, err = r.RolloutTargets(ctx, l.Rollout)
	if err != nil {
		return err
	}
	for _, target := range o.S5.Targets {
		if target.DispatchedAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, target.DispatchedAt); err != nil {
				return fmt.Errorf("target dispatchedAt: %w", err)
			}
		}
		if target.AssignmentToken == "" {
			continue
		}
		var got struct{ UpdateAssignment *OTAAssignment }
		if err := r.session.Query(ctx, r.endpoints.UpdateMgmtGraphQL, queryS5Assignment, map[string]any{"token": target.AssignmentToken}, &got); err != nil {
			return err
		}
		if got.UpdateAssignment != nil && got.UpdateAssignment.Attempt.LastReportAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, got.UpdateAssignment.Attempt.LastReportAt); err != nil {
				return fmt.Errorf("attempt lastReportAt: %w", err)
			}
		}
		o.S5.Assignments[target.AssignmentToken] = got.UpdateAssignment
	}
	return nil
}

// Bad declaration timestamps cannot silently turn a refused entry into an unrefused
// one. They are unavailable evidence, like an unreadable platform response.
func validateRestoreLedger(ledger *sim.ScenarioS5) error {
	for _, l := range ledger.RestoreDrains {
		for _, stamp := range []string{l.FirstRefusalAt, l.CaughtUpAt} {
			if stamp != "" {
				if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
					return fmt.Errorf("restore ledger timestamp: %w", err)
				}
			}
		}
		for _, d := range l.Refused {
			for _, stamp := range d.Times {
				at, err := time.Parse(time.RFC3339Nano, stamp)
				if err != nil || at.Nanosecond()%1000 != 0 {
					return fmt.Errorf("restore refused time %q is not a microsecond instant", stamp)
				}
			}
		}
	}
	return nil
}

func (r *Reader) RolloutTargets(ctx context.Context, token string) ([]OTATarget, error) {
	var out []OTATarget
	for page := 1; page <= maxPages; page++ {
		var got struct {
			UpdateRolloutTargets *struct {
				Results    []OTATarget
				Pagination struct{ TotalRecords *int }
			}
		}
		vars := map[string]any{"token": token, "c": map[string]any{"pageNumber": page, "pageSize": 100}}
		if err := r.session.Query(ctx, r.endpoints.UpdateMgmtGraphQL, queryS5Targets, vars, &got); err != nil {
			return nil, err
		}
		p := got.UpdateRolloutTargets
		if p == nil || p.Pagination.TotalRecords == nil {
			return nil, fmt.Errorf("rollout targets %s returned null page or totalRecords", token)
		}
		out = append(out, p.Results...)
		if len(out) == *p.Pagination.TotalRecords {
			return out, nil
		}
		if len(out) > *p.Pagination.TotalRecords || len(p.Results) == 0 {
			return nil, fmt.Errorf("rollout target pagination is inconsistent")
		}
	}
	return nil, fmt.Errorf("rollout targets exceed %d pages", maxPages)
}

const otaSampleGap = 4 * time.Second

// S5Coverage judges whether the live sample check can make a claim. Historical rollouts
// need no invented samples: their complete terminal timeline is independent evidence.
func S5Coverage(s S5) error {
	if !s.LiveSampled {
		return nil
	}
	if len(s.Samples) == 0 {
		return fmt.Errorf("OTA live sampling was requested but no samples answered")
	}
	if s.Rollout == nil {
		return fmt.Errorf("OTA sample coverage has no platform rollout")
	}
	started, err := time.Parse(time.RFC3339Nano, s.Rollout.StartedAt)
	if err != nil || s.SampleTo.IsZero() || s.SampleTo.Before(started) {
		return fmt.Errorf("OTA sample coverage has no valid platform start or observation end")
	}
	s.SampleFrom = started
	if s.Samples[0].At.After(s.SampleFrom.Add(otaSampleGap)) || s.Samples[len(s.Samples)-1].At.Before(s.SampleTo.Add(-otaSampleGap)) {
		return fmt.Errorf("OTA live samples do not cover the running interval")
	}
	for i := 1; i < len(s.Samples); i++ {
		gap := s.Samples[i].At.Sub(s.Samples[i-1].At)
		if gap < 0 || gap > otaSampleGap {
			return fmt.Errorf("OTA live sample gap %s: no verdict on the sampled bound", gap)
		}
	}
	return nil
}

// watchS5 starts at the once-per-run schedule, then polls at one second until the rollout
// finishes or its six-minute deadline. Every read failure produces no verdict.
func watchS5(ctx context.Context, cfg Config, r *Reader, o *Observations) (*sim.ScenarioClock, error) {
	initial, err := ReadStatus(ctx, cfg.StatusHTTP, cfg.SimURL)
	if err != nil {
		return nil, err
	}
	if initial.Scenario == nil || initial.Scenario.S5 == nil {
		return nil, fmt.Errorf("S5 rollout schedule is absent")
	}
	hour := initial.Scenario.S5.OTA.ScheduleHour
	if hour != sim.HarborpulseRolloutHour {
		return nil, fmt.Errorf("S5 rollout schedule %v is not day 0 26:00", hour)
	}
	scheduled := o.Clock.At(0, hour)
	if err := sleepUntil(ctx, scheduled); err != nil {
		return nil, err
	}
	deadline := scheduled.Add(6 * time.Minute)
	var last *sim.ScenarioClock
	for {
		status, err := ReadStatus(ctx, cfg.StatusHTTP, cfg.SimURL)
		if err != nil {
			return nil, err
		}
		if status.Scenario == nil || status.Scenario.S5 == nil {
			return nil, fmt.Errorf("S5 ledger is absent during rollout watch")
		}
		c, err := ClockFrom(status.Scenario)
		if err != nil {
			return nil, err
		}
		if !c.StartedAt.Equal(o.Clock.StartedAt) {
			return nil, fmt.Errorf("scenario restarted during S5 watch")
		}
		last = status.Scenario
		l := last.S5.OTA
		now := time.Now()
		var rollout *OTARollout
		if l.Rollout != "" {
			rollout, err = r.Rollout(ctx, l.Rollout)
			if err != nil {
				return nil, err
			}
		}
		if rollout != nil {
			o.S5.Rollout = rollout
			// The observation ends when this platform read answers, independently
			// of the last sample's request instant. Slow reads leave visible gaps.
			o.S5.SampleTo = time.Now()
			terminal := rollout.State == "COMPLETED" || rollout.State == "ABORTED"
			if terminal && o.Day == 0 && !o.S5.LiveSampled {
				return nil, fmt.Errorf("day 0 rollout finished before live sampling began")
			}
			if o.S5.LiveSampled || !terminal {
				if !o.S5.LiveSampled {
					o.S5.LiveSampled = true
					o.S5.SampleFrom, _ = time.Parse(time.RFC3339Nano, rollout.StartedAt)
				}
				o.S5.Samples = append(o.S5.Samples, OTASample{At: now, InFlight: rollout.Progress.InFlight})
			}
			if terminal {
				break
			}
			if rollout.StartedAt != "" {
				started, _ := time.Parse(time.RFC3339Nano, rollout.StartedAt)
				deadline = started.Add(6 * time.Minute)
			}
		} else if l.RefusalCode != "" {
			break
		}
		if now.After(deadline) {
			break
		} // an answering but stuck rollout is a FAIL, not no verdict
		if err := sleepUntil(ctx, time.Now().Add(time.Second)); err != nil {
			return nil, err
		}
	}
	if err := S5Coverage(o.S5); err != nil {
		return nil, err
	}
	return last, nil
}
