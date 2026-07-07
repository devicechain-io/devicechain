// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"math"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

// Sim is the headless Go reference driver for one scenario: it describes its
// topology (Manifest), provisions it once (Bootstrap), and advances one time
// step (Tick).
//
// IMPORTANT: this Go interface is the reference driver, NOT the wire contract
// (sim-subsystem-contract.md §3c). A future Unity (or any other language) sim
// implements the four wire seams directly — device-plane ingress, tenant
// GraphQL provisioning, the control API, and graphql-ws subscribe — and never
// needs to implement this interface. Two sims that never share a line of Go can
// still interoperate with the same platform because the contract lives on the
// wire, not in this type.
type Sim interface {
	// Manifest returns the scenario's declarative topology.
	Manifest() SimManifest
	// Bootstrap provisions the manifest's topology (idempotent — see Provision).
	Bootstrap(ctx context.Context, rt *Runtime) error
	// Tick advances the simulation by one step, typically emitting telemetry.
	Tick(ctx context.Context, rt *Runtime) error
}

// Devicepulse profile/device-type/population tokens — fixed, not derived from
// any handshake field, since this manifest is a static built-in scenario.
const (
	DevicepulseProfileToken    = "devicepulse-profile"
	DevicepulseDeviceTypeToken = "devicepulse-vehicle"
	DevicepulseMetricKey       = "speed_kph"
)

// devicepulse is the slice-1 MVP scenario (sim-slice1-devicepulse-spec.md): one
// device, one numeric metric, emitted every EmitInterval. It proves the whole
// loop — provisioning, ingress, and live read-back — with the smallest possible
// topology.
type devicepulse struct {
	// seed drives all deterministic generation (token/externalId/credential
	// derivation), threaded from the handshake so a given (sim, seed) always
	// renders the same topology and reset is reproducible.
	seed int64
	// ticks counts Tick calls since process start, driving a smooth synthetic
	// speed curve. Atomic since the control API and the tick loop are on
	// different goroutines even though only one ever calls Tick.
	ticks atomic.Int64
}

// NewDevicepulse returns the devicepulse reference Sim seeded from the handshake.
func NewDevicepulse(seed int64) Sim {
	return &devicepulse{seed: seed}
}

func (s *devicepulse) Manifest() SimManifest {
	return SimManifest{
		Name: "devicepulse",
		Seed: s.seed,
		Profiles: []ProfileSpec{
			{
				Token:    DevicepulseProfileToken,
				Name:     "Devicepulse Vehicle Profile",
				Category: "vehicle",
				Metrics: []MetricSpec{
					{Key: DevicepulseMetricKey, Name: "Speed", DataType: "DOUBLE", Unit: "kph"},
				},
			},
		},
		DeviceTypes: []DeviceTypeSpec{
			{Token: DevicepulseDeviceTypeToken, Name: "Devicepulse Vehicle", ProfileToken: DevicepulseProfileToken},
		},
		Populations: []PopulationSpec{
			{
				OfType:            DevicepulseDeviceTypeToken,
				Count:             1,
				TokenPattern:      "devicepulse-{n:05d}",
				ExternalIdPattern: "VIN-DEVICEPULSE-{n:05d}",
			},
		},
	}
}

func (s *devicepulse) Bootstrap(ctx context.Context, rt *Runtime) error {
	return Provision(ctx, rt, s.Manifest())
}

// Tick emits one speed_kph measurement per provisioned device. The value
// traces a smooth, bounded sine wave (30-70 kph) purely as a function of the
// tick count — a demo needs *something* moving on the presentation page, not
// physically accurate telemetry.
func (s *devicepulse) Tick(ctx context.Context, rt *Runtime) error {
	n := s.ticks.Add(1)
	speed := 50 + 20*math.Sin(float64(n)*0.3)

	for _, d := range rt.Devices {
		if err := EmitMeasurement(ctx, rt, d, DevicepulseMetricKey, speed); err != nil {
			log.Error().Err(err).Str("device", d.Token).Msg("emit measurement failed")
			return err
		}
	}
	return nil
}
