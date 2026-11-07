// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

type dupReplay struct{ msgs []messaging.Message }
type dupReader struct {
	msgs []messaging.Message
	i    int
}

func (r *dupReader) Read(context.Context) (messaging.Message, error) {
	if r.i >= len(r.msgs) {
		return messaging.Message{}, io.EOF
	}
	r.i++
	return r.msgs[r.i-1], nil
}
func (r *dupReader) Close() error { return nil }
func (d dupReplay) NewReplayReaderFromTime(string, time.Time) (messaging.ReplayReader, time.Time, error) {
	return &dupReader{msgs: d.msgs}, time.Time{}, nil
}

// The resolver hands the preview the instance's envelope mode: with live's duplicate guard off a
// retransmitted event is scanned twice, with it on once. (Differential coverage of the replay itself
// is in processor.TestPreviewAgreesWithLiveOnDuplicatesInBothEnvelopeModes; this pins the wiring.)
func TestPreviewRuleTellsThePreviewWhetherLiveRunsItsDuplicateGuard(t *testing.T) {
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	var msgs []messaging.Message
	for seq := uint64(1); seq <= 2; seq++ {
		b, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{
			Source: "http1", SourceDeviceToken: "d1", ProfileVersionToken: "p@1",
			OccurredTime: at, ProcessedTime: at, EventType: eventtype.Measurement,
			Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
				OccurredTime: at,
				Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: "90"}},
			}}},
		})
		require.NoError(t, err)
		m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, nil)
		m.StreamSeq = seq
		m.AppendTime = at
		msgs = append(msgs, m)
	}
	for _, tc := range []struct {
		guard bool
		want  int32
	}{{false, 2}, {true, 1}} {
		r := &SchemaResolver{Profiles: publishedProfiles(t, true), Replay: dupReplay{msgs: msgs}, AppliedGuard: tc.guard}
		res, err := r.PreviewRule(previewCtx(auth.DeviceRead, auth.LocationRead),
			previewInput(plainDraft, "2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z"))
		require.NoError(t, err)
		require.True(t, res.Ok())
		require.Equal(t, tc.want, res.Stats().EventsScanned(), "guard=%v", tc.guard)
	}
}
