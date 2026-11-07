// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

var fleetEpoch = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// fleetTraffic is a deterministic recording of a pump fleet: every device reports a temperature
// every five minutes for six hours, on a slow wave with a per-device offset, so a lower threshold
// catches more of the waves than a higher one. It stands in for a recorded stream.
func fleetTraffic(t *testing.T, devices int) []messaging.Message {
	t.Helper()
	var msgs []messaging.Message
	seq := uint64(0)
	for step := 0; step < 72; step++ {
		at := fleetEpoch.Add(time.Duration(step) * 5 * time.Minute)
		for d := 0; d < devices; d++ {
			temp := 72 + 9*math.Sin(2*math.Pi*float64(step)/36+float64(d)) + float64(d%4)
			b, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{
				Source: "http1", SourceDeviceToken: fmt.Sprintf("pump-%02d", d+1), ProfileVersionToken: "p@1",
				OccurredTime: at, ProcessedTime: at, EventType: eventtype.Measurement,
				Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
					OccurredTime: at,
					Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: fmt.Sprintf("%.2f", temp)}},
				}}},
			})
			require.NoError(t, err)
			seq++
			m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, nil)
			m.StreamSeq = seq
			m.AppendTime = at
			msgs = append(msgs, m)
		}
	}
	return msgs
}

func thresholdRule(name string, threshold float64) string {
	return fmt.Sprintf(`{"id":"draft","name":%q,"type":"threshold","when":{"metric":"temperature","op":"gt","threshold":%v}}`, name, threshold)
}

func physprResolver(t *testing.T, enabled bool, devices int) *SchemaResolver {
	t.Helper()
	return &SchemaResolver{Profiles: publishedProfiles(t, true), Replay: dupReplay{msgs: fleetTraffic(t, devices)}, PhysicalPR: enabled}
}

const (
	physprStart = "2026-11-01T00:00:00Z"
	physprEnd   = "2026-11-01T06:00:00Z"
)

func str(s string) *string { return &s }

func diffArgs(base, head string) struct{ Input experimentalRuleDiffInput } {
	return struct{ Input experimentalRuleDiffInput }{Input: experimentalRuleDiffInput{
		ProfileToken: "p", Start: physprStart, End: physprEnd,
		Base: experimentalRuleSide{Label: str("v4"), Author: str("alice@example.com"), RuleDefinition: str(base)},
		Head: experimentalRuleSide{Label: str("draft"), Author: str("agent:tuner"), RuleDefinition: str(head)},
	}}
}

func TestPhysicalPRIsOffByDefault(t *testing.T) {
	r := physprResolver(t, false, 3)
	ctx := previewCtx(auth.DeviceRead)
	_, err := r.ExperimentalPreviewRuleDiff(ctx, diffArgs(thresholdRule("a", 80), thresholdRule("a", 70)))
	require.ErrorContains(t, err, "not enabled")
	_, err = r.ExperimentalBisectRule(ctx, struct{ Input experimentalBisectInput }{})
	require.ErrorContains(t, err, "not enabled")
}

func TestPhysicalPRNeedsTheReadAuthority(t *testing.T) {
	r := physprResolver(t, true, 3)
	_, err := r.ExperimentalPreviewRuleDiff(previewCtx(), diffArgs(thresholdRule("a", 80), thresholdRule("a", 70)))
	require.Error(t, err)
}

// The negative control: an edit that changes no behaviour (a rename) is an EMPTY diff.
func TestPhysicalPRNoOpEditIsAnEmptyDiff(t *testing.T) {
	r := physprResolver(t, true, 8)
	res, err := r.ExperimentalPreviewRuleDiff(previewCtx(auth.DeviceRead), diffArgs(thresholdRule("hot pump", 76), thresholdRule("pump too hot", 76)))
	require.NoError(t, err)
	require.True(t, res.Ok())
	require.True(t, res.Empty(), res.Report())
	require.Zero(t, res.Summary().DevicesAffected())
	require.NotZero(t, res.Summary().AlarmsUnchanged(), "the control must have alarms to leave unchanged, or it proves nothing")
	require.Contains(t, res.Report(), "No behavioural change")
}

