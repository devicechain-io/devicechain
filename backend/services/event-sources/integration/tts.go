// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/devicechain-io/dc-microservice/devicealias"
)

// The Things Stack v3 JSON.
//
// The field names were checked against the upstream source, not written from memory:
//   - lorawan-stack api/ttn/lorawan/v3 messages.proto: ApplicationUp has end_device_ids (1),
//     correlation_ids (2), received_at (12) and the oneof member uplink_message (3);
//     ApplicationUplink has f_port (2), f_cnt (3), frm_payload (4, bytes), decoded_payload (5, a
//     Struct), rx_metadata (6) and received_at (8).
//   - identifiers.proto: EndDeviceIdentifiers has device_id (1), application_ids (2), dev_eui
//     (4), join_eui (5) and dev_addr (6); metadata.proto: RxMetadata has gateway_ids (1), rssi
//     (8), channel_rssi (9) and snr (11).
//   - the webhook body is protobuf-JSON with the original snake_case names, as in The Things
//     Stack's published uplink example: dev_eui is an upper-case hex string, f_cnt is omitted
//     when it is 0, and frm_payload is base64.
//   - a message type other than an uplink has a different member of the oneof in place of
//     uplink_message (join_accept, downlink_ack, location_solved, ...), which is how it is told
//     apart from an uplink. The webhook posts each type to a path of its own.
//
// The recorded bodies under testdata/tts are the goldens that pin this.

type ttsUp struct {
	EndDeviceIds struct {
		DeviceId string `json:"device_id"`
		DevEui   string `json:"dev_eui"`
	} `json:"end_device_ids"`
	ReceivedAt    *string `json:"received_at"`
	UplinkMessage *struct {
		FCnt           json.Number     `json:"f_cnt"`
		FrmPayload     string          `json:"frm_payload"`
		DecodedPayload json.RawMessage `json:"decoded_payload"`
		ReceivedAt     *string         `json:"received_at"`
		RxMetadata     []struct {
			Rssi *float64 `json:"rssi"`
			Snr  *float64 `json:"snr"`
		} `json:"rx_metadata"`
	} `json:"uplink_message"`
}

// ttsUplinkPath says whether a webhook path segment names the uplink message type. The type is
// whatever the tenant configured The Things Stack to post to, so the common spellings are
// accepted; any other segment is a different message type and is ignored.
func ttsUplinkPath(segment string) bool {
	s := strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(segment))
	switch s {
	case "up", "uplink", "uplinks", "uplinkmessage":
		return true
	}
	return false
}

func decodeTTS(d *Descriptor, r *Request) (*Result, error) {
	if r.MessageType != "" && !ttsUplinkPath(r.MessageType) {
		return &Result{Ignored: true}, nil
	}
	if !isJSONContent(r.ContentType) {
		return nil, malformed("the body is not JSON (content type %q)", clip(r.ContentType))
	}
	var up ttsUp
	if err := parseDocument(r.Body, &up); err != nil {
		return nil, err
	}
	if up.UplinkMessage == nil {
		return &Result{Ignored: true}, nil
	}
	m := up.UplinkMessage

	scheme := devicealias.SchemeDevEUI
	raw := up.EndDeviceIds.DevEui
	if d.Identity == identityDeviceID {
		scheme, raw = devicealias.SchemeTTSDeviceID, up.EndDeviceIds.DeviceId
	}
	id, err := normalized(scheme, raw)
	if err != nil {
		return nil, err
	}

	// The dedup key is the device, its frame counter and the time the network server received the
	// frame. The counter alone is not enough: it restarts when a device rejoins.
	stamp := m.ReceivedAt
	if stamp == nil {
		stamp = up.ReceivedAt
	}
	if stamp == nil || *stamp == "" {
		return nil, malformed("the uplink has no received_at")
	}
	at, _, err := eventTime(stamp, "received_at", r)
	if err != nil {
		return nil, err
	}
	fcnt := "0"
	if s := m.FCnt.String(); s != "" {
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return nil, malformed("f_cnt is not a frame counter: %q", clip(s))
		}
		fcnt = strconv.FormatUint(n, 10)
	}
	deviceKey := id
	if scheme != devicealias.SchemeDevEUI && up.EndDeviceIds.DevEui != "" {
		if eui, err := devicealias.Normalize(devicealias.SchemeDevEUI, up.EndDeviceIds.DevEui); err == nil {
			deviceKey = eui
		}
	}
	object, err := objectOf(m.DecodedPayload, "decoded_payload")
	if err != nil {
		return nil, err
	}
	u := Uplink{
		Identifier: id, Scheme: scheme,
		AltId: "tts:" + deviceKey + ":" + fcnt + ":" + *stamp,
		Time:  at, SenderTime: true, Decoded: object,
	}
	if m.FrmPayload != "" {
		if frame, err := base64.StdEncoding.DecodeString(m.FrmPayload); err == nil {
			u.Frame = frame
		}
	}
	if d.LinkMetrics {
		link := &Link{Gateways: len(m.RxMetadata)}
		for _, rx := range m.RxMetadata {
			if rx.Rssi == nil || rx.Snr == nil {
				continue
			}
			if !link.HasSignal || *rx.Rssi > link.RSSI {
				link.RSSI, link.SNR, link.HasSignal = *rx.Rssi, *rx.Snr, true
			}
		}
		u.Link = link
	}
	return &Result{Uplinks: []Uplink{u}}, nil
}
