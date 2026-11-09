// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/httpsink"
	"github.com/devicechain-io/dc-notification-management/model"
)

// This file holds what the native channel adapters (Slack, Teams, PagerDuty) share. Each of
// them delivers through httpsink.Send over the same egress-guarded client the webhook uses,
// so the destination boundary, the no-redirect policy and the per-tenant egress allowances
// apply to them exactly as they do to a webhook; none of them dials anything itself.

// alarmLinkPath is the console page a message's "open" link points at: the alarm list. When
// the alarm event also names its device (its token), the message carries a second link to that
// device's page; see deviceLink.
const alarmLinkPath = "/alarms"

// ackHint tells a human how to stop the escalation; it is shown only while the alarm is
// still open and unacknowledged.
const ackHint = "Acknowledge this alarm in the DeviceChain console to stop further notifications."

// redactedURLError is an error whose text has had a secret URL removed. The webhook URL of
// a Slack or Teams channel IS its credential, and net/http puts the request URL in a
// transport error's text; that text reaches logs. Unwrap keeps the cause visible to
// errors.Is, so the egress and auth classifications the dispatcher keys on still work.
type redactedURLError struct {
	msg string
	err error
}

func (e *redactedURLError) Error() string { return e.msg }
func (e *redactedURLError) Unwrap() error { return e.err }

// scrubSecret returns err with every occurrence of secret replaced in its text, preserving
// the error chain. A nil err or empty secret passes through.
func scrubSecret(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, secret) {
		return err
	}
	return &redactedURLError{msg: strings.ReplaceAll(msg, secret, "<redacted>"), err: err}
}

// postJSON sends body as JSON to target with no credential header (the credential is in
// the URL or the body), through httpsink.Send. secretInURL says that target is itself the
// credential, so it is scrubbed from any error.
func postJSON(ctx context.Context, client *http.Client, target string, secretInURL bool,
	headers map[string]string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	err = httpsink.Send(ctx, client, httpsink.Request{
		URL:     target,
		Headers: headers,
		Body:    raw,
		Auth:    httpsink.Auth{Mode: httpsink.AuthNone},
		// Every native channel carries its credential in the URL or the body, so a
		// reflected response body could carry it too: never put the body in an error.
		SuppressBody: true,
	})
	if secretInURL {
		err = scrubSecret(err, target)
	}
	return err
}

// requireSecret is the delivery-time half of the "a native channel always has a credential"
// rule. The wrapped sentinel makes it terminal in deliverWithRetry, like every other
// refused credential configuration.
func requireSecret(channel *model.NotificationChannel, secret string) error {
	if secret == "" {
		return fmt.Errorf("%s channel %q has no secret: %w", channel.ChannelType, channel.Token,
			httpsink.ErrMissingCredential)
	}
	return nil
}

// trunc shortens s to at most max runes, marking the cut with an ellipsis. Vendor APIs
// reject, rather than clip, over-long fields, and an alarm key is tenant-authored.
func trunc(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// severityColor is the accent colour for a transition: a clear reads green and an
// acknowledgement blue whatever the severity, otherwise the severity picks it.
func severityColor(a AlarmFacts) string {
	switch a.EventType {
	case "CLEARED":
		return "#2E7D32"
	case "ACKNOWLEDGED":
		return "#1565C0"
	}
	switch a.Severity {
	case "CRITICAL":
		return "#B71C1C"
	case "MAJOR":
		return "#E65100"
	case "MINOR":
		return "#F9A825"
	case "WARNING":
		return "#FBC02D"
	default:
		return "#757575"
	}
}

// open reports whether the alarm is still demanding attention: not cleared, not yet
// acknowledged. Only then is the acknowledge hint shown.
func (a AlarmFacts) open() bool {
	return a.EventType != "CLEARED" && a.EventType != "ACKNOWLEDGED" && !a.Acknowledged && a.State != "CLEARED"
}

// title is the one-line heading of a message.
func (a AlarmFacts) title(msg *RenderedNotification) string {
	if a.AlarmKey == "" {
		return msg.Subject
	}
	if a.EventType == "RENOTIFIED" {
		return fmt.Sprintf("Alarm still unresolved (escalation %d): %s", a.Escalation, a.AlarmKey)
	}
	return fmt.Sprintf("Alarm %s: %s", strings.ToLower(a.EventType), a.AlarmKey)
}

// facts returns the (label, value) pairs a chat message lists, in display order, skipping
// what the transition does not carry.
func (a AlarmFacts) facts() [][2]string {
	out := [][2]string{{"Severity", a.Severity}}
	if a.PreviousSeverity != "" {
		out = append(out, [2]string{"Previous severity", a.PreviousSeverity})
	}
	if a.State != "" {
		out = append(out, [2]string{"State", a.State})
	}
	if a.Originator != "" {
		out = append(out, [2]string{"Source", a.Originator})
	}
	if a.DeviceTypeToken != "" {
		out = append(out, [2]string{"Device type", a.DeviceTypeToken})
	}
	if a.MetricKey != "" {
		out = append(out, [2]string{"Metric", a.MetricKey})
	}
	if a.Value != nil {
		out = append(out, [2]string{"Value", fmt.Sprintf("%g", *a.Value)})
	}
	if !a.RaisedTime.IsZero() {
		out = append(out, [2]string{"Raised", formatTime(a.RaisedTime)})
	}
	return out
}

// alarmLink builds the message's link from the channel's configured console base URL, or
// "" when none is configured.
func alarmLink(base string) string {
	if base == "" {
		return ""
	}
	return base + alarmLinkPath
}

// deviceLinkPath is the console route of one device's page, which routes by device token.
const deviceLinkPath = "/devices/"

// deviceLink builds the link to the alarm's device page, or "" when the channel has no
// console base URL or the event did not name the device. A device is only linkable by
// token, so a producer that predates the token yields no link rather than a wrong one.
func deviceLink(base, deviceToken string) string {
	if base == "" || deviceToken == "" {
		return ""
	}
	return base + deviceLinkPath + url.PathEscape(deviceToken)
}

// errNoAlarm is returned by an adapter handed a rendering that carries no alarm identity.
// A vendor message built from nothing would be a plausible-looking empty page, so it fails
// loudly instead.
var errNoAlarm = errors.New("notification carries no alarm facts")
