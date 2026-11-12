// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/internal/evaluate"
	"github.com/devicechain-io/dc-event-processing/internal/nightshift"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/testdata/streamprobe"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ruletest"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

const south = "tp-vav-tower-f1-south"
const north = "tp-vav-tower-f3-north"
const west = "tp-vav-tower-f5-west"

func nightParams(t *testing.T) (Params, *nightshift.Capture) {
	t.Helper()
	c := nightshift.Load(t)
	bystanders := 0
	for _, s := range c.Samples {
		if s.Device == c.Header.Bystander {
			bystanders++
		}
	}
	require.Positive(t, bystanders, "device-filter assertion needs real bystander traffic")
	b := &RuleEnvelope{Token: c.Header.R1.Token, Enabled: true, Definition: c.Header.R1.Definition}
	p := Params{Tenant: nightshift.Tenant, ProfileToken: c.Header.ProfileToken, Base: b, Draft: b, Devices: c.Header.Devices, Source: c, Suffix: streams.ResolvedEvents, Lateness: config.DefaultWatermarkLatenessSeconds * time.Second, Now: c.Samples[len(c.Samples)-1].At.Add(time.Hour)}
	for _, role := range []Role{Incident, Normal} {
		w := c.Header.Windows[string(role)]
		p.Windows = append(p.Windows, Window{role, w[0], w[1]})
	}
	p.VersionRules = func(_ context.Context, version string) (*RuleEnvelope, bool, error) {
		return b, version == c.Header.ProfileVersion, nil
	}
	for _, e := range []struct {
		role Role
		text string
	}{{Incident, `raised: {device: tp-vav-tower-f1-south, within: 30m}`}, {Normal, `neverIntent: {kind: sendCommand, command: boost-cooling, device: tp-vav-tower-f3-north}`}} {
		var expect ruletest.Expect
		require.NoError(t, ruletest.DecodeStrict([]byte(e.text), &expect))
		p.Expectations = append(p.Expectations, Expectation{e.role, expect})
	}
	return p, c
}
func nightDraft(t *testing.T, b *RuleEnvelope, threshold float64, window time.Duration) *RuleEnvelope {
	t.Helper()
	d, err := rules.Decode(b.Definition)
	require.NoError(t, err)
	d.Threshold = &threshold
	d.Window = rules.Duration(window)
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	out := *b
	out.Definition = raw
	return &out
}
func nightWindow(t *testing.T, r Result, role Role) WindowResult {
	t.Helper()
	for _, w := range r.Windows {
		if w.Role == role {
			return w
		}
	}
	t.Fatal("missing window", role)
	return WindowResult{}
}
func nightTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return at
}
func checkBystander(t *testing.T, r Result, c *nightshift.Capture) {
	t.Helper()
	for _, w := range r.Windows {
		for _, d := range w.Devices {
			require.NotEqual(t, c.Header.Bystander, d.Device)
		}
		for _, d := range w.Changes {
			require.NotEqual(t, c.Header.Bystander, d.Device)
		}
	}
}
func TestNightShiftDraftA(t *testing.T) {
	p, c := nightParams(t)
	p.Draft = nightDraft(t, p.Base, 25.5, 5*time.Minute)
	r := run(t, p)
	require.Equal(t, "FAILED", r.Outcome)
	require.Equal(t, Pass, r.Expectations[0].Verdict)
	require.Equal(t, Fail, r.Expectations[0].Baseline)
	require.Equal(t, Fail, r.Expectations[1].Verdict)
	checkBystander(t, r, c)
	for _, pair := range []struct {
		role       Role
		device, at string
	}{{Incident, south, "2026-11-02T00:51:35Z"}, {Normal, north, "2026-11-02T02:05:35Z"}} {
		w := nightWindow(t, r, pair.role)
		at := nightTime(t, pair.at)
		var divergence *time.Time
		for _, d := range w.Devices {
			if d.Device == pair.device {
				divergence = d.Divergence
			}
		}
		require.NotNil(t, divergence)
		require.True(t, divergence.Equal(at))
		for _, subject := range []string{"EDGE_RAISED", evaluate.KindRaiseAlarm, evaluate.KindSendCommand} {
			found := false
			for _, change := range w.Changes {
				if change.Device == pair.device && change.Subject == subject && change.Kind == Added && change.Draft != nil && change.Draft.Equal(at) {
					found = true
					require.False(t, change.AfterDivergence)
				}
			}
			require.True(t, found, "missing ADDED %s on %s", subject, pair.device)
		}
		later := 0
		for _, change := range w.Changes {
			if change.Device == pair.device && changeTime(change).After(at) {
				later++
				require.True(t, change.AfterDivergence)
			}
		}
		// The incident ends before south's eventual clear; NORMAL includes north's clear.
		if pair.role == Normal {
			require.Positive(t, later)
		}
		if pair.role == Normal {
			var first *evaluate.Intent
			for i := range w.DraftIntents {
				in := &w.DraftIntents[i]
				if in.Series == north && in.Kind == evaluate.KindSendCommand {
					first = in
					break
				}
			}
			require.NotNil(t, first)
			require.True(t, first.At.Equal(*divergence), "first forbidden match must be AT divergence")
			reason := r.Expectations[1].Reason
			for _, part := range []string{"boost-cooling", `{"minutes":15}`, north, pair.at} {
				require.Contains(t, reason, part)
			}
			require.NotEmpty(t, r.Expectations[1].Evidence)
			require.Contains(t, reason, fmt.Sprintf("reading value %g", r.Expectations[1].Evidence[0].Value))
		}
	}
}

