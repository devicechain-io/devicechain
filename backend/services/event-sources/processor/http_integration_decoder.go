// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/devicechain-io/dc-event-sources/integration"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/eventlimit"
	"github.com/devicechain-io/dc-microservice/integrationcodec"
	"github.com/devicechain-io/dc-microservice/payloaddecode/plancost"
)

// The link keys an IntegrationUplink carries, which the decoder reads as `link`.
const (
	LinkRSSI     = "rssi"
	LinkSNR      = "snr"
	LinkGateways = "gateways"
)

// linkReadings is how many readings an integration with linkMetrics adds on top of a decoder's:
// rssi, snr and the gateway count. It is the same three the flatten path stores.
const linkReadings = 3

// costPerReading is how many units of a decoder's static cost the ingest charge counts as one
// reading. A decode of that much work is held to cost what a stored reading does, so a decoder
// that emits little or nothing (an ignore-only section, a decode that always fails) is still
// charged for the CPU it spends.
const costPerReading = 2000

// carrierRefusal is an uplink that cannot be carried to its decoder. The request is refused whole
// with a 400, like every other malformed uplink, and counted under Uplink.
type carrierRefusal struct {
	uplink string
	err    error
}

// DecoderCharge is what an uplink for a device type with a decoder is charged at the reading gate,
// before the 202:
//
//	max(1, maxReadings + (3 if the integration has linkMetrics), ceil(cost / 2000))
//
// where cost is the plan's static worst-case cost for THIS uplink (plancost.Shape.For, the same
// formula as payloaddecode.Plan.CostFor), with each parse priced by the size of the input carried.
//
// 🔴 THE THREE TERMS ARE A MAX, AND EACH ONE COVERS WHAT THE OTHER TWO CANNOT.
//   - maxReadings is what the decode stores, which is what the tenant's reading ceiling meters.
//   - the link readings are added after the decode, by the resolver, so they are never in
//     maxReadings.
//   - the cost term charges work that stores little or nothing. A decoder whose every section is
//     `ignore`, or whose decode fails, emits no readings and still spends its full static cost;
//     without this term it would be charged the floor of 1, and the tenant could spend the
//     platform's CPU at the price of a single reading.
//
// The floor of 1 is not decoration: the reading gate admits an uplink charged 0 or less for free.
//
// The charge is static and made before the 202, so a decoder that emits fewer readings than its
// maximum is charged its maximum (the over-charge residual), and a plan published since this
// replica asked is charged on the old plan's figures (device-management counts the skew).
func DecoderCharge(plan integration.DecoderPlan, linkMetrics bool, jsonLen, decodedLen int) int {
	readings := plan.MaxReadings
	if linkMetrics {
		readings += linkReadings
	}
	cost := plan.Cost.For(jsonLen, decodedLen)
	costTerm := cost / costPerReading
	if cost%costPerReading != 0 {
		costTerm++
	}
	n := max(1, readings)
	if costTerm > uint64(n) {
		n = int(min(costTerm, uint64(eventlimit.MaxReadingsPerEvent)))
	}
	return min(n, eventlimit.MaxReadingsPerEvent)
}