// A lower threshold adds alarms, never removes any, and the diff agrees with two separate previews.
func TestPhysicalPRLoweredThresholdAddsAlarms(t *testing.T) {
	r := physprResolver(t, true, 8)
	ctx := previewCtx(auth.DeviceRead)
	res, err := r.ExperimentalPreviewRuleDiff(ctx, diffArgs(thresholdRule("hot", 78), thresholdRule("hot", 72)))
	require.NoError(t, err)
	require.True(t, res.Ok(), res.Report())
	require.False(t, res.Empty())
	s := res.Summary()
	require.Positive(t, s.AlarmsAdded())
	require.Zero(t, s.AlarmsSuppressed())
	require.Positive(t, s.DevicesAffected())

	count := func(rule string) int32 {
		p, perr := r.PreviewRule(ctx, previewInput(rule, physprStart, physprEnd))
		require.NoError(t, perr)
		n := int32(0)
		for _, f := range p.Firings() {
			if f.Signal() == "raised" {
				n++
			}
		}
		return n
	}
	b, h := count(thresholdRule("hot", 78)), count(thresholdRule("hot", 72))
	require.Equal(t, h-b, s.AlarmsAdded()-s.AlarmsSuppressed(), "the diff must equal the difference of the two previews")
	require.Equal(t, b, s.AlarmsUnchanged()+s.AlarmsMoved()+s.AlarmsSuppressed(), "every base alarm is accounted for exactly once")
}

func TestPhysicalPRReversingTheEditReversesTheDiff(t *testing.T) {
	r := physprResolver(t, true, 8)
	ctx := previewCtx(auth.DeviceRead)
	fwd, err := r.ExperimentalPreviewRuleDiff(ctx, diffArgs(thresholdRule("hot", 78), thresholdRule("hot", 72)))
	require.NoError(t, err)
	rev, err := r.ExperimentalPreviewRuleDiff(ctx, diffArgs(thresholdRule("hot", 72), thresholdRule("hot", 78)))
	require.NoError(t, err)
	require.Equal(t, fwd.Summary().AlarmsAdded(), rev.Summary().AlarmsSuppressed())
	require.Equal(t, fwd.Summary().AlarmsSuppressed(), rev.Summary().AlarmsAdded())
}

func TestPhysicalPRRejectsABadSideByName(t *testing.T) {
	r := physprResolver(t, true, 3)
	res, err := r.ExperimentalPreviewRuleDiff(previewCtx(auth.DeviceRead), diffArgs(thresholdRule("a", 80), `{"nope":`))
	require.NoError(t, err)
	require.False(t, res.Ok())
	require.NotEmpty(t, res.Diagnostics())
	require.True(t, strings.HasPrefix(res.Diagnostics()[0].Message(), "head: "), res.Diagnostics()[0].Message())
}

func TestPhysicalPRUnpublishedProfileDegradesRatherThanErrors(t *testing.T) {
	r := physprResolver(t, true, 3)
	r.Profiles = publishedProfiles(t, false)
	res, err := r.ExperimentalPreviewRuleDiff(previewCtx(auth.DeviceRead), diffArgs(thresholdRule("a", 80), thresholdRule("a", 70)))
	require.NoError(t, err)
	require.NotNil(t, res.Degraded())
	require.False(t, res.Empty(), "a comparison that never ran must not read as no behavioural change")
	require.False(t, res.Empty(), "a comparison that never ran must not read as no behavioural change")
}

// ledger builds a rule history in which version `bad` is the one that lowers the threshold; every
// other version is a cosmetic edit of the same rule.
func ledger(n, bad int) []experimentalRuleVersion {
	vs := make([]experimentalRuleVersion, n)
	for i := range vs {
		thr := 80.0
		if i >= bad {
			thr = 76.0
		}
		author, kind := "alice@example.com", "human"
		if i == bad {
			author, kind = "agent:tuner", "agent"
		}
		vs[i] = experimentalRuleVersion{
			Commit: fmt.Sprintf("v%d", i+1), Author: author, AuthorKind: str(kind),
			Message:        str(fmt.Sprintf("edit %d", i+1)),
			PublishedAt:    fleetEpoch.Add(-time.Duration(n-i) * 24 * time.Hour).Format(time.RFC3339),
			RuleDefinition: str(thresholdRule(fmt.Sprintf("hot %d", i), thr)),
		}
	}
	return vs
}

// BISECT names the planted commit wherever it sits in the history, and the principal who made it.
func TestPhysicalPRBisectNamesThePlantedCommit(t *testing.T) {
	r := physprResolver(t, true, 8)
	ctx := previewCtx(auth.DeviceRead)

	// Pick a moment and a device the bad version raises on and the good one does not.
	d, err := r.ExperimentalPreviewRuleDiff(ctx, diffArgs(thresholdRule("hot", 80), thresholdRule("hot", 76)))
	require.NoError(t, err)
	var series, at string
	for _, c := range d.Changes() {
		if c.Kind() == "added" && c.Edge() == "raise" {
			series, at = c.Series(), *c.HeadAt()
			break
		}
	}
	require.NotEmpty(t, series, "the scenario needs an alarm only the bad version raises")

	const n = 7
	for bad := 1; bad < n; bad++ {
		res, err := r.ExperimentalBisectRule(ctx, struct{ Input experimentalBisectInput }{Input: experimentalBisectInput{
			ProfileToken: "p", Versions: ledger(n, bad), Series: series, At: at,
		}})
		require.NoError(t, err)
		require.True(t, res.Ok(), res.Report())
		require.True(t, res.Found(), "bad=%d: %s", bad, res.Report())
		require.Equal(t, fmt.Sprintf("v%d", bad+1), res.Commit().Id(), "bad=%d", bad)
		require.Equal(t, fmt.Sprintf("v%d", bad), res.PreviousCommit().Id())
		require.Equal(t, "agent:tuner", res.Commit().Author())
		require.Equal(t, "appears", *res.Outcome())
		require.LessOrEqual(t, res.Replays(), int32(5), "n=7 needs at most ceil(log2 7)+2 replays")
	}
}

