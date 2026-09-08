// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	emtest "github.com/devicechain-io/dc-event-management/test"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The two ways an event-management failure is reported to the failed-events subject
// have to agree about what happens when the failure itself will not encode.
//
// 🔴 THEY DID NOT. OnFailedEvent marshals inside an if/else and publishes nothing on a
// marshal error; ProcessFailedEvent logged the error and FELL THROUGH, publishing a
// message whose Value was nil — an undecodable record with the reason in its key, which
// is strictly worse than no record, since the only signal a consumer receives is one it
// cannot parse.
//
// The branch is unreachable through the real encoder (PFailedEvent is all scalars, so
// proto.Marshal has nothing to reject), which is exactly why the bug survived: nothing
// could enter it. marshalFailedEvent exists as a seam so this test can.

// recordingWriter captures what is published rather than merely that something was.
//
// The shared MockMessageWriter cannot serve here: its WriteMessages calls Called() with
// no arguments, so a mock expectation can see that a publish happened and never what it
// carried. The defect under test is a publish whose BODY is nil, which is invisible to
// an assertion that only counts calls.
type recordingWriter struct {
	messages []messaging.Message
}

func (w *recordingWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.messages = append(w.messages, msgs...)
	return nil
}

func (w *recordingWriter) WriteToDevice(_ context.Context, _ string, msgs ...messaging.Message) error {
	w.messages = append(w.messages, msgs...)
	return nil
}

func (w *recordingWriter) HandleResponse(error) {}

// newFailedEventProcessor builds a processor over a recording writer, isolated from the
// suite next door so the two cannot share a metrics registry or a channel.
func newFailedEventProcessor(t *testing.T) (*EventPersistenceProcessor, *recordingWriter) {
	t.Helper()
	registry := prometheus.NewRegistry()
	prometheus.DefaultRegisterer = registry
	prometheus.DefaultGatherer = registry

	failed := &recordingWriter{}
	eproc := NewEventPersistenceProcessor(
		dmtest.DeviceManagementMicroservice,
		new(msgtest.MockMessageReader),
		failed,
		core.NewNoOpLifecycleCallbacks(),
		new(emtest.MockApi))
	require.NoError(t, eproc.Initialize(context.Background()))
	return eproc, failed
}

// failedItemForTest is one queued failure, ready to be drained by ProcessFailedEvent.
func failedItemForTest() failedItem {
	return failedItem{
		tenant: "tenant1",
		event: *dmodel.NewFailedEvent(uint(dmproto.FailureReason_Invalid), "event-management",
			"message could not be parsed", errors.New("boom"), []byte("payload")),
		correlationId: "corr-1",
	}
}

// A failure that will not encode publishes NOTHING. Publishing an empty record here is
// the defect, so the assertion is on the writer never being called at all.
func TestUnmarshalableFailedEventPublishesNothing(t *testing.T) {
	eproc, failed := newFailedEventProcessor(t)

	original := marshalFailedEvent
	marshalFailedEvent = func(*dmodel.FailedEvent) ([]byte, error) {
		return nil, errors.New("encoder refused the event")
	}
	t.Cleanup(func() { marshalFailedEvent = original })

	// The writer accepts anything it is given, so nothing but the code under test can
	// be the reason no message is recorded.
	eproc.failed <- failedItemForTest()
	eof := eproc.ProcessFailedEvent(context.Background())

	require.False(t, eof, "a handled failure is not the end of the stream")
	require.Empty(t, failed.messages,
		"an unencodable failure must publish nothing at all, not an empty record")
}

// The control, and it is the half that makes the test above mean something: with a
// working encoder the same path DOES publish, and publishes a non-empty payload. A fix
// that simply stopped publishing would pass the assertion above and fail here.
func TestEncodableFailedEventIsPublishedWithItsPayload(t *testing.T) {
	eproc, failed := newFailedEventProcessor(t)

	eproc.failed <- failedItemForTest()
	eof := eproc.ProcessFailedEvent(context.Background())

	require.False(t, eof)
	require.Len(t, failed.messages, 1, "an encodable failure must still be published")
	sent := failed.messages[0]
	require.NotEmpty(t, sent.Value, "a published failure must carry a decodable body")

	decoded, err := dmproto.UnmarshalFailedEvent(sent.Value)
	require.NoError(t, err, "what is published must decode as a failed event")
	require.Equal(t, "message could not be parsed", decoded.Message)
}
