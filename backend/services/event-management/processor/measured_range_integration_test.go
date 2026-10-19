// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package processor

import (
	"fmt"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// Which measured values the numeric(20,8) column takes, observed on a real server: a batch of
// eight where one message carries the value under test. The ones the column refuses must cost
// that message only (never the batch); the ones it takes must be values a reader can use.
func TestMeasuredValuesAtTheEdgeOfTheColumnOnPostgres(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		stored      bool
	}{
		{"eleven integer digits", "99999999999.5", true},
		{"1e-12 rounds to zero", "1e-12", true},
		{"1e12 is one digit too many", "1e12", false},
		{"1e21 in exponent form", "1.25e+21", false},
		{"positive infinity", "+Inf", false},
		{"negative infinity", "-Inf", false},
		{"not a number", "NaN", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPgBatchRig(t, "emrangetest", 8)
			tenant := fmt.Sprintf("mr%d", time.Now().UnixNano())
			base := time.Now().UTC().Truncate(time.Hour)
			var msgs []messaging.Message
			for i := 0; i < 8; i++ {
				ev := batchEvent(i, true, base)
				if i == 3 {
					ev.Payload.(*dmodel.ResolvedMeasurementsPayload).Entries[0].Entries[0].Value = tc.value
				}
				msgs = append(msgs, consumed(t, r.acks, i, tenant, 1, ev))
			}
			r.run(msgs)

			want := int64(7)
			if tc.stored {
				want = 8
			}
			if got := r.count(t, tenant, ""); got != want {
				t.Errorf("stored %d events; want %d", got, want)
			}
			if got := len(r.acks.acked()); got != 8 {
				t.Errorf("%d messages acknowledged; want 8 (a poison batch is redelivered, not acked)", got)
			}
			if !tc.stored && len(r.failures) != 1 {
				t.Errorf("reported %+v; want exactly the one refused message", r.failures)
			}
		})
	}
}
