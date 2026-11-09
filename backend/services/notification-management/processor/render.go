// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-notification-management/model"
)

// RenderedNotification is the channel-independent rendering of one alarm transition
// that the dispatcher hands to a channel adapter (ADR-017). An adapter takes the
// representation that fits its transport: SMTP uses Subject + TextBody; a webhook
// (Slack included) serializes Payload as JSON. Rendering once, here, keeps the
// per-alarm formatting in a single tested place rather than duplicated per adapter.
//
// v1 uses a built-in default rendering — there is no per-channel custom template yet
// (deferred to a later slice, so there is no template-injection surface to secure
// now). Payload always carries a human "text" summary so a Slack incoming webhook
// renders something legible without any Slack-specific formatting.
type RenderedNotification struct {
	// Subject is the one-line summary (email subject / short header).
	Subject string
	// TextBody is the multi-line plaintext body (email).
	TextBody string
	// Payload is the structured JSON body for a webhook. "text" mirrors Subject for
	// Slack compatibility; the remaining keys are the alarm's structured fields.
	Payload map[string]any
	// Alarm is the same transition as typed facts, for the native adapters (Slack, Teams,
	// PagerDuty) that build a vendor-shaped message and must not re-parse Payload. It is
	// the zero value on a rendering that did not come from an alarm.
	Alarm AlarmFacts
}

// AlarmFacts is the typed view of one alarm transition. EventType is the alarm event
// stream's name for it (RAISED, ESCALATED, DEESCALATED, CLEARED, ACKNOWLEDGED) or
// RENOTIFIED for a timed re-page of an unchanged alarm.
type AlarmFacts struct {
	EventType        string
	AlarmToken       string
	AlarmKey         string
	Severity         string
	PreviousSeverity string
	State            string
	Originator       string
	// DeviceToken, DeviceName and DeviceTypeToken describe the originating device when the
	// alarm event carried them (a producer that predates them, or an originator that is not a
	// device, leaves them empty). Empty means unknown, never "no such device".
	DeviceToken     string
	DeviceName      string
	DeviceTypeToken string
	MetricKey       string
	Value           *float64
	Acknowledged    bool
	Escalation      int
	RaisedTime      time.Time
	OccurredTime    time.Time
}

// renderNotification builds the default rendering of an alarm state-change event.
// eventTime is the transition's occurred time already resolved to the event.
func renderNotification(event *dmmodel.AlarmStateChangeEvent) *RenderedNotification {
	verb := verbForEvent(event.EventType)
	originator := fmt.Sprintf("%s %d", event.OriginatorType, event.OriginatorId)
	// A person reads the device's name, or failing that its token, in preference to a numeric
	// id; the id stays in the structured payload for anything that wants it.
	shown := originator
	switch {
	case event.DeviceName != "":
		shown = oneLine(event.DeviceName)
	case event.DeviceToken != "":
		shown = oneLine(event.DeviceToken)
	}
	subject := fmt.Sprintf("[%s] Alarm %s: %s on %s", event.Severity, verb, event.AlarmKey, shown)

	var b strings.Builder
	fmt.Fprintf(&b, "Alarm %s %s on %s.\n\n", event.AlarmKey, verb, shown)
	if event.DeviceName != "" {
		writeField(&b, "Device", oneLine(event.DeviceName))
	}
	if event.DeviceToken != "" {
		writeField(&b, "Device token", oneLine(event.DeviceToken))
	}
	if event.DeviceTypeToken != "" {
		writeField(&b, "Device type", oneLine(event.DeviceTypeToken))
	}
	writeField(&b, "Severity", event.Severity)
	if event.PreviousSeverity != "" {
		writeField(&b, "Previous severity", event.PreviousSeverity)
	}
	writeField(&b, "State", event.State)
	if event.MetricKey != "" {
		writeField(&b, "Metric", event.MetricKey)
	}
	if event.LastValue != nil {
		writeField(&b, "Value", fmt.Sprintf("%g", *event.LastValue))
	}
	writeField(&b, "Acknowledged", yesNo(event.Acknowledged))
	writeField(&b, "Raised", formatTime(event.RaisedTime))
	writeField(&b, "Occurred", formatTime(event.OccurredTime))

	payload := map[string]any{
		"text":           subject,
		"eventType":      event.EventType.String(),
		"alarmToken":     event.AlarmToken,
		"alarmKey":       event.AlarmKey,
		"originatorType": event.OriginatorType,
		"originatorId":   event.OriginatorId,
		"severity":       event.Severity,
		"state":          event.State,
		"acknowledged":   event.Acknowledged,
		"raisedTime":     formatTime(event.RaisedTime),
		"occurredTime":   formatTime(event.OccurredTime),
	}
	// Named only when the event named them: a missing name is omitted, never invented.
	if event.DeviceToken != "" {
		payload["deviceToken"] = event.DeviceToken
	}
	if event.DeviceName != "" {
		payload["deviceName"] = event.DeviceName
	}
	if event.DeviceTypeToken != "" {
		payload["deviceTypeToken"] = event.DeviceTypeToken
	}
	if event.MetricKey != "" {
		payload["metricKey"] = event.MetricKey
	}
	if event.PreviousSeverity != "" {
		payload["previousSeverity"] = event.PreviousSeverity
	}
	if event.LastValue != nil {
		payload["value"] = *event.LastValue
	}

	facts := AlarmFacts{
		EventType: event.EventType.String(), AlarmToken: event.AlarmToken, AlarmKey: event.AlarmKey,
		Severity: event.Severity, PreviousSeverity: event.PreviousSeverity, State: event.State,
		Originator: shown, DeviceToken: event.DeviceToken, DeviceName: event.DeviceName,
		DeviceTypeToken: event.DeviceTypeToken, MetricKey: event.MetricKey, Value: event.LastValue,
		Acknowledged: event.Acknowledged, RaisedTime: event.RaisedTime, OccurredTime: event.OccurredTime,
	}
	return &RenderedNotification{Subject: subject, TextBody: b.String(), Payload: payload, Alarm: facts}
}

