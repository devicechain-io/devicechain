// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integrationcodec

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenericAckPaths(t *testing.T) {
	for path, want := range map[string]bool{"ack": true, "ACK": true, "downlink-ack": true, "downlink/nack": true, "downlink_failed": true, "acknowledge": false, "ackx": false, "": false, "uplink": false} {
		require.Equal(t, want, IsGenericAckPath(path), path)
	}
}
func TestGenericAckDecode(t *testing.T) {
	key := "dc:0123456789abcdef0123456789abcdef"
	for _, source := range []string{"header:X-Device", "query:device", "pointer:/device/id"} {
		for _, status := range []string{"acknowledged", "timeout", "failed"} {
			t.Run(source+status, func(t *testing.T) {
				d := generic(GenericJSON, `{"idFrom":"`+source+`","itemsPointer":"/uplinks"}`)
				b, _ := json.Marshal(map[string]any{"device": map[string]string{"id": "Pump-1"}, "correlationId": key, "status": status})
				r := req(b, "")
				r.MessageType = "downlink/ack"
				r.Header = http.Header{"X-Device": []string{"Pump-1"}}
				r.Query = url.Values{"device": []string{"Pump-1"}}
				got, err := Decode(d, r)
				require.NoError(t, err)
				require.Empty(t, got.Uplinks)
				require.False(t, got.Ignored)
				want := DownlinkEvent{Kind: DownlinkAck, Scheme: "custom.pump", Identifier: "Pump-1", QueueItemID: key, DeviceRef: "webhook", Acknowledged: status == "acknowledged"}
				if status == "failed" {
					want.Kind = DownlinkFailed
					want.ErrorNamespace = "webhook"
					want.ErrorName = "failed"
				}
				require.Equal(t, []DownlinkEvent{want}, got.Downlinks)
			})
		}
	}
	d := generic(GenericJSON, `{"idFrom":"header:X-Device"}`)
	for name, body := range map[string]string{
		"bad status":          `{"correlationId":"` + key + `","status":"ACKNOWLEDGED"}`,
		"missing status":      `{"correlationId":"` + key + `"}`,
		"missing correlation": `{"status":"acknowledged"}`,
		"no prefix":           `{"correlationId":"1234","status":"acknowledged"}`,
		"uppercase hex":       `{"correlationId":"dc:AB12","status":"acknowledged"}`,
		"empty hex":           `{"correlationId":"dc:","status":"acknowledged"}`,
		"long key":            `{"correlationId":"dc:` + strings.Repeat("a", 98) + `","status":"acknowledged"}`,
		"array":               `[{"correlationId":"` + key + `","status":"acknowledged"}]`,
		"null":                `null`, "too large": `{"padding":"` + strings.Repeat("a", MaxDecodedBytes) + `","correlationId":"` + key + `","status":"acknowledged"}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := req([]byte(body), "")
			r.MessageType = "ack"
			r.Header.Set("X-Device", "Pump-1")
			_, err := Decode(d, r)
			require.True(t, errors.Is(err, ErrMalformed), "%v", err)
		})
	}
	r := req([]byte(`{"correlationId":"`+key+`","status":"acknowledged"}`), "")
	r.MessageType = "ack"
	r.Header.Set("X-Device", "Pump-1")
	r.ContentType = "text/plain"
	_, err := Decode(d, r)
	require.ErrorIs(t, err, ErrMalformed)
	r.DownlinkReports = false
	r.Body = []byte("invalid")
	got, err := Decode(d, r)
	require.NoError(t, err)
	require.True(t, got.Ignored)
	r.DownlinkReports = true
	got, err = Decode(generic(GenericBinary, `{"idFrom":"header:X-Device"}`), r)
	require.NoError(t, err)
	require.True(t, got.Ignored)
}
