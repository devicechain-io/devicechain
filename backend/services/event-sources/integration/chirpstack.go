// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"encoding/base64"
	"encoding/json"

	"github.com/devicechain-io/dc-microservice/devicealias"
)

// ChirpStack v4 JSON.
//
// The field names were checked against the upstream source, not written from memory:
//   - integration.proto (chirpstack/chirpstack, api/proto/integration): UplinkEvent has
//     deduplication_id (1), time (2), device_info (3), dev_addr (4), f_cnt (7), f_port (8),
//     data (10), object (11, a Struct that "is only set when a codec is configured in the
//     Device Profile") and rx_info (12); DeviceInfo has dev_eui (8); gw.UplinkRxInfo has
//     gateway_id (1), rssi (6, int32) and snr (7, float).
//   - the HTTP integration (chirpstack/src/integration/http.rs) POSTs every event with the query
//     parameter event=up|join|ack|txack|log|status|location, and with Content-Type
//     application/json when its JSON encoding is on and application/octet-stream (Protobuf)
//     otherwise. Its custom headers are copied onto the request, which is how the bearer token
//     arrives.
//   - the JSON form is protobuf-JSON, so keys are lowerCamelCase (deduplicationId, deviceInfo,
//     devEui, rxInfo, gatewayId), as in ChirpStack's published event examples. A field at its
//     default (fCnt 0) may be absent.
//
// The recorded bodies under testdata/chirpstack are the goldens that pin this.

type csUplink struct {
	DeduplicationId string  `json:"deduplicationId"`
	Time            *string `json:"time"`
	DeviceInfo      struct {
		DevEui string `json:"devEui"`
	} `json:"deviceInfo"`
	Data   string          `json:"data"`
	Object json.RawMessage `json:"object"`
	RxInfo []struct {
		Rssi *float64 `json:"rssi"`
		Snr  *float64 `json:"snr"`
	} `json:"rxInfo"`
}

func decodeChirpStack(d *Descriptor, r *Request) (*Result, error) {
	// A Protobuf body is not something to guess at: it is a setting on the sender's side, and the
	// refusal says which.
	if !isJSONContent(r.ContentType) {
		return nil, ErrUnsupportedEncoding
	}
	switch ev := r.Query.Get("event"); ev {
	case "up":
	case "":
		return nil, malformed("the request names no event: add ?event=up, which ChirpStack's HTTP integration appends itself")
	default:
		return &Result{Ignored: true}, nil
	}
	var up csUplink
	if err := parseDocument(r.Body, &up); err != nil {
		return nil, err
	}
	if up.DeduplicationId == "" {
		return nil, malformed("the uplink has no deduplicationId")
	}
	eui, err := normalized(devicealias.SchemeDevEUI, up.DeviceInfo.DevEui)
	if err != nil {
		return nil, err
	}
	at, sender, err := eventTime(up.Time, "time", r)
	if err != nil {
		return nil, err
	}
	object, err := objectOf(up.Object, "object")
	if err != nil {
		return nil, err
	}
	u := Uplink{
		Identifier: eui, Scheme: devicealias.SchemeDevEUI,
		AltId: "cs:" + up.DeduplicationId, Time: at, SenderTime: sender, Decoded: object,
	}
	if up.Data != "" {
		if frame, err := base64.StdEncoding.DecodeString(up.Data); err == nil {
			u.Frame = frame
		}
	}
	if d.LinkMetrics {
		link := &Link{Gateways: len(up.RxInfo)}
		for _, rx := range up.RxInfo {
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
