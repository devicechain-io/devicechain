// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/lorawan"
)

// The LoRaWAN tide gauges. They are battery devices behind a network server, which this process
// plays: on every tick each gauge sends one uplink through the scenario's ChirpStack v4 integration,
// carrying a raw application frame and no decoded object, so the PLATFORM decodes it with the
// tenant's payload decoder on the gauge's profile.
//
// Two frame layouts are on the air, alternating uplink by uplink, because the gauges' firmware
// added a status byte:
//
//	layout 1, 5 bytes: u8 1 | i16 BE level, cm | u16 BE battery, mV
//	layout 2, 6 bytes: u8 2 | i16 BE level, cm | u16 BE battery, mV | u8 status
//
// The current decoder reads both. The legacy gauge's profile still carries the decoder written when
// layout 2 was planned with the status byte BEFORE the battery, so it reads the battery two bytes
// too far on, past the end of the frame: every layout-2 uplink of that gauge fails to decode
// (eval_error) and is recorded as DECODE_FAILED, while its layout-1 uplinks decode. That is the
// failure the decoder health has to show, beside healthy gauges on the same integration.

// HarborpulseLoraFPort is the application port the gauges send on.
const HarborpulseLoraFPort = 2

// HarborpulseLoraDecoder is the current gauge decoder: both layouts. Its vectors gate the profile's
// publish, so a decoder that misreads either layout cannot be published.
const HarborpulseLoraDecoder = `{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "layout-1",
      "when": "fPort == 2 && u8(frame, 0) == 1",
      "outputs": [
        {"key": "water_level_m", "expr": "double(i16be(frame, 1)) / 100.0"},
        {"key": "battery_v", "expr": "double(u16be(frame, 3)) / 1000.0"}
      ]
    },
    {
      "name": "layout-2",
      "when": "fPort == 2 && u8(frame, 0) == 2",
      "outputs": [
        {"key": "water_level_m", "expr": "double(i16be(frame, 1)) / 100.0"},
        {"key": "battery_v", "expr": "double(u16be(frame, 3)) / 1000.0"}
      ]
    }
  ],
  "vectors": [
    {"name": "layout 1", "input": {"frameHex": "01007b0e10", "fPort": 2}, "expect": {"water_level_m": 1.23, "battery_v": 3.6}},
    {"name": "layout 2", "input": {"frameHex": "02ff9c0e1000", "fPort": 2}, "expect": {"water_level_m": -1, "battery_v": 3.6}}
  ]
}`

// harborpulseLegacyDecoder is the broken one: layout 2's battery is read at offset 5, past the end
// of a 6-byte frame. Its one vector is layout 1, which it reads correctly, so the profile publishes.
const harborpulseLegacyDecoder = `{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "layout-1",
      "when": "fPort == 2 && u8(frame, 0) == 1",
      "outputs": [
        {"key": "water_level_m", "expr": "double(i16be(frame, 1)) / 100.0"},
        {"key": "battery_v", "expr": "double(u16be(frame, 3)) / 1000.0"}
      ]
    },
    {
      "name": "layout-2",
      "when": "fPort == 2 && u8(frame, 0) == 2",
      "outputs": [
        {"key": "water_level_m", "expr": "double(i16be(frame, 1)) / 100.0"},
        {"key": "battery_v", "expr": "double(u16be(frame, 5)) / 1000.0"}
      ]
    }
  ],
  "vectors": [
    {"name": "layout 1", "input": {"frameHex": "01007b0e10", "fPort": 2}, "expect": {"water_level_m": 1.23, "battery_v": 3.6}}
  ]
}`

// harborLegacyDecoder is the legacy gauge's decoder: the broken one, or the current one under the
// legacy-decoder-fixed defect.
func harborLegacyDecoder(fixed bool) string {
	if fixed {
		return HarborpulseLoraDecoder
	}
	return harborpulseLegacyDecoder
}

// HarborpulseLoraGauges are the LoRaWAN gauges on the current decoder, and HarborpulseLegacyGauge
// the one on the broken decoder.
func HarborpulseLoraGauges() []string {
	var out []string
	for _, d := range harborLayout() {
		if d.kind == harborLoraGauge && d.token != harborLegacyGauge {
			out = append(out, d.token)
		}
	}
	return out
}

// HarborpulseLegacyGauge is the gauge whose decoder fails on layout 2.
const HarborpulseLegacyGauge = harborLegacyGauge

// harborLoraLevelCm is the level a gauge encodes at elapsed, in centimetres: the same tide, surge
// and quay offset as the HTTP gauges.
func harborLoraLevelCm(seed int64, d harborDevice, elapsed, dayLength time.Duration) float64 {
	sample := int64(elapsed / time.Second)
	return math.Round(harborGaugeLevel(seed, d, harborHourOf(elapsed, dayLength), sample) * 100)
}

// HarborpulseLoraLevelAt is the water level a correctly decoded uplink of gauge token carries, when
// the uplink was taken elapsed into a run of seed with days of dayLength. A verifier holds the
// stored readings to it.
func HarborpulseLoraLevelAt(seed int64, token string, elapsed, dayLength time.Duration) (float64, bool) {
	for _, d := range harborLayout() {
		if d.token == token && d.kind == harborLoraGauge {
			return harborLoraLevelCm(seed, d, elapsed, dayLength) / 100, true
		}
	}
	return 0, false
}

