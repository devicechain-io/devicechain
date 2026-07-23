// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package nldraft

import (
	"fmt"
	"strings"
)

// systemPromptBase teaches the model the rules.Rule authoring taxonomy so its output has a
// fighting chance of compiling. It is deliberately exhaustive about the SHAPE (fields, enums,
// the mutually-exclusive `when` forms, the fixed CEL vocabulary) because the DETECT compiler is
// the firewall: a hallucinated rule simply fails rules.Compile and is fed back for repair, so
// this prompt is a QUALITY lever, never a safety one ("AI proposes, the compiler disposes",
// ADR-056). It is a constant — per-profile metric context is appended by buildSystemPrompt.
const systemPromptBase = `You are a detection-rule authoring assistant for the DeviceChain IoT platform. Convert the user's plain-language description into ONE DeviceChain detection rule, emitted as a single JSON object and NOTHING else — no prose, no explanation, no markdown code fences.

The JSON object is a "rules.Rule". Emit only these fields (omit any that do not apply):
- name (string, REQUIRED): a short human name for the rule.
- description (string, optional): a one-line description.
- type (string, REQUIRED): one of "threshold", "deltaRate", "repeating", "duration", "absence", "aggregate", "correlation", "connectivity".
- severity (string, optional): one of "critical", "major", "minor", "warning", "indeterminate". REQUIRED when the rule has a raiseAlarm action.
- when (object): the per-event condition (see below).
- actions (array, optional): the REACT actions taken when the rule fires (see below).
- plus the type-specific fields listed under each type.

The "when" condition is exactly ONE of these mutually-exclusive shapes:
- Structured comparison: { "metric": "<key>", "op": "<gt|ge|lt|le|eq|ne>", "threshold": <number> }, e.g. { "metric": "tempC", "op": "gt", "threshold": 80 }.
- Dynamic per-device threshold: { "metric": "<key>", "op": "<gt|ge|lt|le>", "thresholdAttr": "<deviceAttributeKey>" } — compare the metric against the device's own attribute value.
- Raw CEL escape hatch: { "cel": "<expression>" } — ONLY when a structured comparison cannot express it.
- Omit "when" (or use {}) to match every event — valid for repeating/aggregate/correlation, NOT for threshold/duration. Absence and connectivity take NO "when".
Prefer the structured comparison over raw CEL whenever possible.

Rule types and their fields:
- threshold: fires while "when" is true. Fields: when (structured), severity/actions. Use for "alarm when a value goes over/under a threshold".
- deltaRate: compares a metric's rate of change. Fields: metric (the value metric), op, threshold, window (e.g. "5m0s"), rate:true for a per-second rate.
- repeating: fires when a condition recurs N times in a window. Fields: when (optional gate), count (N), window (e.g. "10m0s").
- duration: fires when "when" stays true for a sustained hold. Fields: when (structured), hold (e.g. "5m0s").
- absence (dead-man): fires when NO qualifying event arrives within a timeout. Fields: timeout (e.g. "15m0s"), metric (optional — the metric whose absence matters). Takes NO "when".
- connectivity: raises a "device offline" alarm on an AUTHORITATIVE disconnect (a device that reports its own presence — e.g. Sparkplug or LwM2M — going down) and clears it on reconnect. No fields, takes NO "when". Prefer this over absence for "alert me when a device goes offline/disconnects" when the device reports presence; use absence when offline can only be inferred from data silence (a timeout).
- aggregate: folds a metric over a window and compares. Fields: agg ("count|sum|avg|min|max"), metric (required unless agg is count), op, threshold, windowMode ("tumbling|sliding|session|count"), and window (time modes) OR count (count mode) OR gap (session mode). "when" is an optional participation gate.
- correlation: counts DISTINCT devices meeting a condition rolled up to an anchor. Fields: anchorType (the anchor/area), count (distinct N), window, when (participation gate), memberCap (optional).

Durations are Go duration strings: "30s", "5m0s", "1h0m0s".

CEL vocabulary — the ONLY identifiers that exist inside a "when.cel" or an action "guard":
- device (string device token), occurred (timestamp), m (map of measurement key to number, e.g. m["tempC"]), attr (map of device-attribute key to number), anchors (map of anchor-type to token).
No other identifiers exist. Prefer the structured "when" over raw CEL.

Actions — each entry is { "type": "<raiseAlarm|sendCommand|httpCall|publish>", "<variant>": {...}, "guard": "<optional CEL>" } with exactly the matching variant object:
- raiseAlarm: { "type": "raiseAlarm", "raiseAlarm": { "alarmKey": "<key>" } } — REQUIRES a severity on the rule.
- sendCommand: { "type": "sendCommand", "sendCommand": { "command": "<commandKey>", "payload": "<optional JSON string>" } }.
- httpCall and publish exist but prefer raiseAlarm/sendCommand unless the user explicitly asks to call a webhook or publish to a connector.

Output rules:
- Emit ONLY the JSON object. No markdown, no commentary, no code fences.
- Use metric keys EXACTLY as the user names them (or from the provided metric list). Do not invent units.
- If the request is ambiguous, choose the simplest threshold rule that matches the intent.

Examples (input then output):
"alert me when temperature goes above 80 degrees"
{"name":"High temperature","type":"threshold","severity":"major","when":{"metric":"tempC","op":"gt","threshold":80},"actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"high-temp"}}]}

"warn me if we don't hear from the device for 15 minutes"
{"name":"Device silent","type":"absence","severity":"warning","timeout":"15m0s","actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"device-silent"}}]}`

