// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integrationcodec

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

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
		DeviceId       string `json:"device_id"`
		DevEui         string `json:"dev_eui"`
		ApplicationIds struct {
			ApplicationId string `json:"application_id"`
		} `json:"application_ids"`
	} `json:"end_device_ids"`
	ReceivedAt     *string      `json:"received_at"`
	DownlinkAck    *ttsDownlink `json:"downlink_ack"`
	DownlinkNack   *ttsDownlink `json:"downlink_nack"`
	DownlinkSent   *ttsDownlink `json:"downlink_sent"`
	DownlinkFailed *struct {
		Downlink *ttsDownlink `json:"downlink"`
		Error    *struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"error"`
	} `json:"downlink_failed"`
	UplinkMessage *struct {
		FPort          json.RawMessage `json:"f_port"`
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

// ttsDownlink is the part of an ApplicationDownlink a report is matched by. Its correlation_ids are
// the ones the downlink was pushed with; the message's own correlation_ids are the network
// server's trace of the event and are never used.
type ttsDownlink struct {
	CorrelationIds []string `json:"correlation_ids"`
	// ConfirmedRetry is set by the Application Server when it re-queues a confirmed downlink after a
	// nack. proto3 JSON omits attempt at 0.
	ConfirmedRetry *struct {
		Attempt json.RawMessage `json:"attempt"`
	} `json:"confirmed_retry"`
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
		if !r.DownlinkReports {
			return &Result{Ignored: true}, nil
		}
		// Another message type. The ones that report on a downlink the platform pushed are read
		// whatever path they were posted to (the body says which they are); a body that cannot be
		// read as one is ignored, as every other message type always was.
		var up ttsUp
		if json.Unmarshal(r.Body, &up) != nil {
			return &Result{Ignored: true}, nil
		}
		return decodeTTSDownlink(d, &up)
	}
	if !isJSONContent(r.ContentType) {
		return nil, malformed("the body is not JSON (content type %q)", clip(r.ContentType))
	}
	var up ttsUp
	if err := parseDocument(r.Body, &up); err != nil {
		return nil, err
	}
	if up.UplinkMessage == nil {
		if !r.DownlinkReports {
			return &Result{Ignored: true}, nil
		}
		return decodeTTSDownlink(d, &up)
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
	var fCnt int64
	if s := m.FCnt.String(); s != "" {
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return nil, malformed("f_cnt is not a frame counter: %q", clip(s))
		}
		fcnt = strconv.FormatUint(n, 10)
		fCnt = int64(n)
	}
	fPort, portErr := loraPort(m.FPort, "f_port")
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
		AltId: "tts:" + deviceKey + ":" + fcnt + ":" + at.Format(time.RFC3339Nano),
		Time:  at, SenderTime: true, Decoded: object,
	}
	u.setMeta(fPort, portErr, fCnt, nil)
	if m.FrmPayload != "" {
		u.SetFrame(m.FrmPayload, EncodingBase64)
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

// decodeTTSDownlink reads a The Things Stack message that reports on a downlink: downlink_ack (the
// device acknowledged a confirmed downlink), downlink_nack (it did not), downlink_sent (a gateway
// transmitted it) and downlink_failed (the network server could not send it). Anything else is
// ignored, and so is a report with no correlation id the platform set: that downlink was queued
// by somebody else.
func decodeTTSDownlink(d *Descriptor, up *ttsUp) (*Result, error) {
	var (
		kind  string
		acked bool
		dl    *ttsDownlink
	)
	switch {
	case up.DownlinkAck != nil:
		kind, acked, dl = DownlinkAck, true, up.DownlinkAck
	case up.DownlinkNack != nil:
		kind, dl = DownlinkAck, up.DownlinkNack
	case up.DownlinkSent != nil:
		kind, dl = DownlinkTxAck, up.DownlinkSent
	case up.DownlinkFailed != nil && up.DownlinkFailed.Downlink != nil:
		kind, dl = DownlinkFailed, up.DownlinkFailed.Downlink
	default:
		return &Result{Ignored: true}, nil
	}
	key := ""
	for _, c := range dl.CorrelationIds {
		if strings.HasPrefix(c, DownlinkCorrelationPrefix) && len(c) <= 100 {
			key = c
			break
		}
	}
	if key == "" {
		// A downlink somebody queued by hand: nothing of the platform's to follow.
		return &Result{Ignored: true, Untracked: true}, nil
	}
	scheme := devicealias.SchemeDevEUI
	raw := up.EndDeviceIds.DevEui
	if d.Identity == identityDeviceID {
		scheme, raw = devicealias.SchemeTTSDeviceID, up.EndDeviceIds.DeviceId
	}
	id, err := normalized(scheme, raw)
	if err != nil {
		return nil, err
	}
	if up.EndDeviceIds.ApplicationIds.ApplicationId == "" || up.EndDeviceIds.DeviceId == "" {
		return nil, malformed("the downlink report names no application or device id")
	}
	ev := DownlinkEvent{
		Kind: kind, Scheme: scheme, Identifier: id, QueueItemID: key, Acknowledged: acked,
		DeviceRef: up.EndDeviceIds.ApplicationIds.ApplicationId + "/" + up.EndDeviceIds.DeviceId,
	}
	if kind == DownlinkFailed {
		// What the failure came after matters more than the failure: the Application Server also emits
		// downlink_failed when it gives up retrying a confirmed downlink that WAS transmitted (retries
		// exhausted, or the re-push failed), so a failed report is only ever "never transmitted" when
		// no retry preceded it and the error is one of the network server's own drops.
		if dl.ConfirmedRetry != nil {
			if text, present := loraNumber(dl.ConfirmedRetry.Attempt); present {
				if n, err := strconv.ParseUint(text, 10, 32); err == nil {
					ev.Attempt = int(n)
				} else {
					// An attempt that cannot be read is not 0: it is read as a retry that happened.
					ev.Attempt = 1
				}
			}
		}
		if e := up.DownlinkFailed.Error; e != nil {
			ev.ErrorNamespace, ev.ErrorName = e.Namespace, e.Name
		}
	}
	return &Result{Downlinks: []DownlinkEvent{ev}}, nil
}