// buildUplinkCarrier turns an uplink into the payload of an IntegrationUplink and the number of
// readings it is charged. Nothing is flattened: the decoder reads the raw input in
// device-management.
//
// The strictness lives here, after the plan lookup, and only here. The codecs decode a frame, read
// the port and counter and measure nesting before the device or its plan is known, so they keep
// the error and decide nothing (integrationcodec.Uplink). Cut A, for a device type with no
// decoder, never reaches this function and accepts exactly what it always has.
func buildUplinkCarrier(desc *integrationcodec.Descriptor, up *integrationcodec.Uplink, plan integration.DecoderPlan) (*model.UnresolvedIntegrationUplinkPayload, int, *carrierRefusal) {
	// What the decoder can read. An input kind this build does not know (a decoder written by a
	// newer release) is carried in full: dropping an input the decoder reads would turn a working
	// decode into a missing_input failure.
	known := plan.Input == "FRAME" || plan.Input == "JSON" || plan.Input == "DECODED"
	needsFrame := plan.Input == "FRAME" || !known
	wantsJSON := plan.Input == "JSON" || plan.Cost.ParsesJSON || !known
	wantsDecoded := plan.Input == "DECODED" || plan.Cost.ParsesDecoded || !known

	payload := &model.UnresolvedIntegrationUplinkPayload{ProfileVersionToken: plan.ProfileVersionToken}

	// A frame that was written and did not decode is a malformed envelope for a decoder that needs
	// it. For one that does not, it is as harmless as it always was.
	if up.FrameErr != nil && needsFrame {
		return nil, 0, &carrierRefusal{UplinkBadFrame, fmt.Errorf("the frame is not valid %s: %v", up.FrameEncoding, up.FrameErr)}
	}
	if up.MetaErr != nil {
		return nil, 0, &carrierRefusal{UplinkBadMeta, fmt.Errorf("the envelope's fPort or fCnt is invalid: %v", up.MetaErr)}
	}
	if up.Frame != nil {
		switch {
		case len(up.Frame) <= plancost.MaxFrameBytes:
			payload.Frame = up.Frame
		case needsFrame:
			return nil, 0, &carrierRefusal{UplinkTooLargeInput, fmt.Errorf("the frame is %d bytes, over the limit of %d", len(up.Frame), plancost.MaxFrameBytes)}
		}
	}
	if up.FPort != integrationcodec.NoLoRaWAN {
		if up.FPort < 0 || up.FPort > math.MaxInt32 {
			return nil, 0, &carrierRefusal{UplinkBadMeta, fmt.Errorf("fPort %d is out of range", up.FPort)}
		}
		v := int32(up.FPort)
		payload.FPort = &v
	}
	if up.FCnt != integrationcodec.NoLoRaWAN {
		if up.FCnt < 0 || up.FCnt > math.MaxUint32 {
			return nil, 0, &carrierRefusal{UplinkBadMeta, fmt.Errorf("fCnt %d is out of range", up.FCnt)}
		}
		v := uint32(up.FCnt)
		payload.FCnt = &v
	}

	// The object: a generic sender's item is `json`, a network server's decoded object is
	// `decoded`. Deeper than the cap is refused before it is carried, and the codec has already
	// bounded its size, which is checked again here because this is the cap the carrier promises.
	if up.Decoded != nil {
		toJSON := desc.Type == integrationcodec.GenericJSON
		if (toJSON && wantsJSON) || (!toJSON && wantsDecoded) {
			if up.DepthErr != nil {
				return nil, 0, &carrierRefusal{UplinkTooDeep, up.DepthErr}
			}
			raw, err := json.Marshal(up.Decoded)
			if err != nil {
				return nil, 0, &carrierRefusal{UplinkBadMeta, fmt.Errorf("the decoded object cannot be carried: %v", err)}
			}
			if len(raw) > plancost.MaxJSONBytes {
				return nil, 0, &carrierRefusal{UplinkTooLargeInput, fmt.Errorf("the decoded object is %d bytes, over the limit of %d", len(raw), plancost.MaxJSONBytes)}
			}
			if toJSON {
				payload.Json = raw
			} else {
				payload.Decoded = raw
			}
		}
	}

	if desc.LinkMetrics && up.Link != nil {
		link := map[string]float64{LinkGateways: float64(up.Link.Gateways)}
		if up.Link.HasSignal {
			link[LinkRSSI], link[LinkSNR] = up.Link.RSSI, up.Link.SNR
		}
		payload.Link = link
	}

	n := DecoderCharge(plan, desc.LinkMetrics, len(payload.Json), len(payload.Decoded))
	payload.MaxReadings = uint32(n)
	return payload, n, nil
}
