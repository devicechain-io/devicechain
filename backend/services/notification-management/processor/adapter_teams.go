// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"net/http"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-notification-management/model"
)

// teamsAdapter delivers a notification to a Microsoft Teams Workflows (Power Automate)
// webhook as an Adaptive Card. The Office 365 connector's MessageCard is retired, so the
// body is the message envelope the Workflows "when a Teams webhook request is received"
// trigger expects: one attachment of content type application/vnd.microsoft.card.adaptive.
// The webhook URL is the channel's secret.
type teamsAdapter struct {
	client *http.Client
}

// Deliver posts the card. recipients is ignored: the workflow decides where it posts.
func (a *teamsAdapter) Deliver(ctx context.Context, channel *model.NotificationChannel,
	secret string, _ []string, msg *RenderedNotification) error {
	cfg, err := model.ParseLinkConfig(channel.Token, model.ChannelTypeTeams, dcgraphql.MetadataStr(channel.Config))
	if err != nil {
		return err
	}
	if err := requireSecret(channel, secret); err != nil {
		return err
	}
	if msg.Alarm.AlarmToken == "" {
		return fmt.Errorf("teams channel %q: %w", channel.Token, errNoAlarm)
	}
	if err := postJSON(ctx, a.client, secret, true, nil, teamsMessage(msg, alarmLink(cfg.LinkBaseURL), deviceLink(cfg.LinkBaseURL, msg.Alarm.DeviceToken))); err != nil {
		return fmt.Errorf("teams channel %q: %w", channel.Token, err)
	}
	return nil
}

// teamsAccent maps a transition onto an Adaptive Card text colour.
func teamsAccent(a AlarmFacts) string {
	switch a.EventType {
	case "CLEARED":
		return "Good"
	case "ACKNOWLEDGED":
		return "Accent"
	}
	switch a.Severity {
	case "CRITICAL", "MAJOR":
		return "Attention"
	case "MINOR", "WARNING":
		return "Warning"
	default:
		return "Default"
	}
}

// teamsMessage builds the Workflows envelope around the Adaptive Card.
func teamsMessage(msg *RenderedNotification, link, deviceLink string) map[string]any {
	a := msg.Alarm
	facts := make([]map[string]any, 0, 8)
	for _, f := range a.facts() {
		facts = append(facts, map[string]any{"title": f[0], "value": trunc(f[1], 500)})
	}
	body := []map[string]any{
		{
			"type": "TextBlock", "text": trunc(a.title(msg), 300), "weight": "Bolder",
			"size": "Medium", "wrap": true, "color": teamsAccent(a),
		},
		{"type": "FactSet", "facts": facts},
	}
	if a.open() {
		body = append(body, map[string]any{
			"type": "TextBlock", "text": ackHint, "wrap": true, "isSubtle": true, "size": "Small",
		})
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body":    body,
	}
	var actions []map[string]any
	if link != "" {
		actions = append(actions, map[string]any{"type": "Action.OpenUrl", "title": "Open alarms", "url": link})
	}
	if deviceLink != "" {
		actions = append(actions, map[string]any{"type": "Action.OpenUrl", "title": "Open device", "url": deviceLink})
	}
	if len(actions) > 0 {
		card["actions"] = actions
	}
	return map[string]any{
		"type": "message",
		"attachments": []map[string]any{{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"contentUrl":  nil,
			"content":     card,
		}},
	}
}
