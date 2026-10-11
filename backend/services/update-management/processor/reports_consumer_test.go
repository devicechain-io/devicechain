// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// fakeApi records what the consumer asks of the update plane.
type fakeApi struct {
	applied   []ota.Report
	verdict   ota.Verdict
	applyErr  error
	views     []model.UpdateAssignmentView
	grant     *model.Grant
	grantErr  error
	grantsFor []string
	cancels   []string
	answers   int
	published []string
}

func (f *fakeApi) ApplyReport(_ context.Context, device string, rep ota.Report) (ota.Verdict, error) {
	if f.applyErr != nil {
		return "", f.applyErr
	}
	f.applied = append(f.applied, rep)
	if f.verdict == "" {
		return ota.VerdictApplied, nil
	}
	return f.verdict, nil
}

func (f *fakeApi) CurrentUpdates(context.Context, string) ([]model.UpdateAssignmentView, error) {
	return f.views, nil
}

func (f *fakeApi) PublishSyncAnswer(context.Context, string, []model.UpdateAssignmentView) {
	f.answers++
}

func (f *fakeApi) GrantDownload(_ context.Context, device, assignment string) (*model.Grant, error) {
	f.grantsFor = append(f.grantsFor, device+"/"+assignment)
	return f.grant, f.grantErr
}

func (f *fakeApi) PublishCancelFor(_ context.Context, device, attempt, assignment string) {
	f.cancels = append(f.cancels, device+"/"+attempt+"/"+assignment)
}

func (f *fakeApi) PublishGrant(_ context.Context, device, assignment string, g *model.Grant) error {
	f.published = append(f.published, device+"/"+assignment+"/"+g.URL)
	return nil
}

