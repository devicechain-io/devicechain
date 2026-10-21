// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletters

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/payloadredact"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// atRestToken stands in for an ACCESS_TOKEN credential's id, which IS the bearer token.
const atRestToken = "fixture-access-token-bearer-value"

// capturedLetters records what a real dead-letter sink puts on the stream.
type capturedLetters struct{ got []messaging.Message }

func (w *capturedLetters) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.got = append(w.got, msgs...)
	return nil
}

// requireNoCredentialAtRest drives a device-authored command response carrying an
// ACCESS_TOKEN credential id through the platform's REAL producer side (core/deadletter, as
// command-delivery builds it), then through this service's consumer into s, and scans the
// stored ROW — every column, as the database hands it back — for the token.
//
// The row is read with raw SQL, not through the model: a column the model does not map, or a
// field the test did not think to name, is exactly where a kept token would hide.
func requireNoCredentialAtRest(t *testing.T, s *Store, db *gorm.DB) {
	t.Helper()
	ms := &core.Microservice{InstanceId: "inst", FunctionalArea: "command-delivery"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	w := &capturedLetters{}
	sink := deadletter.NewProducer(ms).NewSink(w)
	require.NoError(t, sink.Write(core.WithTenant(context.Background(), "acme"), deadletter.Envelope{
		Kind: deadletter.KindCommandResponse, Reason: deadletter.ReasonExhausted,
		Summary: "a reply could not be recorded", OccurredAt: time.Now().UTC(),
		Payload: []byte(`{"correlationId":"c-1","credentialType":"access-token","credentialId":"` + atRestToken +
			`","payload":{"ok":1}}`),
	}))
	require.Len(t, w.got, 1)

	c := testConsumer(t, s)
	ack := &recordingAck{}
	msg := messaging.NewConsumedMessage("inst.acme.dead-letters", w.got[0].Value, 1, nil, ack)
	msg.StreamSeq, msg.AppendTime = 1, time.Now().UTC()
	c.handle(msg)
	require.Equal(t, 1, ack.acks, "the letter must have been stored, or the scan below proves nothing")

	rows, err := db.Raw("SELECT * FROM dead_letters").Rows()
	require.NoError(t, err)
	defer rows.Close()
	cols, err := rows.Columns()
	require.NoError(t, err)
	var stored strings.Builder
	n := 0
	for rows.Next() {
		n++
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		for _, v := range vals {
			stored.Write(v)
			stored.WriteByte('\n')
		}
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 1, n)
	// The counterweight: the scan reached the payload column, and it is the redacted body.
	require.Contains(t, stored.String(), `"credentialId":"`+payloadredact.Marker+`"`)
	require.Contains(t, stored.String(), `"credentialType":"access-token"`)
	require.NotContains(t, stored.String(), atRestToken)
}

func TestAStoredDeadLetterRowKeepsNoAccessToken(t *testing.T) {
	s := testStore(t)
	requireNoCredentialAtRest(t, s, s.rdbm.Database)
}
