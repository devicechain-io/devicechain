// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// fakeApi records what the consumer asks of the update plane.
type fakeApi struct {
	applied    []ota.Report
	verdict    ota.Verdict
	applyErr   error
	views      []model.UpdateAssignmentView
	grant      *model.Grant
	grantErr   error
	grantsFor  []string
	cancels    []string
	answers    int
	published  []string
	currentErr error
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
	if f.currentErr != nil {
		return nil, f.currentErr
	}
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
	c, _ := newConsumerWithRegistry(t, api)
	return c
}

func newConsumerWithRegistry(t *testing.T, api UpdateReportsApi) (*ReportsConsumer, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{InstanceId: "inst", FunctionalArea: "update-management"}
	ms.UseMetricsRegistry(reg)
	return NewReportsConsumer(ms, nil, core.NewNoOpLifecycleCallbacks(), api, nil, NewUpdateReportsMetrics(ms)), reg
}

// handled reads how many messages the loop finished with the given result.
func handled(t *testing.T, reg *prometheus.Registry, result string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	var total float64
	for _, mf := range mfs {
		if !strings.HasSuffix(mf.GetName(), "update_reports_messages_total") {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "result" && l.GetValue() == result {
					total += m.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}

func msg(subject, body string) messaging.Message {
	return messaging.Message{Subject: subject, Value: []byte(body)}
}

const reportsSubject = "inst.acme.device-reports.dev-1"

func counter(c *ReportsConsumer, vec string, label string) float64 {
	switch vec {
	case "reports":
		verdict, kind, found := strings.Cut(label, "/")
		if !found {
			kind = reportKindProgress
		}
		return testutil.ToFloat64(c.reports.WithLabelValues(verdict, kind))
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

// ackRecorder observes whether the consumer acknowledged a message.
type ackRecorder struct{ acks int }

func (a *ackRecorder) Ack() error { a.acks++; return nil }

// A deleted tenant's report is refused by the erasure fence on every delivery, so it is
// acknowledged on the FIRST one: retrying it would spend the whole redelivery budget and then
// file a dead letter about a tenant whose data is being erased.
func TestAReportForADeletedTenantIsDroppedOnItsFirstDelivery(t *testing.T) {
	api := &fakeApi{applyErr: fmt.Errorf("applying: %w", rdb.ErrTenantPurged)}
	c, reg := newConsumerWithRegistry(t, api)
	ack := &ackRecorder{}
	c.handle(context.Background(), messaging.NewConsumedMessage(reportsSubject, []byte(progressBody("RECEIVED")), 1, nil, ack))
	require.Equal(t, 1, ack.acks, "a report the fence refuses was left for redelivery")
	require.EqualValues(t, 1, handled(t, reg, core.ResultErased))
	require.Zero(t, handled(t, reg, core.ResultRetry))
	require.Zero(t, handled(t, reg, core.ResultFailed))

	// The download-request path reaches the same store.
	api.applyErr = nil
	api.grantErr = rdb.ErrTenantPurged
	ack = &ackRecorder{}
	c.handle(context.Background(), messaging.NewConsumedMessage(reportsSubject,
		[]byte(`{"v":1,"kind":"download.request","assignmentId":"asg-1"}`), 1, nil, ack))
	require.Equal(t, 1, ack.acks, "a download request the fence refuses was left for redelivery")
	require.EqualValues(t, 2, handled(t, reg, core.ResultErased))
}

// letterRecorder is a dead-letter writer that keeps what it was handed.
type letterRecorder struct{ msgs []messaging.Message }

func (l *letterRecorder) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	l.msgs = append(l.msgs, msgs...)
	return nil
}

// And at the CAP — the delivery the letter would be written on — a deleted tenant's report is
// still dropped and never lettered, through a consumer that DOES letter: the same report failing
// for any other reason at the cap writes one, which is what keeps the first half from passing on
// a sink that could not letter at all.
func TestAReportForADeletedTenantAtTheCapIsNotLettered(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		letters int
		result  string
	}{
		{"deleted tenant", fmt.Errorf("applying: %w", rdb.ErrTenantPurged), 0, core.ResultErased},
		{"store failure", errors.New("database down"), 1, core.ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			ms := &core.Microservice{InstanceId: "inst", FunctionalArea: "update-management"}
			ms.UseMetricsRegistry(reg)
			dead := &letterRecorder{}
			c := NewReportsConsumer(ms, nil, core.NewNoOpLifecycleCallbacks(), &fakeApi{applyErr: tc.err},
				deadletter.NewProducer(ms).NewSink(dead), NewUpdateReportsMetrics(ms))
			ack := &ackRecorder{}
			m := messaging.NewConsumedMessage(reportsSubject, []byte(progressBody("RECEIVED")), messaging.MaxDeliver, nil, ack).
				WithOrigin(messaging.Origin{Suffix: streams.DeviceReports, Stream: "dc_device-reports",
					Consumer: "dc_update-management_device-reports", Seq: 7})
			c.handle(context.Background(), m)
			require.Equal(t, 1, ack.acks)
			require.Len(t, dead.msgs, tc.letters)
			require.EqualValues(t, 1, handled(t, reg, tc.result))
		})
	}
}

// A report that could not be applied (the store is down) must be left for redelivery, not
// acknowledged: acknowledging it would lose the device's progress for good. The handler reports a
// retry and never an ok for it, and an ordinary report is the opposite.
func TestAFailedApplyIsRetriedNotAcknowledged(t *testing.T) {
	api := &fakeApi{applyErr: errors.New("database down")}
	c, reg := newConsumerWithRegistry(t, api)
	c.handle(context.Background(), msg(reportsSubject, progressBody("RECEIVED")))
	require.EqualValues(t, 1, handled(t, reg, core.ResultRetry))
	require.Zero(t, handled(t, reg, core.ResultOK), "a report that failed to apply was finished as ok")
	require.Zero(t, handled(t, reg, core.ResultFailed))

	api.applyErr = nil
	c.handle(context.Background(), msg(reportsSubject, progressBody("RECEIVED")))
	require.EqualValues(t, 1, handled(t, reg, core.ResultOK))
}

// An inventory report is counted under kind=inventory, a progress report under kind=progress, so
// the refused-reports alert can judge a fleet on progress alone: inventory reports answer
// UNKNOWN_ASSIGNMENT or UNCONFIRMED on every healthy reconnect.
func TestReportsAreCountedByKind(t *testing.T) {
	api := &fakeApi{verdict: model.VerdictUnknownAssignment}
	c := newConsumer(t, api)
	inventory := `{"v":1,"kind":"update.inventory","component":"firmware","bootId":"b","running":{"version":"1.0.0"}}`
	c.handle(context.Background(), msg(reportsSubject, inventory))
	require.EqualValues(t, 1, counter(c, "reports", "UNKNOWN_ASSIGNMENT/inventory"))
	require.EqualValues(t, 0, counter(c, "reports", "UNKNOWN_ASSIGNMENT/progress"))

	c.handle(context.Background(), msg(reportsSubject, progressBody("RECEIVED")))
	require.EqualValues(t, 1, counter(c, "reports", "UNKNOWN_ASSIGNMENT/progress"))
	require.EqualValues(t, 1, counter(c, "reports", "UNKNOWN_ASSIGNMENT/inventory"))
}
