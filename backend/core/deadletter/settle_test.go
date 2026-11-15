// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
)

type settleAck struct {
	calls  int
	before func()
}

func (a *settleAck) Ack() error {
	if a.before != nil {
		a.before()
	}
	a.calls++
	return nil
}

type settleWriter struct {
	msgtest.MockMessageWriter
	got []messaging.Message
}

func (w *settleWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	w.got = append(w.got, msgs...)
	return w.MockMessageWriter.WriteMessages(ctx, msgs...)
}

func TestSettle(t *testing.T) {
	boom := errors.New("store unavailable")
	for _, tc := range []struct {
		name                        string
		attempts                    int
		err                         error
		nilSink, failWrite, deleted bool
		max                         int
		want                        messaging.Disposition
		ack, writes                 int
	}{
		{name: "retry", attempts: 1, err: boom, want: messaging.Retry},
		{name: "exhausted whole", attempts: messaging.MaxDeliver, err: boom, want: messaging.Exhausted, ack: 1, writes: 1},
		{name: "bounded", attempts: messaging.MaxDeliver, err: boom, max: 3, want: messaging.Exhausted, ack: 1, writes: 1},
		{name: "nil sink", attempts: messaging.MaxDeliver, err: boom, nilSink: true, want: messaging.Exhausted, ack: 1},
		{name: "failed write", attempts: messaging.MaxDeliver, err: boom, failWrite: true, want: messaging.Exhausted, ack: 1, writes: 3},
		{name: "hold final", attempts: messaging.MaxDeliver, err: fmt.Errorf("wrapped: %w", messaging.ErrHoldReleased), want: messaging.Retry},
		{name: "erased final", attempts: messaging.MaxDeliver, err: fmt.Errorf("wrapped: %w", rdb.ErrTenantPurged), want: messaging.TenantErased, ack: 1},
		{name: "erased early", attempts: 1, err: rdb.ErrTenantPurged, want: messaging.TenantErased, ack: 1},
		{name: "suppressed", attempts: messaging.MaxDeliver, err: boom, deleted: true, want: messaging.Exhausted, ack: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
			ms.UseMetricsRegistry(prometheus.NewRegistry())
			p := deadletter.NewProducer(ms)
			p.UseTenantGate(func(string) bool { return tc.deleted })
			w := &settleWriter{}
			var writeErr error
			if tc.failWrite {
				writeErr = boom
			}
			if tc.writes > 0 {
				w.On("WriteMessages").Return(writeErr).Times(tc.writes)
			}
			var sink *deadletter.Sink
			if !tc.nilSink {
				sink = p.NewSink(w)
			}
			a := &settleAck{before: func() {
				if len(w.got) != tc.writes {
					t.Errorf("ack before write completion: got %d attempts, want %d", len(w.got), tc.writes)
				}
			}}
			payload := []byte("abcdef")
			msg := messaging.NewConsumedMessage("test.acme.raise-alarm", payload, tc.attempts, nil, a).
				WithOrigin(messaging.Origin{Suffix: streams.RaiseAlarm, Stream: "s", Consumer: "c", Seq: 42}).WithCorrelationID("corr")
			before := time.Now().UTC()
			d, lettered := sink.Settle(core.WithTenant(context.Background(), "acme"), msg, tc.err, deadletter.Exhausted{Summary: "summary", Reference: "ref", MaxPayload: tc.max})
			if d != tc.want || a.calls != tc.ack || len(w.got) != tc.writes || lettered != ((tc.writes > 0 && !tc.failWrite) || tc.deleted) {
				t.Fatalf("disposition=%v ack=%d writes=%d lettered=%v", d, a.calls, len(w.got), lettered)
			}
			w.AssertExpectations(t)
			w.AssertNumberOfCalls(t, "WriteMessages", tc.writes)
			if string(msg.Value) != "abcdef" {
				t.Fatal("message payload changed")
			}
			if lettered && !tc.deleted {
				e, err := deadletter.Unmarshal(w.got[0].Value)
				if err != nil {
					t.Fatal(err)
				}
				wantPayload := "abcdef"
				if tc.max > 0 {
					wantPayload = wantPayload[:tc.max]
				}
				if e.Reason != deadletter.ReasonExhausted || e.Summary != "summary" || e.Reference != "ref" || e.Detail != tc.err.Error() || string(e.Payload) != wantPayload || e.OccurredAt.Before(before) || e.OccurredAt.After(time.Now().UTC()) || e.Sequence != 42 || e.Attempts != tc.attempts || e.Correlation != "corr" || w.got[0].DedupID != "mdl.s.c.42" {
					t.Fatalf("letter changed: %+v", e)
				}
			}
		})
	}
}

func TestSettleRequiresError(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil handling error accepted")
		}
	}()
	var sink *deadletter.Sink
	sink.Settle(context.Background(), messaging.Message{}, nil, deadletter.Exhausted{})
}

func TestSettleResult(t *testing.T) {
	for d, want := range map[messaging.Disposition]string{messaging.Retry: core.ResultRetry, messaging.Exhausted: core.ResultFailed, messaging.TenantErased: core.ResultErased} {
		if got := deadletter.Result(d); got != want {
			t.Errorf("Result(%v)=%q, want %q", d, got, want)
		}
	}
}

// A letter that cannot be written logs the consumer's own LOST sentence as the message, so an
// operator's existing search for it still matches; with none supplied, a generic one.
func TestSettleLogsTheCallersLostSentence(t *testing.T) {
	boom := errors.New("store unavailable")
	for _, tc := range []struct{ lost, want string }{
		{lost: "LOST device report: it could be neither applied nor dead-lettered.", want: "LOST device report: it could be neither applied nor dead-lettered."},
		{lost: "", want: "LOST message: it could be neither applied nor dead-lettered."},
	} {
		ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
		ms.UseMetricsRegistry(prometheus.NewRegistry())
		p := deadletter.NewProducer(ms)
		p.UseTenantGate(func(string) bool { return false })
		w := &settleWriter{}
		w.On("WriteMessages").Return(boom).Times(3)
		var buf bytes.Buffer
		logger := zerolog.New(&buf)
		ctx := logger.WithContext(core.WithTenant(context.Background(), "acme"))
		msg := messaging.NewConsumedMessage("test.acme.raise-alarm", []byte("x"), messaging.MaxDeliver, nil, &settleAck{}).
			WithOrigin(messaging.Origin{Suffix: streams.RaiseAlarm, Stream: "s", Consumer: "c", Seq: 1})
		p.NewSink(w).Settle(ctx, msg, boom, deadletter.Exhausted{Summary: "s", Reference: "r", Lost: tc.lost})
		var line struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
			t.Fatalf("log line %q: %v", buf.String(), err)
		}
		if line.Message != tc.want {
			t.Fatalf("LOST message = %q, want %q", line.Message, tc.want)
		}
	}
}
