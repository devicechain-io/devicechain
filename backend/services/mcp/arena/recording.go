// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package arena is the Proving Ground: a gym-style, turn-based environment built from a
// tenant's recorded history, for grading an AI operator before it is allowed near a real
// actuator.
//
// EXPERIMENTAL. Nothing here is reachable unless the mcp service is configured with
// experimentalArena.enabled, and nothing here can touch live state: the arena owns no
// GraphQL client, holds no token, and dispatches nothing. An agent's reads are served
// from a recording and its commands go to a simulated plant, so the confused-deputy line
// the rest of the service draws holds trivially.
package arena

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

// maxRecordingBytes bounds a recording read from disk. The committed showcase recordings
// are a few hundred KiB; this is a fixture format, not a database.
const maxRecordingBytes = 32 << 20

const recordingKind = "sitepulse-board-recording"

// Point is one reading: seconds from the recording's start, and the value.
type Point struct{ T, V float64 }

// Device is one recorded device.
type Device struct{ ID, Token, Kind string }

// Recording is a board recording (the dc-simulator format the board-replay player reads),
// parsed for the arena. Parsing is strict in the way the player's is: a document the arena
// cannot honour is refused rather than half-read, because an environment that quietly
// drops rows grades an agent against a history that never happened.
type Recording struct {
	Tenant      string
	Instance    string
	StartedAt   time.Time
	DurationSec float64
	Devices     []Device
	Sha256      string

	byToken map[string]int
	series  map[string]map[string][]Point // device token -> measurement -> points, time-ordered
	raw     map[string]any                // the whole document, kept so an episode can be written back out
}

type recordingDoc struct {
	Kind          string `json:"kind"`
	FormatVersion int    `json:"formatVersion"`
	StartedAtUtc  string `json:"startedAtUtc"`
	DurationMs    int64  `json:"durationMs"`
	Instance      string `json:"instance"`
	Tenant        string `json:"tenant"`
	Devices       []struct {
		ID    string `json:"id"`
		Token string `json:"token"`
		Kind  string `json:"kind"`
	} `json:"devices"`
	Channels struct {
		Measurements []string `json:"measurements"`
	} `json:"channels"`
	Measurements struct {
		D []int     `json:"d"`
		N []int     `json:"n"`
		T []int64   `json:"t"`
		V []float64 `json:"v"`
	} `json:"measurements"`
}

