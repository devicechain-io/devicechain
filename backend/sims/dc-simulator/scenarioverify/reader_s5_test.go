// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-simulator/sim"
)

func s5ReaderServer(t *testing.T, respond func(string, map[string]json.RawMessage) (any, int)) *Reader {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string
			Variables map[string]json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var data any
		switch {
		case strings.Contains(req.Query, "login("):
			data = map[string]any{"login": map[string]any{"identityToken": "identity", "superuser": true}}
		case strings.Contains(req.Query, "selectTenant("), strings.Contains(req.Query, "refresh("):
			name := "selectTenant"
			if strings.Contains(req.Query, "refresh(") {
				name = "refresh"
			}
			data = map[string]any{name: map[string]any{"accessToken": "access", "refreshToken": "refresh", "expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339)}}
		default:
			var status int
			data, status = respond(req.Query, req.Variables)
			if status != 0 {
				http.Error(w, "read failed", status)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return &Reader{session: userclient.NewTenantSession(srv.Client(), srv.URL, "sim", "pw", "tenant"), endpoints: sim.Endpoints{UpdateMgmtGraphQL: srv.URL}}
}

func TestS5ReaderReadsAllTargetPagesAndTheirAssignments(t *testing.T) {
	pages, assignments := 0, 0
	r := s5ReaderServer(t, func(doc string, vars map[string]json.RawMessage) (any, int) {
		switch {
		case strings.Contains(doc, "artifact("):
			return map[string]any{"artifact": cleanDay().s5.Artifact}, 0
		case strings.Contains(doc, "artifactSigner("):
			return map[string]any{"artifactSigner": cleanDay().s5.Signer}, 0
		case strings.Contains(doc, "updateRollout("):
			return map[string]any{"updateRollout": cleanDay().s5.Rollout}, 0
		case strings.Contains(doc, "updateRolloutTargets("):
			var criteria struct{ PageNumber, PageSize int }
			_ = json.Unmarshal(vars["c"], &criteria)
			pages++
			if criteria.PageSize != 100 {
				t.Errorf("unexpected page size %d", criteria.PageSize)
			}
			var targets []OTATarget
			start, end := (criteria.PageNumber-1)*100, criteria.PageNumber*100
			if end > 101 {
				end = 101
			}
			for i := start; i < end; i++ {
				targets = append(targets, OTATarget{DeviceToken: fmt.Sprintf("device-%d", i), AssignmentToken: fmt.Sprintf("a-%d", i)})
			}
			return map[string]any{"updateRolloutTargets": map[string]any{"results": targets, "pagination": map[string]any{"totalRecords": 101}}}, 0
		case strings.Contains(doc, "updateAssignment("):
			assignments++
			var token string
			_ = json.Unmarshal(vars["token"], &token)
			return map[string]any{"updateAssignment": &OTAAssignment{Token: token}}, 0
		default:
			t.Errorf("unexpected %s", doc)
			return nil, 500
		}
	})
	var o Observations
	if err := readS5(context.Background(), r, &o, cleanDay().s5.Ledger); err != nil {
		t.Fatal(err)
	}
	if pages != 2 || assignments != 101 || len(o.S5.Targets) != 101 || len(o.S5.Assignments) != 101 {
		t.Fatalf("pages=%d assignments=%d targets=%d", pages, assignments, len(o.S5.Targets))
	}
}

func TestS5ReadFailuresCannotBecomeVerdicts(t *testing.T) {
	for _, failed := range []string{"artifact(", "artifactSigner(", "updateRollout(", "updateRolloutTargets(", "updateAssignment("} {
		t.Run(failed, func(t *testing.T) {
			fixture := cleanDay().s5
			r := s5ReaderServer(t, func(doc string, vars map[string]json.RawMessage) (any, int) {
				if strings.Contains(doc, failed) {
					return nil, 503
				}
				switch {
				case strings.Contains(doc, "artifact("):
					return map[string]any{"artifact": fixture.Artifact}, 0
				case strings.Contains(doc, "artifactSigner("):
					return map[string]any{"artifactSigner": fixture.Signer}, 0
				case strings.Contains(doc, "updateRollout("):
					return map[string]any{"updateRollout": fixture.Rollout}, 0
				case strings.Contains(doc, "updateRolloutTargets("):
					return map[string]any{"updateRolloutTargets": map[string]any{"results": fixture.Targets, "pagination": map[string]any{"totalRecords": 6}}}, 0
				case strings.Contains(doc, "updateAssignment("):
					var token string
					_ = json.Unmarshal(vars["token"], &token)
					return map[string]any{"updateAssignment": fixture.Assignments[token]}, 0
				}
				return nil, 500
			})
			if err := readS5(context.Background(), r, &Observations{}, fixture.Ledger); err == nil {
				t.Fatal("failed read was treated as evidence")
			}
		})
	}
}

func TestS5TargetPagesRejectNullAndNoProgress(t *testing.T) {
	for _, response := range []any{nil, map[string]any{"results": []any{}, "pagination": map[string]any{}}, map[string]any{"results": []any{}, "pagination": map[string]any{"totalRecords": 6}}} {
		r := s5ReaderServer(t, func(string, map[string]json.RawMessage) (any, int) {
			return map[string]any{"updateRolloutTargets": response}, 0
		})
		if _, err := r.RolloutTargets(context.Background(), "r"); err == nil {
			t.Fatal("incomplete target search succeeded")
		}
	}
}

func TestS5WatchHistoricalRolloutDoesNotInventSamples(t *testing.T) {
	s := cleanDay().s5
	r := s5ReaderServer(t, func(string, map[string]json.RawMessage) (any, int) {
		return map[string]any{"updateRollout": s.Rollout}, 0
	})
	started := time.Now().Add(-time.Hour)
	clock := Clock{StartedAt: started, DayLength: 24 * time.Minute, StartHour: 6}
	status := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Status{Scenario: &sim.ScenarioClock{Started: true, StartedAt: started.Format(time.RFC3339Nano), DayLengthSeconds: 1440, StartHour: 6, S5: s.Ledger}})
	}))
	defer status.Close()
	o := Observations{Clock: clock, Day: 1}
	if _, err := watchS5(context.Background(), Config{SimURL: status.URL, StatusHTTP: status.Client()}, r, &o); err != nil {
		t.Fatal(err)
	}
	if o.S5.LiveSampled || len(o.S5.Samples) != 0 {
		t.Fatal("historical rollout received invented live samples")
	}
	o.Day = 0
	if _, err := watchS5(context.Background(), Config{SimURL: status.URL, StatusHTTP: status.Client()}, r, &o); err == nil {
		t.Fatal("missed day 0 live sampling claimed a bound")
	}
	o.Day = 1
	s.Ledger.OTA.ScheduleHour = 25
	if _, err := watchS5(context.Background(), Config{SimURL: status.URL, StatusHTTP: status.Client()}, r, &o); err == nil {
		t.Fatal("invalid rollout schedule was accepted")
	}

}