func TestNightShiftDraftB(t *testing.T) {
	p, c := nightParams(t)
	p.Draft = nightDraft(t, p.Base, 25.5, 15*time.Minute)
	r := run(t, p)
	require.Equal(t, "PASSED", r.Outcome)
	for _, e := range r.Expectations {
		require.Equal(t, Pass, e.Verdict)
	}
	checkBystander(t, r, c)
	w := nightWindow(t, r, Normal)
	var base, draft []string
	for _, e := range w.BaseEdges {
		if e.Series == west && e.Raise {
			base = append(base, e.At.Format(time.RFC3339))
		}
	}
	for _, e := range w.DraftEdges {
		if e.Series == west {
			draft = append(draft, fmt.Sprintf("%t %s", e.Raise, e.At.Format(time.RFC3339)))
		}
	}
	require.Equal(t, []string{"2026-11-02T01:54:55Z", "2026-11-02T02:11:35Z", "2026-11-02T02:28:15Z"}, base)
	// The longer window raises later and stays raised through the second and third baseline
	// cycles. This is the honest control trade-off computed over recorded inputs, not a claim
	// that these would be the physical device's readings under the changed commands.
	require.Equal(t, []string{"true 2026-11-02T02:02:20Z", "false 2026-11-02T02:33:00Z"}, draft)
	var edgeChanges []string
	controlCounts := map[string]Counts{}
	for _, ch := range w.Changes {
		if ch.Device != west {
			continue
		}
		count := controlCounts[ch.Subject]
		switch ch.Kind {
		case Shifted:
			count.Shifted++
		case Removed:
			count.Removed++
		case Added:
			count.Added++
		case ContentChanged:
			count.ContentChanged++
		}
		controlCounts[ch.Subject] = count
		if strings.HasPrefix(ch.Subject, "EDGE_") {
			edgeChanges = append(edgeChanges, string(ch.Kind)+" "+ch.Subject)
		}
	}
	require.Equal(t, []string{"SHIFTED EDGE_RAISED", "REMOVED EDGE_RESOLVED", "REMOVED EDGE_RAISED", "REMOVED EDGE_RESOLVED", "REMOVED EDGE_RAISED", "SHIFTED EDGE_RESOLVED"}, edgeChanges)
	// Each edge has its matching alarm intent, and each raise also requests the command:
	// five shifted elements and ten removed elements on the control device in total.
	require.Equal(t, map[string]Counts{
		"EDGE_RAISED":            {Shifted: 1, Removed: 2},
		"EDGE_RESOLVED":          {Shifted: 1, Removed: 2},
		evaluate.KindRaiseAlarm:  {Shifted: 1, Removed: 2},
		evaluate.KindClearAlarm:  {Shifted: 1, Removed: 2},
		evaluate.KindSendCommand: {Shifted: 1, Removed: 2},
	}, controlCounts)
}

func TestNightShiftNoop(t *testing.T) {
	for _, description := range []bool{false, true} {
		t.Run(fmt.Sprint(description), func(t *testing.T) {
			p, c := nightParams(t)
			def, err := rules.Decode(p.Base.Definition)
			require.NoError(t, err)
			if description {
				def.Description = "A wording-only edit"
			}
			raw, err := json.Marshal(def)
			require.NoError(t, err)
			d := *p.Base
			d.Definition = raw
			p.Draft = &d
			r := run(t, p)
			checkBystander(t, r, c)
			control := 0
			for _, w := range r.Windows {
				require.Empty(t, w.Changes)
				for _, in := range w.BaseIntents {
					if in.Series == west {
						control++
					}
				}
			}
			require.Positive(t, control, "no-op must compare real side-effect intents")
		})
	}
}
func TestNightShiftAgedOutLeadIn(t *testing.T) {
	p, c := nightParams(t)
	p.Draft = nightDraft(t, p.Base, 25.5, 5*time.Minute)
	p.Windows[0].Start = c.Header.Epoch.Add(5 * time.Minute)
	r := run(t, p)
	require.Equal(t, "INCONCLUSIVE", r.Outcome)
	require.Equal(t, Inconclusive, r.Expectations[0].Verdict)
	require.Contains(t, r.Expectations[0].Reason, "aged out")
}