// renderEscalation builds the re-notification message for an open alarm that the
// escalation scheduler (N.D) re-pages. It is rendered from the persisted
// NotificationState rather than the original alarm event (which the scheduler does not
// have), so it carries the durable identity + progress fields — enough for a human to
// recognize the still-open alarm and act. level is the escalation number this delivery
// represents (1 = first re-notification). Payload mirrors the event rendering's shape
// (a "text" summary plus structured keys) so a Slack/webhook endpoint renders it the
// same way.
func renderEscalation(state *model.NotificationState, level int) *RenderedNotification {
	subject := fmt.Sprintf("[%s] Alarm still unresolved (escalation %d): %s",
		state.Severity, level, state.AlarmKey)

	var b strings.Builder
	fmt.Fprintf(&b, "Alarm %s is still unacknowledged and uncleared after %d notification(s).\n\n",
		state.AlarmKey, state.NotifyCount)
	writeField(&b, "Severity", state.Severity)
	writeField(&b, "Escalation", fmt.Sprintf("%d", level))
	writeField(&b, "Notifications", fmt.Sprintf("%d", state.NotifyCount))
	if state.FirstNotifiedAt.Valid {
		writeField(&b, "First notified", formatTime(state.FirstNotifiedAt.Time))
	}
	if state.LastNotifiedAt.Valid {
		writeField(&b, "Last notified", formatTime(state.LastNotifiedAt.Time))
	}

	payload := map[string]any{
		// RENOTIFIED, not ESCALATED: this is a timed re-notification of an unchanged
		// alarm. The alarm event stream's ESCALATED means the alarm's SEVERITY worsened
		// (renderNotification emits that), so a webhook keying on eventType must be able
		// to tell a reminder from a real severity change.
		"text":         subject,
		"eventType":    "RENOTIFIED",
		"alarmToken":   state.AlarmToken,
		"alarmKey":     state.AlarmKey,
		"severity":     state.Severity,
		"escalation":   level,
		"notifyCount":  state.NotifyCount,
		"acknowledged": false,
	}
	if state.FirstNotifiedAt.Valid {
		payload["firstNotifiedTime"] = formatTime(state.FirstNotifiedAt.Time)
	}
	if state.LastNotifiedAt.Valid {
		payload["lastNotifiedTime"] = formatTime(state.LastNotifiedAt.Time)
	}

	facts := AlarmFacts{
		EventType: "RENOTIFIED", AlarmToken: state.AlarmToken, AlarmKey: state.AlarmKey,
		Severity: state.Severity, State: "ACTIVE", Escalation: level,
	}
	if state.FirstNotifiedAt.Valid {
		facts.RaisedTime = state.FirstNotifiedAt.Time
	}
	if state.LastNotifiedAt.Valid {
		facts.OccurredTime = state.LastNotifiedAt.Time
	}
	return &RenderedNotification{Subject: subject, TextBody: b.String(), Payload: payload, Alarm: facts}
}

// oneLine makes a tenant-authored name safe for the one-line summary: control characters
// (a device name may hold a line break) become spaces, because the summary is also the email
// subject and a header with a line break is refused outright, which would fail every email
// about that device. It is capped so a long name cannot swamp the line. The structured payload
// keeps the name exactly as stored.
func oneLine(s string) string {
	out := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	out = strings.Join(strings.Fields(out), " ")
	if r := []rune(out); len(r) > 120 {
		out = string(r[:119]) + "…"
	}
	return out
}

// verbForEvent renders an event type as a human past-tense verb for the summary.
func verbForEvent(t dmmodel.AlarmEventType) string {
	switch t {
	case dmmodel.AlarmEventRaised:
		return "raised"
	case dmmodel.AlarmEventEscalated:
		return "escalated"
	case dmmodel.AlarmEventDeescalated:
		return "de-escalated"
	case dmmodel.AlarmEventCleared:
		return "cleared"
	case dmmodel.AlarmEventAcknowledged:
		return "acknowledged"
	default:
		return strings.ToLower(t.String())
	}
}

// writeField appends an aligned "Label: value" line to the plaintext body.
func writeField(b *strings.Builder, label, value string) {
	fmt.Fprintf(b, "%-18s %s\n", label+":", value)
}

// formatTime renders a timestamp in RFC 3339 (UTC), or a placeholder when zero.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