func newConsumer(t *testing.T, api UpdateReportsApi) *ReportsConsumer {
	t.Helper()
	ms := &core.Microservice{InstanceId: "inst", FunctionalArea: "update-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	return NewReportsConsumer(ms, nil, core.NewNoOpLifecycleCallbacks(), api, nil, NewUpdateReportsMetrics(ms))
}

func msg(subject, body string) messaging.Message {
	return messaging.Message{Subject: subject, Value: []byte(body)}
}

const reportsSubject = "inst.acme.device-reports.dev-1"

func counter(c *ReportsConsumer, vec string, label string) float64 {
	switch vec {
	case "reports":
		return testutil.ToFloat64(c.reports.WithLabelValues(label))
	case "grants":
		return testutil.ToFloat64(c.grants.WithLabelValues(label))
	default:
		return testutil.ToFloat64(c.rejected.WithLabelValues(label))
	}
}

func progressBody(stage string) string {
	return `{"v":1,"kind":"update.progress","attemptId":"att-1","assignmentId":"asg-1","component":"firmware",` +
		`"artifactDigest":"sha256:` + strings.Repeat("a", 64) + `","seq":1,"stage":"` + stage + `","bootId":"b"}`
}

func TestProgressReportsAreAppliedForTheDeviceTheSubjectNames(t *testing.T) {
	api := &fakeApi{}
	c := newConsumer(t, api)
	c.handle(context.Background(), msg(reportsSubject, progressBody("RECEIVED")))
	require.Len(t, api.applied, 1)
	require.Equal(t, "att-1", api.applied[0].AttemptID)
	require.EqualValues(t, 1, counter(c, "reports", "APPLIED"))

	// A verdict the state machine gave is counted under its own name.
	api.verdict = ota.VerdictRegression
	c.handle(context.Background(), msg(reportsSubject, progressBody("RECEIVED")))
	require.EqualValues(t, 1, counter(c, "reports", "REGRESSION"))
}

func TestMessagesThatAreNotThisServicesAreAcknowledgedAndLeftAlone(t *testing.T) {
	api := &fakeApi{}
	c := newConsumer(t, api)
	// config.status belongs to device-management.
	c.handle(context.Background(), msg(reportsSubject,
		`{"v":1,"kind":"config.status","revision":1,"digest":"sha256:`+strings.Repeat("a", 64)+`","status":"APPLIED"}`))
	require.Empty(t, api.applied)
	require.Zero(t, len(api.grantsFor)+api.answers)

	// A body that does not decode is counted here and lettered by device-management.
	c.handle(context.Background(), msg(reportsSubject, `{"v":1,"kind":"update.progress"`))
	c.handle(context.Background(), msg(reportsSubject, `{"v":1,"kind":"update.progress","deviceToken":"other"}`))
	require.EqualValues(t, 2, counter(c, "rejected", rejectedMalformed))
	require.Empty(t, api.applied)

	// A subject that names no device is unattributable.
	c.handle(context.Background(), msg("inst.acme.device-reports", progressBody("RECEIVED")))
	c.handle(context.Background(), msg("inst.device-reports.dev-1", progressBody("RECEIVED")))
	require.EqualValues(t, 2, counter(c, "rejected", rejectedUnattributable))
	require.Empty(t, api.applied)
}

func TestADownloadRequestIsAnsweredWithAGrantOrACancel(t *testing.T) {
	api := &fakeApi{grant: &model.Grant{URL: "https://x.example/updates/download/fw?cap=1", ExpiresAt: time.Now().Add(time.Minute)}}
	c := newConsumer(t, api)
	c.handle(context.Background(), msg(reportsSubject, `{"v":1,"kind":"download.request","assignmentId":"asg-1"}`))
	require.Equal(t, []string{"dev-1/asg-1"}, api.grantsFor, "the device is the subject's, the assignment the body's")
	require.Equal(t, []string{"dev-1/asg-1/https://x.example/updates/download/fw?cap=1"}, api.published)
	require.EqualValues(t, 1, counter(c, "grants", "granted"))

	// A refusal about an assignment the device should drop tells it to drop it.
	api.grant, api.grantErr = nil, &model.GrantRefusal{Reason: model.GrantSuperseded, AttemptToken: "att-9", Stale: true}
	c.handle(context.Background(), msg(reportsSubject, `{"v":1,"kind":"download.request","assignmentId":"asg-1"}`))
	require.Equal(t, []string{"dev-1/att-9/asg-1"}, api.cancels)
	require.EqualValues(t, 1, counter(c, "grants", model.GrantSuperseded))
	require.Len(t, api.published, 1, "a refused request is not answered with a grant")

	// A refusal that is not the device's to act on is counted and tells it nothing.
	api.cancels = nil
	api.grantErr = &model.GrantRefusal{Reason: model.GrantNotConfigured}
	c.handle(context.Background(), msg(reportsSubject, `{"v":1,"kind":"download.request","assignmentId":"asg-1"}`))
	require.Empty(t, api.cancels)
	require.EqualValues(t, 1, counter(c, "grants", model.GrantNotConfigured))
}

func TestASyncIsAnsweredOncePerCooldown(t *testing.T) {
	api := &fakeApi{}
	c := newConsumer(t, api)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	c.gate.now = func() time.Time { return now }
	sync := `{"v":1,"kind":"sync","bootId":"b","config":{"haveRevision":0}}`

	c.handle(context.Background(), msg(reportsSubject, sync))
	c.handle(context.Background(), msg(reportsSubject, sync))
	require.Equal(t, 1, api.answers, "the second sync inside the cooldown is not answered")
	require.EqualValues(t, 1, counter(c, "rejected", rejectedThrottled))

	// Another device is independent, and so is the same device after the cooldown.
	c.handle(context.Background(), msg("inst.acme.device-reports.dev-2", sync))
	require.Equal(t, 2, api.answers)
	now = now.Add(SyncCooldown)
	c.handle(context.Background(), msg(reportsSubject, sync))
	require.Equal(t, 3, api.answers)
}

func TestASyncGateIsBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	g := syncGate{last: map[string]time.Time{}, cooldown: time.Second, now: func() time.Time { return now }}
	for i := range maxSyncGate {
		require.True(t, g.allow(string(rune('a'+i%26))+time.Duration(i).String()))
	}
	require.False(t, g.allow("one-too-many"), "a full table refuses new devices rather than growing")
	now = now.Add(2 * time.Second)
	require.True(t, g.allow("one-too-many"), "expired entries are reclaimed")
}

func TestATransientFailureIsRetriedAndThenDeadLettered(t *testing.T) {
	api := &fakeApi{applyErr: errors.New("database down")}
	c := newConsumer(t, api)
	var results []string
	retry := messaging.Message{Subject: reportsSubject, Value: []byte(progressBody("RECEIVED")), NumDelivered: 1}
	c.retryOrDrop(context.Background(), retry, "dev-1", func(r string) { results = append(results, r) }, api.applyErr, "update.progress")
	exhausted := retry
	exhausted.NumDelivered = messaging.MaxDeliver
	c.retryOrDrop(context.Background(), exhausted, "dev-1", func(r string) { results = append(results, r) }, api.applyErr, "update.progress")
	require.Equal(t, []string{core.ResultRetry, core.ResultFailed}, results)
}
