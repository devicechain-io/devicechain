// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-notification-management/model"
)

// slackAdapter delivers a notification to a Slack incoming webhook as a Block Kit message:
// a header, a field grid, the acknowledge hint while the alarm is open, and an "Open
// alarms" button when the channel has a console base URL. The webhook URL is the channel's
// secret. The message sits in an attachment so Slack draws the severity colour bar down
// its left edge; the top-level "text" is the plain fallback used for push previews.
type slackAdapter struct {
	client *http.Client
}

// Deliver posts the message. recipients is ignored: a Slack webhook is bound to one
// conversation by Slack itself.
func (a *slackAdapter) Deliver(ctx context.Context, channel *model.NotificationChannel,
	secret string, _ []string, msg *RenderedNotification) error {
	cfg, err := model.ParseLinkConfig(channel.Token, model.ChannelTypeSlack, dcgraphql.MetadataStr(channel.Config))
	if err != nil {
		return err
	}
	if err := requireSecret(channel, secret); err != nil {
		return err
	}
	if msg.Alarm.AlarmToken == "" {
		return fmt.Errorf("slack channel %q: %w", channel.Token, errNoAlarm)
	}
	if err := postJSON(ctx, a.client, secret, true, nil, slackMessage(msg, alarmLink(cfg.LinkBaseURL), deviceLink(cfg.LinkBaseURL, msg.Alarm.DeviceToken))); err != nil {
		return fmt.Errorf("slack channel %q: %w", channel.Token, err)
	}
	return nil
}

// slackEscape escapes the three characters Slack's mrkdwn treats as control (&, <, >), so a
// tenant-authored alarm key or metric name cannot inject a mention or a link.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// slackMessage builds the Block Kit body.
func slackMessage(msg *RenderedNotification, link, deviceLink string) map[string]any {
	a := msg.Alarm
	fields := make([]map[string]any, 0, 8)
	for _, f := range a.facts() {
		fields = append(fields, map[string]any{
			"type": "mrkdwn",
			"text": trunc("*"+f[0]+"*\n"+slackEscape(f[1]), 2000),
		})
	}
	blocks := []map[string]any{
		{"type": "header", "text": map[string]any{"type": "plain_text", "text": trunc(a.title(msg), 150)}},
		// Slack allows at most 10 fields in a section.
		{"type": "section", "fields": fields},
	}
	if a.open() {
		blocks = append(blocks, map[string]any{
			"type":     "context",
			"elements": []map[string]any{{"type": "mrkdwn", "text": ackHint}},
		})
	}
	var buttons []map[string]any
	if link != "" {
		buttons = append(buttons, map[string]any{
			"type": "button",
			"text": map[string]any{"type": "plain_text", "text": "Open alarms"},
			"url":  link,
		})
	}
	if deviceLink != "" {
		buttons = append(buttons, map[string]any{
			"type": "button",
			"text": map[string]any{"type": "plain_text", "text": "Open device"},
			"url":  deviceLink,
		})
	}
	if len(buttons) > 0 {
		blocks = append(blocks, map[string]any{"type": "actions", "elements": buttons})
	}
	return map[string]any{
		// The top-level text is mrkdwn too (push previews, fallbacks), and it carries a
		// device-authored name: escape it so <!channel> or a link cannot ride in.
		"text":        slackEscape(msg.Subject),
		"attachments": []map[string]any{{"color": severityColor(a), "blocks": blocks}},
	}
}
