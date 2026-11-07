// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package arena

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ExportRecording writes the episode back out as a board recording (formatVersion 2), so
// the board-replay player that plays the tenant's history can play the agent driving it.
//
// The document is the source recording with three things replaced, and everything else
// (devices, board, anchors, declared metrics) carried through untouched:
//
//   - measurements: for a commanded zone, the temperature, setpoint and damper after T0
//     are the plant's reports, not the recorded ones; the run ends at the episode's end.
//   - alarms: the history is the arena rule's, folded the way the player expects (one
//     snapshot, then events), so the alarm table shows what the agent's run raised.
//   - chapters: one per episode start, per action (applied, refused or rejected) and the
//     end, which is how the agent's actions appear as annotations on the timeline.
func (e *Episode) ExportRecording() ([]byte, error) {
	if e.taken == nil {
		return nil, fmt.Errorf("the episode has not been graded")
	}
	rec := e.sc.rec
	raw, err := json.Marshal(rec.raw)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}

	endMs := int64(math.Round(e.end * 1000))
	t0Ms := int64(math.Round(e.spec.T0Sec * 1000))

	names, err := stringList(doc, "channels", "measurements")
	if err != nil {
		return nil, err
	}
	nameIdx := map[string]int{}
	for i, n := range names {
		nameIdx[n] = i
	}
	devIdx := map[string]int{}
	for i, d := range rec.Devices {
		devIdx[d.Token] = i
	}
	plantMetrics := map[int]bool{}
	for _, m := range []string{metricTemp, metricSetpoint, metricDamper} {
		i, ok := nameIdx[m]
		if !ok {
			return nil, fmt.Errorf("the recording does not declare measurement %q the plant writes", m)
		}
		plantMetrics[i] = true
	}

	type row struct {
		d, n int
		t    int64
		v    json.Number
		s    json.Number
	}
	m, _ := doc["measurements"].(map[string]any)
	cols := map[string][]any{}
	for _, k := range []string{"d", "n", "t", "v", "s"} {
		cols[k], _ = m[k].([]any)
	}
	var rows []row
	zoneIdx := map[int]bool{}
	for tok := range e.eng.zones {
		zoneIdx[devIdx[tok]] = true
	}
	for i := range cols["t"] {
		d, _ := asInt(cols["d"][i])
		n, _ := asInt(cols["n"][i])
		t, _ := asInt64(cols["t"][i])
		if t > endMs {
			continue
		}
		if zoneIdx[d] && plantMetrics[n] && t > t0Ms {
			continue // replaced by what the plant reported
		}
		v, _ := cols["v"][i].(json.Number)
		s := json.Number("0")
		if len(cols["s"]) > i {
			if sv, ok := cols["s"][i].(json.Number); ok {
				s = sv
			}
		}
		rows = append(rows, row{d: d, n: n, t: t, v: v, s: s})
	}
	toks := make([]string, 0, len(e.eng.zones))
	for tok := range e.eng.zones {
		toks = append(toks, tok)
	}
	sort.Strings(toks)
	for _, tok := range toks {
		for _, tk := range e.eng.zones[tok].ticks {
			ms := int64(math.Round(tk.T * 1000))
			if ms > endMs {
				continue
			}
			for _, kv := range []struct {
				name string
				v    float64
			}{{metricTemp, tk.Temp}, {metricSetpoint, tk.Setpoint}, {metricDamper, round(tk.Damper)}} {
				rows = append(rows, row{d: devIdx[tok], n: nameIdx[kv.name], t: ms, v: json.Number(fmt.Sprint(kv.v)), s: "0"})
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].t < rows[j].t })
	out := map[string][]any{"d": {}, "n": {}, "t": {}, "v": {}, "s": {}}
	for _, r := range rows {
		out["d"] = append(out["d"], r.d)
		out["n"] = append(out["n"], r.n)
		out["t"] = append(out["t"], r.t)
		out["v"] = append(out["v"], r.v)
		out["s"] = append(out["s"], r.s)
	}
	doc["measurements"] = out

	// Alarms: the arena rule's history, folded.
	type event struct {
		ms int64
		m  map[string]any
	}
	var events []event
	for _, a := range e.eng.alarms {
		base := func(state string, at float64) map[string]any {
			return map[string]any{
				"id": fmt.Sprintf("alarm-%d", a.ID), "dev": a.Device, "key": a.Key, "metric": a.Metric,
				"state": state, "sev": a.Severity, "occ": instantString(rec, at), "val": json.Number(fmt.Sprint(a.Value)),
				"tMs": int64(math.Round(at * 1000)),
			}
		}
		events = append(events, event{int64(math.Round(a.RaisedAt * 1000)), base("ACTIVE", a.RaisedAt)})
		if !a.open() {
			events = append(events, event{int64(math.Round(a.ClearedAt * 1000)), base("CLEARED", a.ClearedAt)})
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].ms < events[j].ms })
	evs := make([]any, 0, len(events))
	for _, ev := range events {
		if ev.ms <= endMs {
			evs = append(evs, ev.m)
		}
	}
	doc["alarms"] = map[string]any{
		"snapshots": []any{map[string]any{"tMs": int64(0), "requestedAtMs": int64(0), "total": 0, "alarms": []any{}}},
		"events":    evs,
	}
	if ch, ok := doc["channels"].(map[string]any); ok {
		ch["alarms"] = true
	}

	// Chapters: the agent's actions as annotations.
	type chapter struct {
		ms         int64
		name, note string
	}
	chs := []chapter{{t0Ms, "episode start", fmt.Sprintf("%s: %s. Agent %q takes over.", e.Scenario, e.spec.Title, e.Agent)}}
	for _, a := range e.actions {
		params, _ := json.Marshal(a.Params)
		chs = append(chs, chapter{int64(math.Round(a.T * 1000)),
			fmt.Sprintf("agent: %s %s", a.Command, strings.TrimPrefix(a.Device, "tp-")),
			fmt.Sprintf("%s %s -> %s. %s", a.Device, params, a.Outcome, a.Detail)})
	}
	card := e.taken
	chs = append(chs, chapter{endMs, "scorecard", fmt.Sprintf("score %.3f; %.1f alarm-minutes avoided; %d command(s); %d unsafe attempt(s)",
		card.Score, card.AlarmMinutesAvoided, card.CommandsSpent, card.UnsafeAttempts)})
	sort.SliceStable(chs, func(i, j int) bool { return chs[i].ms < chs[j].ms })
	chapters := make([]any, 0, len(chs))
	for _, c := range chs {
		chapters = append(chapters, map[string]any{"tMs": c.ms, "name": c.name, "note": c.note})
	}
	doc["chapters"] = chapters

	doc["durationMs"] = endMs
	doc["runId"] = "arena-" + e.ID
	doc["converter"] = map[string]any{"version": 1, "name": "proving-ground"}
	if sh, ok := doc["sourceHashes"].(map[string]any); ok {
		sh["arena-actions.json"] = card.Fingerprint
	}
	delete(doc, "excerpt")
	return json.MarshalIndent(doc, "", " ")
}

func instantString(r *Recording, t float64) string {
	return r.Instant(t).Format("2006-01-02T15:04:05.0000000Z")
}

func stringList(doc map[string]any, path ...string) ([]string, error) {
	var cur any = doc
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("recording has no %s", strings.Join(path, "."))
		}
		cur = m[k]
	}
	list, ok := cur.([]any)
	if !ok {
		return nil, fmt.Errorf("recording %s is not a list", strings.Join(path, "."))
	}
	out := make([]string, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("recording %s[%d] is not a string", strings.Join(path, "."), i)
		}
		out[i] = s
	}
	return out, nil
}

func asInt64(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil
}

func asInt(v any) (int, bool) {
	i, ok := asInt64(v)
	return int(i), ok
}
