// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"net/http"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-notification-management/model"
)

// PagerDuty Events API v2 hosts. The routing key selects the account, but the account's
// region selects the host, so the channel states it.
const (
	pagerDutyEndpointUS = "https://events.pagerduty.com/v2/enqueue"
	pagerDutyEndpointEU = "https://events.eu.pagerduty.com/v2/enqueue"
)

// PagerDuty event actions.
const (
	pdTrigger     = "trigger"
	pdAcknowledge = "acknowledge"
	pdResolve     = "resolve"
)

// pagerDutyAdapter maps the alarm lifecycle onto the Events API v2:
//
//	RAISED, ESCALATED, DEESCALATED, RENOTIFIED -> trigger (a repeat on an open incident
//	                                               updates it rather than opening another)
//	ACKNOWLEDGED                                -> acknowledge
//	CLEARED                                     -> resolve
//
// Every event carries dedup_key = the alarm token, which is what lets PagerDuty fold the
// whole lifecycle of one alarm into one incident. The routing key is the channel's secret
// and travels in the request body, as the API defines.
type pagerDutyAdapter struct {
	client *http.Client
	// endpoint overrides the region's host; a test points it at an httptest server.
	endpoint string
}

// pdEndpoint is the Events API host for a region.
func pdEndpoint(region string) string {
	if region == model.PagerDutyRegionEU {
		return pagerDutyEndpointEU
	}
	return pagerDutyEndpointUS
}

// pdAction maps an alarm event type to an Events API action. An event type it does not
// know is an error, not a default: guessing "trigger" for an unrecognized transition would
// page a human for something nobody classified.
func pdAction(eventType string) (string, error) {
	switch eventType {
	case "RAISED", "ESCALATED", "DEESCALATED", "RENOTIFIED":
		return pdTrigger, nil
	case "ACKNOWLEDGED":
		return pdAcknowledge, nil
	case "CLEARED":
		return pdResolve, nil
	}
	return "", fmt.Errorf("no PagerDuty action for alarm event type %q", eventType)
}

// pdSeverity maps the alarm tier onto PagerDuty's four.
func pdSeverity(s string) string {
	switch s {
	case "CRITICAL":
		return "critical"
	case "MAJOR":
		return "error"
	case "MINOR", "WARNING":
		return "warning"
	default:
		return "info"
	}
}

// Deliver sends one event. recipients is ignored: the routing key picks the service.
func (a *pagerDutyAdapter) Deliver(ctx context.Context, channel *model.NotificationChannel,
	secret string, _ []string, msg *RenderedNotification) error {
	cfg, linkBaseErr, err := model.ParsePagerDutyConfigForDelivery(channel.Token, dcgraphql.MetadataStr(channel.Config))
	if err != nil {
		return err
	}
	warnInvalidLinkBase(channel.Token, linkBaseErr)
	if err := requireSecret(channel, secret); err != nil {
		return err
	}
	if msg.Alarm.AlarmToken == "" {
		return fmt.Errorf("pagerduty channel %q: %w", channel.Token, errNoAlarm)
	}
	action, err := pdAction(msg.Alarm.EventType)
	if err != nil {
		return fmt.Errorf("pagerduty channel %q: %w", channel.Token, err)
	}
	endpoint := a.endpoint
	if endpoint == "" {
		endpoint = pdEndpoint(cfg.Region)
	}
	// The routing key is in the body, not the URL, so the URL is not scrubbed; nothing in
	// a PagerDuty error response reflects the request body.
	if err := postJSON(ctx, a.client, endpoint, false, nil, pagerDutyEvent(secret, action, cfg, msg)); err != nil {
		return fmt.Errorf("pagerduty channel %q: %w", channel.Token, err)
	}
	return nil
}

// pagerDutyEvent builds the request body. An acknowledge or resolve needs only the key,
// the action and the dedup key; the descriptive payload belongs to a trigger.
func pagerDutyEvent(routingKey, action string, cfg *model.PagerDutyConfig, msg *RenderedNotification) map[string]any {
	a := msg.Alarm
	ev := map[string]any{
		"routing_key":  routingKey,
		"event_action": action,
		"dedup_key":    trunc(a.AlarmToken, 255),
	}
	if action != pdTrigger {
		return ev
	}
	details := map[string]any{
		"alarmToken": a.AlarmToken,
		"alarmKey":   a.AlarmKey,
		"state":      a.State,
		"eventType":  a.EventType,
	}
	if a.Originator != "" {
		details["source"] = a.Originator
	}
	if a.DeviceToken != "" {
		details["deviceToken"] = a.DeviceToken
	}
	if a.DeviceName != "" {
		details["deviceName"] = a.DeviceName
	}
	if a.DeviceTypeToken != "" {
		details["deviceTypeToken"] = a.DeviceTypeToken
	}
	if a.MetricKey != "" {
		details["metric"] = a.MetricKey
	}
	if a.Value != nil {
		details["value"] = *a.Value
	}
	if a.PreviousSeverity != "" {
		details["previousSeverity"] = a.PreviousSeverity
	}
	if a.Escalation > 0 {
		details["escalation"] = a.Escalation
	}
	payload := map[string]any{
		"summary":        trunc(msg.Subject, 1024),
		"source":         cfg.Source,
		"severity":       pdSeverity(a.Severity),
		"class":          trunc(a.AlarmKey, 255),
		"custom_details": details,
	}
	if !a.OccurredTime.IsZero() {
		payload["timestamp"] = a.OccurredTime.UTC().Format(time.RFC3339)
	}
	if a.Originator != "" {
		payload["group"] = trunc(a.Originator, 255)
	}
	ev["payload"] = payload
	var links []map[string]any
	if link := alarmLink(cfg.LinkBaseURL); link != "" {
		links = append(links, map[string]any{"href": link, "text": "Open alarms in DeviceChain"})
	}
	if link := deviceLink(cfg.LinkBaseURL, a.DeviceToken); link != "" {
		links = append(links, map[string]any{"href": link, "text": "Open device in DeviceChain"})
	}
	if len(links) > 0 {
		ev["links"] = links
	}
	return ev
}
