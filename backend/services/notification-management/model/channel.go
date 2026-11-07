// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// ChannelType is a delivery mechanism the notification service DEFINES for carrying
// an alarm notification (ADR-017). This slice declares the planned set as a static
// capability list; the concrete adapters (the SMTP sender, the webhook poster) and
// the per-tenant channel configuration that references these ids land in later
// slices. Keeping the catalog here, not in the GraphQL layer, lets the dispatch code
// and the schema share one source of truth.
//
// Available reports whether a working adapter for the type actually ships yet, so a
// policy UI can list a defined-but-not-yet-deliverable type without implying a
// notification through it would be sent. Both v1 types deliver as of N.C; the flag
// stays as the seam a future defined-but-unshipped type (SMS, push) would use.
type ChannelType struct {
	Id          string
	Label       string
	Description string
	Available   bool
}

const (
	// ChannelTypeSMTP delivers a notification as an email over SMTP.
	ChannelTypeSMTP = "smtp"
	// ChannelTypeWebhook POSTs a notification as JSON to an HTTP endpoint. A Slack
	// incoming-webhook URL is one such endpoint, so Slack rides this adapter rather
	// than bundling a vendor SDK (ADR-017 keeps SMS/push/vendor SDKs out of core).
	ChannelTypeWebhook = "webhook"
	// ChannelTypeSlack posts a Block Kit message to a Slack incoming-webhook URL, which is
	// the channel's secret.
	ChannelTypeSlack = "slack"
	// ChannelTypeTeams posts an Adaptive Card to a Microsoft Teams Workflows (Power
	// Automate) webhook URL, which is the channel's secret.
	ChannelTypeTeams = "teams"
	// ChannelTypePagerDuty sends PagerDuty Events API v2 events, mapping the alarm
	// lifecycle onto trigger, acknowledge and resolve with dedup_key = the alarm token. The
	// routing key is the channel's secret.
	ChannelTypePagerDuty = "pagerduty"
)

// SupportedChannelTypes is the catalog of channel types this service defines, in
// display order. ADR-017 ships SMTP + webhook (Slack incoming webhooks can be
// targeted) in v1; SMS/push stay pluggable community adapters and are intentionally
// absent. Both types are available as of N.C (their adapters ship in the processor
// layer — keep in sync with newAdapterRegistry).
var SupportedChannelTypes = []ChannelType{
	{
		Id:          ChannelTypeSMTP,
		Label:       "Email (SMTP)",
		Description: "Deliver alarm notifications as email through an SMTP server.",
		Available:   true,
	},
	{
		Id:          ChannelTypeWebhook,
		Label:       "Webhook",
		Description: "POST alarm notifications as JSON to an HTTP endpoint. A Slack incoming webhook can be targeted (the payload carries a text summary).",
		Available:   true,
	},
	{
		Id:          ChannelTypeSlack,
		Label:       "Slack",
		Description: "Post alarm notifications to Slack as Block Kit messages, coloured by severity, through an incoming-webhook URL.",
		Available:   true,
	},
	{
		Id:          ChannelTypeTeams,
		Label:       "Microsoft Teams",
		Description: "Post alarm notifications to Microsoft Teams as Adaptive Cards through a Workflows (Power Automate) webhook URL.",
		Available:   true,
	},
	{
		Id:          ChannelTypePagerDuty,
		Label:       "PagerDuty",
		Description: "Page through PagerDuty Events API v2: a raised alarm triggers an incident, an acknowledgement acknowledges it and a clear resolves it, all keyed on the alarm.",
		Available:   true,
	},
}
