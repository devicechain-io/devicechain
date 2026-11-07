// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// citypulse is the demo data for the experimental outcome objects: a small city of four
// districts (areas), five street nodes in each, every node reporting three measurements the
// three seeded outcomes are judged on:
//
//	lamp_ok           1 when the lamp is on, 0 when it is out
//	pm25              fine particulate, micrograms per cubic metre
//	parking_free_pct  share of the node's bays that are free
//
// The city is built to be UNEVEN on purpose, because that is the thing an outcome exists to
// show: three districts are healthy and one (Eastside, the fourth) has a third of its lamps
// failing in a third of the time blocks, a dip in parking, and an air-quality spike in the
// third district, so the city-wide figure can pass while a neighbourhood does not.
//
// Everything is a pure function of (district, node, unix second): no random source, so the shape
// of the answer is testable without a platform. It is keyed to the WALL CLOCK, not to a tick
// counter, on purpose: a behaviour block is five minutes aligned to the clock, the same
// boundaries the platform's five-minute buckets use, so a bad block is exactly one bad bucket
// whenever the run was started and whatever its cadence. A block counted from the run's own
// first tick would straddle two buckets, spoil both, and double the failures the demo shows.
const (
	CitypulseProfileToken    = "cp-node-profile"
	CitypulseDeviceTypeToken = "cp-node"
	CitypulseAreaTypeToken   = "cp-district"
	CitypulseLampKey         = "lamp_ok"
	CitypulsePM25Key         = "pm25"
	CitypulseParkingKey      = "parking_free_pct"

	citypulseDistrictCount = 4
	citypulseNodesPerDist  = 5
	citypulseNodeCount     = citypulseDistrictCount * citypulseNodesPerDist

	// citypulseBlockSeconds is how long one behaviour block lasts: the bucket width the seeded
	// outcomes are judged on.
	citypulseBlockSeconds = 300
)

// CitypulseDistrictToken is the area token of district n (1-based).
func CitypulseDistrictToken(n int) string { return fmt.Sprintf("cp-district-%d", n) }

type citypulse struct {
	seed  int64
	load  Load
	ticks atomic.Int64
}

// NewCitypulse returns the citypulse Sim seeded from the handshake. Prefer NewSim.
func NewCitypulse(seed int64, load Load) Sim {
	return &citypulse{seed: seed, load: load}
}

func (s *citypulse) Manifest() SimManifest {
	areas := make([]AreaSpec, citypulseDistrictCount)
	for i := range areas {
		areas[i] = AreaSpec{
			Token:         CitypulseDistrictToken(i + 1),
			Name:          fmt.Sprintf("District %d", i+1),
			AreaTypeToken: CitypulseAreaTypeToken,
		}
	}
	return resize(s.load, SimManifest{
		Name:      "citypulse",
		Seed:      s.seed,
		AreaTypes: []AreaTypeSpec{{Token: CitypulseAreaTypeToken, Name: "District"}},
		Areas:     areas,
		Profiles: []ProfileSpec{
			{
				Token:    CitypulseProfileToken,
				Name:     "City Pulse Street Node Profile",
				Category: "sensor",
				Metrics: []MetricSpec{
					{Key: CitypulseLampKey, Name: "Lamp on", DataType: "DOUBLE", Unit: ""},
					{Key: CitypulsePM25Key, Name: "PM2.5", DataType: "DOUBLE", Unit: "ug/m3"},
					{Key: CitypulseParkingKey, Name: "Parking free", DataType: "DOUBLE", Unit: "%"},
				},
			},
		},
		DeviceTypes: []DeviceTypeSpec{
			{Token: CitypulseDeviceTypeToken, Name: "City Pulse Street Node", ProfileToken: CitypulseProfileToken},
		},
		Populations: []PopulationSpec{
			{
				OfType:            CitypulseDeviceTypeToken,
				Count:             citypulseNodeCount,
				TokenPattern:      "cp-node-{n:03d}",
				ExternalIdPattern: "CP-NODE-{n:05d}",
				DistributeAcross:  []string{"area"},
			},
		},
	})
}

func (s *citypulse) Bootstrap(ctx context.Context, rt *Runtime) error {
	return Provision(ctx, rt, s.Manifest())
}

// citypulseReading is one node's three readings at a unix second. district is 0-based, node is
// the node's index within its district.
func citypulseReading(district, node int, unix int64) map[string]float64 {
	block := unix / citypulseBlockSeconds
	// A smooth ripple with a period near a minute, so a bucket's average is stable.
	wave := math.Sin(float64(unix)*0.1 + float64(district)*1.3 + float64(node)*0.4)

	lamp := 1.0
	// Eastside: in every third block, three nodes in five have their lamp out.
	if district == 3 && block%3 == 0 && node%5 < 3 {
		lamp = 0
	}

	pm25 := 9 + 2.5*wave
	// The third district: a pollution episode in one block in six.
	if district == 2 && block%6 == 0 {
		pm25 = 24 + 2*wave
	}

	parking := []float64{36, 30, 24, 17}[district%4] + 4*wave
	// Eastside: parking all but runs out in one block in five.
	if district == 3 && block%5 == 0 {
		parking = 4 + math.Abs(wave)
	}

	return map[string]float64{
		CitypulseLampKey:    lamp,
		CitypulsePM25Key:    pm25,
		CitypulseParkingKey: parking,
	}
}

func (s *citypulse) Tick(ctx context.Context, rt *Runtime) error {
	s.ticks.Add(1)
	if len(rt.Devices) == 0 {
		return nil
	}
	now := time.Now().Unix()
	err := EmitAll(ctx, rt, rt.Load.Workers(len(rt.Devices)),
		func(i int, _ DeviceInstance) map[string]float64 {
			// The device-to-district map is Expand's: device i is in area i modulo the count.
			return citypulseReading(i%citypulseDistrictCount, i/citypulseDistrictCount, now)
		})
	if err != nil {
		log.Error().Err(err).Msg("emit measurements failed")
	}
	return err
}
