// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ruletest

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// maxPlaybackRows bounds one file, so a mistaken path to a large export fails fast.
const maxPlaybackRows = 4 * MaxEvents

// row is one playback record before it is bound to a device and metric.
type row struct {
	at     string
	value  float64
	device string
	metric string
}

// playback reads a stream's recorded readings. Rows may be in any order; delivery order is decided
// by the scenario's faults and the readings' own instants, like every other behaviour.
func (st Stream) playback(s *Scenario, device string, read FileReader) ([]sample, error) {
	if read == nil {
		return nil, errors.New("playback needs a file reader")
	}
	name := st.Playback.File
	data, err := read(name)
	if err != nil {
		return nil, fmt.Errorf("playback %s: %w", name, err)
	}
	var rows []row
	switch strings.ToLower(extOf(name)) {
	case ".csv":
		rows, err = parseCSV(data)
	case ".ndjson", ".jsonl":
		rows, err = parseNDJSON(data)
	default:
		return nil, fmt.Errorf("playback %s: the file must end in .csv, .ndjson or .jsonl", name)
	}
	if err != nil {
		return nil, fmt.Errorf("playback %s: %w", name, err)
	}
	end := s.Start.Add(s.Duration.D())
	var out []sample
	for i, r := range rows {
		at, err := playbackInstant(s.Start, r.at)
		if err != nil {
			return nil, fmt.Errorf("playback %s row %d: %w", name, i+1, err)
		}
		if at.Before(s.Start) || at.After(end) {
			return nil, fmt.Errorf("playback %s row %d: %s is outside the scenario (%s to %s)", name, i+1,
				at.UTC().Format(time.RFC3339), s.Start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
		}
		dev := device
		if r.device != "" {
			dev = r.device
		}
		if r.device != "" && r.device != device {
			// A row for another device would silently attribute readings to a device that did not
			// declare the stream; require the file to be listed under the device it feeds.
			return nil, fmt.Errorf("playback %s row %d: names device %q but is played on device %q", name, i+1, r.device, device)
		}
		metric := st.Metric
		if r.metric != "" {
			metric = r.metric
		}
		if metric == "" {
			return nil, fmt.Errorf("playback %s row %d: no metric (name one on the stream or in the row)", name, i+1)
		}
		out = append(out, sample{device: dev, metric: metric, at: at, value: r.value})
	}
	return out, nil
}

func extOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

func playbackInstant(start time.Time, v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, nil
	}
	if d, err := ParseDuration(v); err == nil {
		return start.Add(d), nil
	}
	return time.Time{}, fmt.Errorf("at %q is neither an RFC 3339 instant nor an offset like 90m", v)
}

func finite(f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return errors.New("value is not a finite number")
	}
	return nil
}

func parseCSV(data []byte) ([]row, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	head, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("the file is empty; a CSV needs a header row with at and value columns")
	}
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range head {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, string(rune(0xFEFF)))))
		switch h {
		case "at", "value", "device", "metric":
			if _, dup := col[h]; dup {
				return nil, fmt.Errorf("the header names %q twice", h)
			}
			col[h] = i
		default:
			return nil, fmt.Errorf("the header has an unknown column %q (columns are at, value, device, metric)", h)
		}
	}
	for _, need := range []string{"at", "value"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("the header has no %q column", need)
		}
	}
	var rows []row
	for line := 2; ; line++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rows) >= maxPlaybackRows {
			return nil, fmt.Errorf("more than %d rows", maxPlaybackRows)
		}
		get := func(name string) string {
			if i, ok := col[name]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		v, err := strconv.ParseFloat(get("value"), 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: value %q is not a number", line, get("value"))
		}
		if err := finite(v); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		rows = append(rows, row{at: get("at"), value: v, device: get("device"), metric: get("metric")})
	}
	return rows, nil
}

func parseNDJSON(data []byte) ([]row, error) {
	type rec struct {
		At     string   `json:"at"`
		Value  *float64 `json:"value"`
		Device string   `json:"device"`
		Metric string   `json:"metric"`
	}
	var rows []row
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for line := 1; sc.Scan(); line++ {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		if len(rows) >= maxPlaybackRows {
			return nil, fmt.Errorf("more than %d rows", maxPlaybackRows)
		}
		var r rec
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if r.At == "" || r.Value == nil {
			return nil, fmt.Errorf("line %d: a row needs at and value", line)
		}
		if err := finite(*r.Value); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		rows = append(rows, row{at: r.At, value: *r.Value, device: r.Device, metric: r.Metric})
	}
	return rows, sc.Err()
}
