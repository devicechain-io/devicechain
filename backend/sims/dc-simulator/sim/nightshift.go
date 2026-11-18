// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import "fmt"

const (
	EpisodeClassic      = "classic"
	EpisodeNightShift   = "night-shift"
	NightShiftIncident  = "tp-vav-tower-f1-south"
	NightShiftNormal    = "tp-vav-tower-f3-north"
	NightShiftControl   = "tp-vav-tower-f5-west"
	NightShiftBystander = "tp-vav-tower-f2-east"
)

// ValidateEpisode fails closed before any provisioning or device activity.
func ValidateEpisode(manifest, episode string) error {
	if episode == "" {
		return nil
	}
	if manifest != "towerpulse" {
		return fmt.Errorf("episode %q requires manifest towerpulse", episode)
	}
	if episode != EpisodeClassic && episode != EpisodeNightShift {
		return fmt.Errorf("unknown towerpulse episode %q", episode)
	}
	return nil
}

// NewSimEpisode keeps the legacy constructor's default behavior and validates the handshake setting.
func NewSimEpisode(manifest, episode string, seed int64, load Load) (Sim, error) {
	if err := ValidateEpisode(manifest, episode); err != nil {
		return nil, err
	}
	s, err := NewSim(manifest, seed, load)
	if err != nil {
		return nil, err
	}
	if tp, ok := s.(*towerpulse); ok {
		tp.episode = episode
	}
	return s, nil
}

func (m *towerpulseModel) initNightShift() {
	m.episode, m.script, m.dropAt = EpisodeNightShift, false, -1
	for _, z := range m.zones {
		z.temp, z.setpoint, z.extraGain, z.people = 22.8, 22.5, 0, 0
	}
	m.applyNightShift(0)
}

// Episode-owned heat sources exchange heat with each room. The conductance is a
// gain in C/s per degree of difference; the controller and command physics remain
// the classic model's. A boost therefore removes real heat from these rooms.
const (
	nightShiftCoolTarget      = 22.8
	nightShiftIncidentTarget  = 25.8
	nightShiftWarmupTarget    = 26.25
	nightShiftMeetingTarget   = 27.5
	nightShiftHeatConductance = 0.01
)

func (m *towerpulseModel) applyNightShift(t float64) {
	for _, z := range m.zones {
		target, conductance := nightShiftCoolTarget, nightShiftHeatConductance
		z.setpoint = 22.5
		z.people = 0
		if t >= 600 {
			z.people = 3
		}
		switch z.token {
		case NightShiftIncident:
			if t >= 45*60 && t < 95*60 {
				target, z.setpoint = nightShiftIncidentTarget, 24
			}
		case NightShiftNormal:
			if t >= 120*60 && t < 126*60 {
				target = nightShiftWarmupTarget
			}
		case NightShiftControl:
			if t >= 110*60 && t < 150*60 {
				target, z.people = nightShiftMeetingTarget, 14
			}
		}
		equilibriumCooling := towerpulseCoolCap * clamp(towerpulseControlBase+towerpulseControlGain*(target-z.setpoint), towerpulseDamperFloor, 100) / 100
		z.extraGain = equilibriumCooling + conductance*(target-z.temp) - z.gain
	}
}

// SlidingAverage is the shared emitted-sample oracle used by the classic script
// tests and the capture generator. Its trailing interval is exactly (t-W, t].
// Call Add only for events admitted by the rule's gate.
type SlidingAverage struct{ points []averagePoint }
type averagePoint struct{ t, v float64 }

func (a *SlidingAverage) Add(t, value, window float64) float64 {
	if window <= 0 {
		panic("sliding average requires a positive window")
	}
	a.points = append(a.points, averagePoint{t, value})
	for len(a.points) > 0 && a.points[0].t <= t-window {
		a.points = a.points[1:]
	}
	sum := 0.0
	for _, p := range a.points {
		sum += p.v
	}
	return sum / float64(len(a.points))
}