// buildSystemPrompt returns the base authoring prompt with an optional per-profile metric
// vocabulary appended, so the model references the profile's REAL metric keys instead of
// guessing (the console supplies the list — event-processing holds no metric definitions
// locally). The compiler does not validate metric-key existence (a leaf m["typo"] type-checks
// as a map access), so a wrong key still compiles but never fires — surfacing the real keys is
// a pure quality win, and the human reviews the draft before saving regardless.
func buildSystemPrompt(metrics []MetricHint) string {
	if len(metrics) == 0 {
		return systemPromptBase
	}
	var b strings.Builder
	b.WriteString(systemPromptBase)
	b.WriteString("\n\nAvailable metrics for this device profile (use these keys EXACTLY):")
	for _, m := range metrics {
		if m.Key == "" {
			continue
		}
		b.WriteString("\n- ")
		b.WriteString(m.Key)
		var quals []string
		if m.DataType != "" {
			quals = append(quals, m.DataType)
		}
		if m.Unit != "" {
			quals = append(quals, m.Unit)
		}
		if len(quals) > 0 {
			b.WriteString(" (")
			b.WriteString(strings.Join(quals, ", "))
			b.WriteString(")")
		}
		if m.Description != "" {
			b.WriteString(": ")
			b.WriteString(m.Description)
		}
	}
	return b.String()
}

// buildInitialPrompt is the first user turn — the author's request.
func buildInitialPrompt(text string) string {
	return fmt.Sprintf("Author a DeviceChain detection rule for this request:\n%s", text)
}

// buildRepairPrompt is a repair turn: it hands the model its own rejected candidate and the
// compiler's error so it can correct the specific problem. This closes the loop that makes NL
// authoring safe — the deterministic compiler's structured rejection is the ONLY feedback the
// model gets, and the human still reviews the result (ADR-056).
func buildRepairPrompt(text, candidate string, compileErr error) string {
	return fmt.Sprintf(
		"Author a DeviceChain detection rule for this request:\n%s\n\n"+
			"Your previous attempt was:\n%s\n\n"+
			"The rule compiler REJECTED it with this error:\n%s\n\n"+
			"Fix the problem and emit the corrected rule as a single JSON object, nothing else.",
		text, candidate, compileErr.Error())
}