// harborLoraFrame is the n-th uplink frame of a gauge: layout 1 on even n, layout 2 on odd. The
// lora-wrong-scale defect encodes millimetres.
func harborLoraFrame(seed int64, d harborDevice, n int, elapsed, dayLength time.Duration, wrongScale bool) []byte {
	level := harborLoraLevelCm(seed, d, elapsed, dayLength)
	if wrongScale {
		level *= 10
	}
	battery := math.Round((3.6 + harborNoise(seed, d.token, int64(elapsed/time.Second), 0.01)) * 1000)
	b := make([]byte, 5, 6)
	b[0] = 1
	binary.BigEndian.PutUint16(b[1:], uint16(int16(math.Max(math.MinInt16, math.Min(math.MaxInt16, level)))))
	binary.BigEndian.PutUint16(b[3:], uint16(battery))
	if n%2 == 1 {
		b[0] = 2
		b = append(b, 0)
	}
	return b
}

// harborUplink is one uplink to send: the device and its frame.
type harborUplink struct {
	devEUI string
	n      int
	frame  []byte
}

// uplinks advances every LoRaWAN gauge's frame counter and returns this tick's frames.
func (m *harborModel) loraUplinks(elapsed time.Duration) []harborUplink {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []harborUplink
	for _, d := range harborLayout() {
		if d.kind != harborLoraGauge {
			continue
		}
		n := m.uplinks[d.token]
		m.uplinks[d.token] = n + 1
		out = append(out, harborUplink{devEUI: d.devEUI, n: n,
			frame: harborLoraFrame(m.seed, d, n, elapsed, m.dayLength, m.defects.loraWrongScale)})
	}
	return out
}

// sendLoraUplinks plays the network server for one tick: each gauge's uplink, posted through the
// integration with the bearer token Provision was issued. A refused uplink is counted and
// reported, never retried: a network server forwards what it heard once.
func (s *harborpulse) sendLoraUplinks(ctx context.Context, rt *Runtime, model *harborModel, elapsed time.Duration, at time.Time) error {
	token := rt.IntegrationTokens[HarborpulseIntegration]
	if token == "" || strings.TrimSpace(rt.Endpoints.Integrations) == "" {
		return fmt.Errorf("no token or endpoint for integration %s: the gauges' uplinks have nowhere to go", HarborpulseIntegration)
	}
	cfg := lorawan.Config{
		NS:  lorawan.ChirpStack,
		URL: strings.TrimRight(rt.Endpoints.Integrations, "/") + "/" + rt.Tenant + "/" + HarborpulseIntegration,
	}
	runID := strconv.FormatInt(s.startedAt().UnixNano(), 10)
	var errs []error
	for _, u := range model.loraUplinks(elapsed) {
		if u.devEUI == HarborpulseSamplingDevEUI {
			s.mu.Lock()
			next := s.s3b.nextUplink
			s.mu.Unlock()
			if elapsed < next {
				continue
			}
		}
		pending, pollErr := s.pollGaugeQueue(ctx, rt, u.devEUI)
		if pollErr != nil {
			errs = append(errs, pollErr)
			continue
		}
		s.mu.Lock()
		if s.s3b.pending == nil {
			s.s3b.pending = map[string][]lorawan.PendingDownlink{}
		}
		for _, item := range pending {
			duplicate := s.s3b.seen[item.ID]
			for _, queued := range s.s3b.pending[u.devEUI] {
				duplicate = duplicate || queued.ID == item.ID
			}
			if !duplicate {
				s.s3b.pending[u.devEUI] = append(s.s3b.pending[u.devEUI], item)
			}
		}
		pending = append([]lorawan.PendingDownlink(nil), s.s3b.pending[u.devEUI]...)
		s.mu.Unlock()
		req, err := lorawan.BuildFrame(cfg, runID, u.devEUI, u.n, at, HarborpulseLoraFPort, u.frame)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, bytes.NewReader(req.Body))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		hr.Header.Set("Content-Type", req.ContentType)
		hr.Header.Set("Authorization", "Bearer "+token)
		resp, err := rt.HTTPClient.Do(hr)
		if err != nil {
			rt.Stats.Failed.Add(1)
			errs = append(errs, fmt.Errorf("uplink %s: %w", u.devEUI, err))
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			rt.Stats.Failed.Add(1)
			errs = append(errs, fmt.Errorf("uplink %s: the integration answered HTTP %d", u.devEUI, resp.StatusCode))
			continue
		}
		rt.Stats.Emitted.Add(1)
		if err := s.answerGaugeQueue(ctx, rt, pending, elapsed, at); err != nil {
			errs = append(errs, err)
		}
		s.mu.Lock()
		var unanswered []lorawan.PendingDownlink
		for _, item := range s.s3b.pending[u.devEUI] {
			if !s.s3b.seen[item.ID] {
				unanswered = append(unanswered, item)
			}
		}
		s.s3b.pending[u.devEUI] = unanswered
		s.mu.Unlock()
		if u.devEUI == HarborpulseSamplingDevEUI {
			s.mu.Lock()
			s.s3b.nextUplink = elapsed + s.s3b.interval
			s.mu.Unlock()
		}
	}
	return errors.Join(errs...)
}