func TestS5WatchSamplesUntilTerminalAndRejectsUncoveredStuckRollout(t *testing.T) {
	for _, stuck := range []bool{false, true} {
		t.Run(fmt.Sprint(stuck), func(t *testing.T) {
			fixture := cleanDay().s5
			fixture.Rollout.State = "RUNNING"
			fixture.Rollout.StartedAt = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
			if stuck {
				fixture.Rollout.StartedAt = time.Now().Add(-7 * time.Minute).Format(time.RFC3339Nano)
			}
			queries := 0
			r := s5ReaderServer(t, func(string, map[string]json.RawMessage) (any, int) {
				queries++
				rollout := *fixture.Rollout
				rollout.Progress.InFlight = 2
				if !stuck && queries > 1 {
					rollout.State = "COMPLETED"
					rollout.Progress.InFlight = 0
				}
				return map[string]any{"updateRollout": &rollout}, 0
			})
			started := time.Now().Add(-21 * time.Minute)
			status := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(Status{Scenario: &sim.ScenarioClock{Started: true, StartedAt: started.Format(time.RFC3339Nano), DayLengthSeconds: 1440, StartHour: 6, S5: fixture.Ledger}})
			}))
			defer status.Close()
			o := Observations{Clock: Clock{StartedAt: started, DayLength: 24 * time.Minute, StartHour: 6}, Day: 0}
			_, err := watchS5(context.Background(), Config{SimURL: status.URL, StatusHTTP: status.Client()}, r, &o)
			if err != nil {
				t.Fatal(err)
			}
			// An uncovered interval loses the samples' own claim, not the day: the sweep still judges.
			if stuck && o.S5.SampleCoverage == "" {
				t.Fatal("uncovered seven-minute interval claimed a sample verdict")
			}
			if !stuck && o.S5.SampleCoverage != "" {
				t.Fatalf("covered rollout reported a coverage gap: %s", o.S5.SampleCoverage)
			}
			if !o.S5.LiveSampled || len(o.S5.Samples) == 0 {
				t.Fatal("running rollout was not sampled")
			}
			if !stuck && len(o.S5.Samples) != 2 {
				t.Fatalf("samples %+v", o.S5.Samples)
			}
			if stuck && otaOutcomes(Observations{S5: fixture}) == nil {
				t.Fatal("stuck but answering rollout did not fail")
			}
		})
	}
}

// TestS5GappySamplesStillJudgeOnTheSweep is the live finding from the rig: with every control
// sim polling at once, the one-second samples had five-second gaps. That must not void the
// day — the platform's dispatch/terminal sweep is complete evidence — while a sampled value
// above the cap still fails, gaps or not.
func TestS5GappySamplesStillJudgeOnTheSweep(t *testing.T) {
	d := cleanDay()
	s := d.s5
	start := time.Now().Add(-time.Minute)
	s.LiveSampled = true
	s.Rollout.StartedAt = start.Format(time.RFC3339Nano)
	s.SampleTo = start.Add(30 * time.Second)
	s.Samples = []OTASample{{At: start, InFlight: 2}, {At: start.Add(20 * time.Second), InFlight: 1}, {At: start.Add(30 * time.Second), InFlight: 0}}
	if S5Coverage(s) == nil {
		t.Fatal("a 20 s gap claimed coverage")
	}
	if err := otaConcurrency(Observations{S5: s}); err != nil {
		t.Fatalf("gappy samples voided a sweep that holds: %v", err)
	}
	s.Samples[1].InFlight = 3
	if otaConcurrency(Observations{S5: s}) == nil {
		t.Fatal("a sampled inFlight of 3 passed because the samples were gappy")
	}
}