// ParseRecording reads a board recording. It refuses a document of the wrong kind or an
// unsupported version, ragged columns, an index out of range and a time axis that goes
// backwards.
func ParseRecording(data []byte) (*Recording, error) {
	if len(data) > maxRecordingBytes {
		return nil, fmt.Errorf("recording is %d bytes; the arena reads at most %d", len(data), maxRecordingBytes)
	}
	var doc recordingDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("recording is not valid JSON: %w", err)
	}
	if doc.Kind != recordingKind {
		return nil, fmt.Errorf("recording kind is %q, want %q", doc.Kind, recordingKind)
	}
	if doc.FormatVersion != 1 && doc.FormatVersion != 2 {
		return nil, fmt.Errorf("recording formatVersion %d is not one the arena reads (1, 2)", doc.FormatVersion)
	}
	started, err := time.Parse(time.RFC3339Nano, doc.StartedAtUtc)
	if err != nil {
		return nil, fmt.Errorf("recording startedAtUtc: %w", err)
	}
	if doc.DurationMs <= 0 {
		return nil, fmt.Errorf("recording durationMs must be positive")
	}
	m := doc.Measurements
	n := len(m.T)
	if len(m.D) != n || len(m.N) != n || len(m.V) != n {
		return nil, fmt.Errorf("recording measurement columns are ragged (d=%d n=%d t=%d v=%d)", len(m.D), len(m.N), n, len(m.V))
	}
	r := &Recording{
		Tenant: doc.Tenant, Instance: doc.Instance, StartedAt: started.UTC(),
		DurationSec: float64(doc.DurationMs) / 1000,
		byToken:     map[string]int{},
		series:      map[string]map[string][]Point{},
	}
	for i, d := range doc.Devices {
		if d.Token == "" {
			return nil, fmt.Errorf("recording device %d has no token", i)
		}
		if _, dup := r.byToken[d.Token]; dup {
			return nil, fmt.Errorf("recording device token %q appears twice", d.Token)
		}
		r.byToken[d.Token] = i
		r.Devices = append(r.Devices, Device{ID: d.ID, Token: d.Token, Kind: d.Kind})
		r.series[d.Token] = map[string][]Point{}
	}
	names := doc.Channels.Measurements
	var last int64 = -1
	for i := 0; i < n; i++ {
		if m.D[i] < 0 || m.D[i] >= len(r.Devices) {
			return nil, fmt.Errorf("recording row %d names device %d of %d", i, m.D[i], len(r.Devices))
		}
		if m.N[i] < 0 || m.N[i] >= len(names) {
			return nil, fmt.Errorf("recording row %d names measurement %d of %d", i, m.N[i], len(names))
		}
		if m.T[i] < last {
			return nil, fmt.Errorf("recording row %d goes back in time (%d ms after %d ms)", i, m.T[i], last)
		}
		if m.T[i] < 0 || m.T[i] > doc.DurationMs {
			return nil, fmt.Errorf("recording row %d is outside the run (%d ms)", i, m.T[i])
		}
		if math.IsNaN(m.V[i]) || math.IsInf(m.V[i], 0) {
			return nil, fmt.Errorf("recording row %d holds a non-finite value", i)
		}
		last = m.T[i]
		tok := r.Devices[m.D[i]].Token
		r.series[tok][names[m.N[i]]] = append(r.series[tok][names[m.N[i]]], Point{T: float64(m.T[i]) / 1000, V: m.V[i]})
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&r.raw); err != nil {
		return nil, fmt.Errorf("recording is not valid JSON: %w", err)
	}
	sum := sha256.Sum256(data)
	r.Sha256 = hex.EncodeToString(sum[:])
	return r, nil
}

// Device returns a recorded device by token.
func (r *Recording) Device(token string) (Device, bool) {
	i, ok := r.byToken[token]
	if !ok {
		return Device{}, false
	}
	return r.Devices[i], true
}

// Series is a device's recorded points for one measurement, time-ordered. Callers must
// not modify it.
func (r *Recording) Series(token, metric string) []Point { return r.series[token][metric] }

// Metrics lists the measurements a device has any recorded reading of, sorted.
func (r *Recording) Metrics(token string) []string {
	out := make([]string, 0, len(r.series[token]))
	for m := range r.series[token] {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// At is the latest recorded value at or before t. It is the only read an observation is
// built from, which is what makes an observation strictly causal: there is no lookup here
// that can return a point later than t.
func (r *Recording) At(token, metric string, t float64) (Point, bool) {
	s := r.series[token][metric]
	i := sort.Search(len(s), func(i int) bool { return s[i].T > t })
	if i == 0 {
		return Point{}, false
	}
	return s[i-1], true
}

// Interp is the recorded value at t, linearly interpolated between the points around it
// and held flat outside the recorded span. It feeds the plant, whose disturbance is the
// recorded trajectory; it is never shown to an agent (see Episode).
func (r *Recording) Interp(token, metric string, t float64) (float64, bool) {
	s := r.series[token][metric]
	if len(s) == 0 {
		return 0, false
	}
	i := sort.Search(len(s), func(i int) bool { return s[i].T > t })
	switch {
	case i == 0:
		return s[0].V, true
	case i == len(s):
		return s[len(s)-1].V, true
	}
	a, b := s[i-1], s[i]
	if b.T == a.T {
		return b.V, true
	}
	return a.V + (b.V-a.V)*(t-a.T)/(b.T-a.T), true
}

// Instant is the wall-clock moment t seconds into the recording.
func (r *Recording) Instant(t float64) time.Time {
	return r.StartedAt.Add(time.Duration(t * float64(time.Second)))
}
