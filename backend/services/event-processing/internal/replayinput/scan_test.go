// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package replayinput

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

type fakeReader struct {
	msgs []messaging.Message
	err  error
}

func (r *fakeReader) Read(context.Context) (messaging.Message, error) {
	if len(r.msgs) == 0 {
		if r.err != nil {
			return messaging.Message{}, r.err
		}
		return messaging.Message{}, io.EOF
	}
	m := r.msgs[0]
	r.msgs = r.msgs[1:]
	return m, nil
}
func (r *fakeReader) Close() error { return nil }

var at = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func message(t *testing.T, seq uint64, dev string) messaging.Message {
	t.Helper()
	b, e := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{Source: "test", SourceDeviceToken: dev, ProfileVersionToken: "p@1", OccurredTime: at, EventType: eventtype.Measurement, Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{OccurredTime: at, Entries: []resolvedevent.ResolvedMeasurementEntry{{Name: "temp", Value: "1"}}}}}})
	if e != nil {
		t.Fatal(e)
	}
	return messaging.Message{Subject: "dc.acme.resolved-events", StreamSeq: seq, Value: b}
}
func TestAdmissionCapsAndSequences(t *testing.T) {
	for _, tc := range []struct {
		name        string
		adm         Admission
		want        int
		stop        Stop
		first, last uint64
	}{
		{"all", Admission{}, 3, "", 2, 4},
		{"devices", Admission{Devices: map[string]bool{"a": true}}, 2, "", 2, 4},
		{"upper", Admission{UpperSeq: 2}, 1, StopUpperBound, 2, 3},
		{"read", Admission{MaxRead: 1}, 1, StopReadCap, 2, 3},
		{"scan", Admission{MaxScan: 1}, 1, StopScanCap, 2, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.adm
			a.Tenant = "acme"
			a.Span = TimeRange{at.Add(-time.Minute), at.Add(time.Minute)}
			n := 0
			c, stop, e := Scan(context.Background(), &fakeReader{msgs: []messaging.Message{message(t, 2, "a"), message(t, 3, "b"), message(t, 4, "a")}}, a, func(messaging.Message, *resolvedevent.ResolvedEvent, bool) { n++ })
			if e != nil || stop != tc.stop || n != tc.want || c.Scanned != n || c.FirstSeq != tc.first || c.LastSeq != tc.last {
				t.Fatalf("%+v %s n=%d err=%v", c, stop, n, e)
			}
		})
	}
}
func TestCorruptionIsTenantScoped(t *testing.T) {
	bad := messaging.Message{Subject: "dc.acme.resolved-events", Value: []byte("bad"), StreamSeq: 1}
	env := bad
	env.StreamSeq = 2
	env.Headers = map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}
	other := bad
	other.StreamSeq = 3
	other.Subject = "dc.other.resolved-events"
	c, _, e := Scan(context.Background(), &fakeReader{msgs: []messaging.Message{bad, env, other}}, Admission{Tenant: "acme"}, func(messaging.Message, *resolvedevent.ResolvedEvent, bool) { t.Fatal("corrupt event visited") })
	if e != nil || c.Corrupt != 2 || c.Read != 3 || c.FirstSeq != 1 || c.LastSeq != 3 {
		t.Fatalf("%+v %v", c, e)
	}
}
func TestCancellationAndDeadline(t *testing.T) {
	for _, e := range []error{context.Canceled, context.DeadlineExceeded} {
		c, stop, err := Scan(context.Background(), &fakeReader{err: e}, Admission{}, nil)
		if c.Read != 0 {
			t.Fatal(c)
		}
		if errors.Is(e, context.Canceled) && !errors.Is(err, e) {
			t.Fatal(err)
		}
		if errors.Is(e, context.DeadlineExceeded) && (err != nil || stop != StopDeadline) {
			t.Fatalf("%v %v", stop, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Scan(ctx, &fakeReader{}, Admission{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
