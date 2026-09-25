// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/eventtime"
)

// An event's ProcessedTime is when the platform RECEIVED it, and an event that reports
// no time of its own is dated at that same instant. For the capture stream the receipt
// time is the broker's append time: the moment the broker stored the device's publish,
// before it acknowledged the device. It is NOT the moment this service got round to
// decoding it, which after an outage is later by the whole outage.

// timestampless is a measurement with no occurredTime on the envelope or its entry.
const timestampless = `{"device":"sensor-001","eventType":"Measurement",` +
	`"payload":{"entries":[{"measurements":{"temp":"21.5"}}]}}`

// handleCaptured runs one captured message through the gateway source and returns the
// event it published.
func handleCaptured(t *testing.T, h *captureHarness, body string, numDelivered int, appendTime time.Time) *model.UnresolvedEvent {
	t.Helper()
	before := len(h.publishedEvents())
	ack := &recordingAck{}
	msg := capturedMsg(captureSubject, body, numDelivered, 42, ack)
	msg.AppendTime = appendTime
	h.source.handle(msg)
	require.True(t, ack.settled(t), "message never settled")
	events := h.publishedEvents()
	require.Len(t, events, before+1, "the captured message was not published")
	return events[before]
}

// A reading that waited in the capture stream through an outage keeps the time it
// arrived. Before, it was dated when it was decoded, an hour late here; and because the
// skew bound is measured from ProcessedTime, stamping only ProcessedTime with the
// capture time would have made every such reading look an hour in the future of its
// own receipt, clamped and counted as bounded.
func TestACapturedBacklogKeepsItsCaptureTime(t *testing.T) {
	h := newCaptureHarness(t)
	appended := time.Now().Add(-time.Hour).Truncate(time.Millisecond)

	ev := handleCaptured(t, h, timestampless, 1, appended)

	assert.True(t, ev.ProcessedTime.Equal(appended),
		"ProcessedTime = %v, want the capture append time %v", ev.ProcessedTime, appended)
	assert.True(t, ev.OccurredTime.Equal(appended),
		"a timestampless reading must be dated when received (%v), got %v", appended, ev.OccurredTime)
	_, bounded := eventtime.Effective(ev.OccurredTime, ev.ProcessedTime, 300*time.Second)
	assert.False(t, bounded, "a timestampless backlog reading must never be clamped by the skew bound")
}

// A redelivery of the same captured message decodes to the same times, so its event
// identity (which includes the occurred time) is the same on every attempt.
func TestARedeliveryDecodesToTheSameTimes(t *testing.T) {
	h := newCaptureHarness(t)
	appended := time.Now().Add(-10 * time.Minute).Truncate(time.Millisecond)

	first := handleCaptured(t, h, timestampless, 1, appended)
	time.Sleep(5 * time.Millisecond) // a decode-time stamp would now differ
	second := handleCaptured(t, h, timestampless, 2, appended)

	assert.True(t, first.OccurredTime.Equal(second.OccurredTime),
		"occurred time moved across a redelivery: %v then %v", first.OccurredTime, second.OccurredTime)
	assert.True(t, first.ProcessedTime.Equal(second.ProcessedTime),
		"processed time moved across a redelivery: %v then %v", first.ProcessedTime, second.ProcessedTime)
}

// A device time is kept as the device reported it; only the receipt is stamped.
func TestACapturedDeviceTimeIsKeptAndReceiptIsStamped(t *testing.T) {
	h := newCaptureHarness(t)
	appended := time.Now().Add(-time.Hour).Truncate(time.Millisecond)

	ev := handleCaptured(t, h, validEvent, 1, appended) // occurredTime 2026-07-20T10:30:00Z

	assert.Equal(t, time.Date(2026, 7, 20, 10, 30, 0, 0, time.UTC), ev.OccurredTime.UTC())
	assert.True(t, ev.ProcessedTime.Equal(appended),
		"ProcessedTime = %v, want the capture append time %v", ev.ProcessedTime, appended)
}

// A captured message whose append time is unavailable (zero) is received now: both
// times are set, equal, and current. A zero ProcessedTime would silently switch the
// skew bound off (eventtime.Effective returns early on it).
func TestACapturedMessageWithNoAppendTimeIsReceivedNow(t *testing.T) {
	h := newCaptureHarness(t)
	before := time.Now()
	ev := handleCaptured(t, h, timestampless, 1, time.Time{})
	after := time.Now()

	require.False(t, ev.ProcessedTime.IsZero(), "a zero ProcessedTime disables the skew bound")
	assert.True(t, ev.OccurredTime.Equal(ev.ProcessedTime),
		"one instant for both: occurred %v, processed %v", ev.OccurredTime, ev.ProcessedTime)
	assert.False(t, ev.ProcessedTime.Before(before) || ev.ProcessedTime.After(after),
		"ProcessedTime %v is outside [%v, %v]", ev.ProcessedTime, before, after)
}

// The decoder itself: a receipt time dates a timestampless event, and both fields are
// that one instant, in UTC.
func TestATimestamplessEventIsDatedWhenReceived(t *testing.T) {
	received := time.Now().Add(-time.Hour)
	ev, _, err := NewJsonDecoder(nil, 0).Decode([]byte(timestampless), received)
	require.NoError(t, err)

	assert.Equal(t, received.UTC(), ev.ProcessedTime, "ProcessedTime is the receipt time")
	assert.Equal(t, received.UTC(), ev.OccurredTime, "a timestampless event is dated at its receipt")
}

// A reported time is never overwritten by the receipt time.
func TestADeviceTimeIsKeptAndReceiptIsStamped(t *testing.T) {
	received := time.Now().Add(-time.Hour)
	ev, _, err := NewJsonDecoder(nil, 0).Decode([]byte(validEvent), received)
	require.NoError(t, err)

	assert.Equal(t, time.Date(2026, 7, 20, 10, 30, 0, 0, time.UTC), ev.OccurredTime.UTC())
	assert.Equal(t, received.UTC(), ev.ProcessedTime)
}

// No receipt time means now, never the zero time: a zero ProcessedTime would switch
// the skew bound off.
func TestNoReceiptTimeMeansNow(t *testing.T) {
	before := time.Now()
	ev, _, err := NewJsonDecoder(nil, 0).Decode([]byte(timestampless), time.Time{})
	after := time.Now()
	require.NoError(t, err)

	require.False(t, ev.ProcessedTime.IsZero(), "a zero ProcessedTime disables the skew bound")
	assert.Equal(t, ev.ProcessedTime, ev.OccurredTime, "one instant for both")
	assert.False(t, ev.ProcessedTime.Before(before) || ev.ProcessedTime.After(after),
		"ProcessedTime %v is outside [%v, %v]", ev.ProcessedTime, before, after)
}

// HTTP has no broker in front of it, so an event posted there is received when it is
// decoded: ProcessedTime is now, and a timestampless event's OccurredTime is the very
// same instant.
func TestAnHTTPEventIsReceivedNow(t *testing.T) {
	es, dec, _ := newTestHttpSource(t, nil)
	before := time.Now()
	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/inst-1/acme/events",
		strings.NewReader(timestampless)))
	after := time.Now()
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.NotNil(t, dec.event)

	assert.Equal(t, dec.event.ProcessedTime, dec.event.OccurredTime, "one instant for both")
	assert.False(t, dec.event.ProcessedTime.Before(before) || dec.event.ProcessedTime.After(after),
		"ProcessedTime %v is outside [%v, %v]", dec.event.ProcessedTime, before, after)
}