func TestPhysicalPRBisectWithNoChangeSaysSo(t *testing.T) {
	r := physprResolver(t, true, 8)
	res, err := r.ExperimentalBisectRule(previewCtx(auth.DeviceRead), struct{ Input experimentalBisectInput }{Input: experimentalBisectInput{
		ProfileToken: "p", Versions: ledger(4, 99), Series: "pump-01", At: "2026-11-01T03:00:00Z",
	}})
	require.NoError(t, err)
	require.True(t, res.Ok())
	require.False(t, res.Found())
	require.Contains(t, res.Report(), "no change to bisect")
}

func TestPhysicalPRBisectRejectsBadRequests(t *testing.T) {
	r := physprResolver(t, true, 3)
	ctx := previewCtx(auth.DeviceRead)
	for name, in := range map[string]experimentalBisectInput{
		"one version": {ProfileToken: "p", Versions: ledger(1, 0), Series: "pump-01", At: physprEnd},
		"no series":   {ProfileToken: "p", Versions: ledger(3, 1), At: physprEnd},
		"bad time":    {ProfileToken: "p", Versions: ledger(3, 1), Series: "pump-01", At: "yesterday"},
	} {
		res, err := r.ExperimentalBisectRule(ctx, struct{ Input experimentalBisectInput }{Input: in})
		require.NoError(t, err, name)
		require.False(t, res.Ok(), name)
	}
	broken := ledger(3, 1)
	broken[0].RuleDefinition = str(`{"nope":`)
	res, err := r.ExperimentalBisectRule(ctx, struct{ Input experimentalBisectInput }{Input: experimentalBisectInput{
		ProfileToken: "p", Versions: broken, Series: "pump-01", At: physprEnd}})
	require.NoError(t, err)
	require.False(t, res.Ok())
	require.Contains(t, res.Diagnostics()[0].Message(), "version v1")
}

// TestPhysicalPRDemoTranscript runs the whole story and, when PHYSPR_DEMO_OUT names a file, writes
// the transcript there. It asserts nothing a reader would not see: it is the demo, kept honest by
// the same code paths the product uses.
func TestPhysicalPRDemoTranscript(t *testing.T) {
	r := physprResolver(t, true, 12)
	ctx := previewCtx(auth.DeviceRead)
	var out strings.Builder
	say := func(f string, a ...any) { fmt.Fprintf(&out, f, a...) }

	say("$ dcctl rules diff --ledger pump.ledger.json --base v4 --head-file noop.json --fail-on-change   # negative control: a rename\n")
	noop, err := r.ExperimentalPreviewRuleDiff(ctx, diffArgs(thresholdRule("hot pump", 80), thresholdRule("pump too hot", 80)))
	require.NoError(t, err)
	say("%s\n", noop.Report())
	require.True(t, noop.Empty())

	say("$ dcctl rules diff --ledger pump.ledger.json --base v4 --head-file lower.json    # an agent lowers the threshold 80 -> 76\n")
	lower, err := r.ExperimentalPreviewRuleDiff(ctx, diffArgs(thresholdRule("hot pump", 80), thresholdRule("hot pump", 76)))
	require.NoError(t, err)
	say("%s\n", lower.Report())
	require.False(t, lower.Empty())

	var series, at string
	for _, c := range lower.Changes() {
		if c.Kind() == "shifted" && c.Edge() == "raise" && *c.ShiftSeconds() < 0 {
			series, at = c.Series(), *c.HeadAt()
			break
		}
	}
	require.NotEmpty(t, series)
	say("# several versions after that change landed, %s alarms at %s, earlier than it ever used to, and nobody knows why\n", series, at)
	say("$ dcctl rules bisect --ledger pump.ledger.json --device %s --at %s\n", series, at)
	bis, err := r.ExperimentalBisectRule(ctx, struct{ Input experimentalBisectInput }{Input: experimentalBisectInput{
		ProfileToken: "p", Versions: ledger(8, 4), Series: series, At: at}})
	require.NoError(t, err)
	require.True(t, bis.Found())
	require.Equal(t, "v5", bis.Commit().Id())
	say("%s\n", bis.Report())

	if path := os.Getenv("PHYSPR_DEMO_OUT"); path != "" {
		require.NoError(t, os.WriteFile(path, []byte(out.String()), 0o644))
	}
}