func TestNightShiftCompiledMarginsAndOracle(t *testing.T) {
	p, c := nightParams(t)
	var events []evaluate.Event
	for _, s := range c.Samples {
		if s.Device == c.Header.Bystander {
			continue
		}
		e := evaluate.Event{Device: s.Device, At: s.At}
		for k, v := range s.Metrics {
			e.Metrics = append(e.Metrics, evaluate.Metric{Name: k, Value: v})
		}
		events = append(events, e)
	}
	for _, probe := range []struct {
		device    string
		threshold float64
		window    time.Duration
		raises    []string
	}{
		{south, 25.85, 5 * time.Minute, nil},
		{south, 25.75, 5 * time.Minute, []string{"2026-11-02T00:54:10Z"}},
		{south, 26, 5 * time.Minute, nil},
		{north, 25.70, 5 * time.Minute, []string{"2026-11-02T02:06:05Z"}},
		{north, 25.80, 5 * time.Minute, nil},
		{north, 25.30, 15 * time.Minute, nil},
		{south, 25.65, 15 * time.Minute, []string{"2026-11-02T01:01:00Z"}},
		{west, 26.3, 5 * time.Minute, []string{"2026-11-02T01:55:20Z", "2026-11-02T02:14:25Z"}},
		{west, 26, 5 * time.Minute, []string{"2026-11-02T01:54:55Z", "2026-11-02T02:11:35Z", "2026-11-02T02:28:15Z"}},
	} {
		t.Run(fmt.Sprintf("%s/%g/%s", probe.device, probe.threshold, probe.window), func(t *testing.T) {
			def, err := rules.Decode(nightDraft(t, p.Base, probe.threshold, probe.window).Definition)
			require.NoError(t, err)
			out, err := evaluate.Run(context.Background(), evaluate.Params{Tenant: p.Tenant, ProfileVersion: c.Header.ProfileVersion, Rules: []evaluate.Rule{{Token: p.Base.Token, Def: def}}, Events: events, AdvanceTo: c.Samples[len(c.Samples)-1].At, Lateness: p.Lateness})
			require.NoError(t, err)
			require.Empty(t, out.Degradations)
			var got []string
			for _, e := range out.Edges {
				if e.Series == probe.device && e.Raise {
					got = append(got, e.At.Format(time.RFC3339))
				}
			}
			require.Equal(t, probe.raises, got)
			if probe.device == south && probe.window == 15*time.Minute {
				require.True(t, nightTime(t, got[0]).Before(p.Windows[0].Start.Add(25*time.Minute)))
			}
			if probe.device == west && probe.threshold == 26 {
				var oracle []string
				for _, o := range c.Oracles {
					oracle = append(oracle, o.RaisedAt.Format(time.RFC3339))
				}
				require.Equal(t, oracle, got)
			}
		})
	}
}

func TestNightShiftBrokerHasZeroSideEffects(t *testing.T) {
	p, c := nightParams(t)
	p.Draft = nightDraft(t, p.Base, 25.5, 5*time.Minute)
	probe := streamprobe.New(t)
	httpProbe := streamprobe.NewHTTP(t)
	require.Zero(t, httpProbe.CommandEnqueues())
	ctx := core.WithTenant(context.Background(), p.Tenant)
	require.NoError(t, probe.Writers[streams.ResolvedEvents].WriteMessages(ctx, c.Messages...))
	before := probe.Counts(t)
	require.Equal(t, uint64(len(c.Messages)), before[streams.ResolvedEvents])
	p.Source = probe.Manager
	r, err := Run(ctx, p)
	require.NoError(t, err)
	intents := 0
	for _, w := range r.Windows {
		intents += len(w.DraftIntents)
	}
	require.Positive(t, intents)
	require.Equal(t, before, probe.Counts(t), "review published to a live stream")
	require.Zero(t, httpProbe.CommandEnqueues(), "review enqueued a command over HTTP")
}
