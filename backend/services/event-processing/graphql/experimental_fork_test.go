// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var forkBase = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func forkReading(t *testing.T, seq uint64, device string, v float64, at time.Time) messaging.Message {
	t.Helper()
	b, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{
		Source: "http1", SourceDeviceToken: device, ProfileVersionToken: "p@1",
		OccurredTime: at, ProcessedTime: at, EventType: eventtype.Measurement,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: at,
			Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: strconv.FormatFloat(v, 'g', -1, 64)}},
		}}},
	})
	require.NoError(t, err)
	m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, nil)
	m.StreamSeq = seq
	m.AppendTime = at
	return m
}

// liveRules stores one live rule (temperature above 80) on the profile's active version.
func liveRules(t *testing.T) *model.DetectRuleStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DetectRule{}))
	s := model.NewDetectRuleStore(&rdb.RdbManager{Database: db})
	require.NoError(t, s.Upsert(context.Background(), []model.DetectRule{{
		RuleId: "acme/p@1/hot", Tenant: "acme", ProfileVersionToken: "p@1", RuleToken: "hot",
		Definition: `{"id":"hot","name":"hot","type":"threshold","when":{"metric":"temperature","op":"gt","threshold":80}}`,
	}}))
	return s
}

func forkInput(perts ...experimentalPerturbationInput) struct{ Input experimentalForkTimelineInput } {
	return struct{ Input experimentalForkTimelineInput }{experimentalForkTimelineInput{
		ProfileToken: "p", Start: "2026-11-03T11:00:00Z", End: "2026-11-03T14:00:00Z", Perturbations: perts,
	}}
}

func fp(f float64) *float64 { return &f }
func sp(s string) *string   { return &s }

// A heatwave: two rooftop sensors read 78 and 76; reality never alarms, the fork (+5) alarms both.
func TestExperimentalForkTimelineHeatwaveAddsAlarms(t *testing.T) {
	var msgs []messaging.Message
	for i, v := range []float64{78, 76} {
		msgs = append(msgs, forkReading(t, uint64(i+1), "roof-"+strconv.Itoa(i+1), v, forkBase.Add(time.Duration(i)*time.Minute)))
	}
	r := &SchemaResolver{Profiles: publishedProfiles(t, true), DetectRules: liveRules(t),
		Replay: dupReplay{msgs: msgs}, ForkTimelineEnabled: true}
	res, err := r.ExperimentalForkTimeline(previewCtx(auth.DeviceRead),
		forkInput(experimentalPerturbationInput{Op: "metric-offset", Metric: sp("temperature"), Offset: fp(5)}))
	require.NoError(t, err)
	require.True(t, res.Ok(), "%v", res.Error())
	require.Equal(t, int32(1), res.RuleCount())
	require.Empty(t, res.Baseline(), "reality stays calm")
	require.Len(t, res.Fork(), 2, "78+5 and 76+5 both cross 80")
	require.Len(t, res.Divergence(), 2)
	for _, d := range res.Divergence() {
		require.Equal(t, "added", d.Kind())
		require.Equal(t, "raised", d.Signal())
		require.Nil(t, d.BaselineAt())
	}
	require.Nil(t, res.Degraded())
	require.Equal(t, []string{"metric-offset metric=temperature value*1+5"}, res.Perturbations())
}

func TestExperimentalForkTimelineIsOffByDefault(t *testing.T) {
	r := &SchemaResolver{Profiles: publishedProfiles(t, true), DetectRules: liveRules(t), Replay: dupReplay{}}
	_, err := r.ExperimentalForkTimeline(previewCtx(auth.DeviceRead),
		forkInput(experimentalPerturbationInput{Op: "drop-device", Device: sp("d")}))
	require.ErrorIs(t, err, errForkDisabled)
}

func TestExperimentalForkTimelineNeedsDeviceRead(t *testing.T) {
	r := &SchemaResolver{Profiles: publishedProfiles(t, true), DetectRules: liveRules(t),
		Replay: dupReplay{}, ForkTimelineEnabled: true}
	_, err := r.ExperimentalForkTimeline(previewCtx(),
		forkInput(experimentalPerturbationInput{Op: "drop-device", Device: sp("d")}))
	require.Error(t, err)
}

func TestExperimentalForkTimelineRefusesBadRequestsAsOkFalse(t *testing.T) {
	r := &SchemaResolver{Profiles: publishedProfiles(t, true), DetectRules: liveRules(t),
		Replay: dupReplay{}, ForkTimelineEnabled: true}
	for name, in := range map[string]struct{ Input experimentalForkTimelineInput }{
		"no perturbation":   forkInput(),
		"unknown operation": forkInput(experimentalPerturbationInput{Op: "explode"}),
		"open blackout":     forkInput(experimentalPerturbationInput{Op: "blackout", From: sp("2026-11-03T12:00:00Z")}),
	} {
		res, err := r.ExperimentalForkTimeline(previewCtx(auth.DeviceRead), in)
		require.NoError(t, err, name)
		require.False(t, res.Ok(), name)
		require.NotNil(t, res.Error(), name)
	}
}

func TestExperimentalForkTimelineUnpublishedProfileIsRefused(t *testing.T) {
	r := &SchemaResolver{Profiles: publishedProfiles(t, false), DetectRules: liveRules(t),
		Replay: dupReplay{}, ForkTimelineEnabled: true}
	res, err := r.ExperimentalForkTimeline(previewCtx(auth.DeviceRead),
		forkInput(experimentalPerturbationInput{Op: "drop-device", Device: sp("d")}))
	require.NoError(t, err)
	require.False(t, res.Ok())
}
