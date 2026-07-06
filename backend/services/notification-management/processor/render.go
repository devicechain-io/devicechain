// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"strings"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
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
}

// renderNotification builds the default rendering of an alarm state-change event.
// eventTime is the transition's occurred time already resolved to the event.
func renderNotification(event *dmmodel.AlarmStateChangeEvent) *RenderedNotification {
	verb := verbForEvent(event.EventType)
	originator := fmt.Sprintf("%s %d", event.OriginatorType, event.OriginatorId)
	subject := fmt.Sprintf("[%s] Alarm %s: %s on %s", event.Severity, verb, event.AlarmKey, originator)

	var b strings.Builder
	fmt.Fprintf(&b, "Alarm %s %s on %s.\n\n", event.AlarmKey, verb, originator)
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
	if event.Message != nil && *event.Message != "" {
		writeField(&b, "Message", *event.Message)
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
	if event.MetricKey != "" {
		payload["metricKey"] = event.MetricKey
	}
	if event.PreviousSeverity != "" {
		payload["previousSeverity"] = event.PreviousSeverity
	}
	if event.LastValue != nil {
		payload["value"] = *event.LastValue
	}
	if event.Message != nil && *event.Message != "" {
		payload["message"] = *event.Message
	}

	return &RenderedNotification{Subject: subject, TextBody: b.String(), Payload: payload}
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
