// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// ChannelType is a delivery mechanism the notification service can carry an alarm
// notification through (ADR-017). This slice declares the supported set as a static
// capability list; the concrete adapters (the SMTP sender, the webhook poster) and
// the per-tenant channel configuration that references these ids land in later
// slices. Keeping the catalog here, not in the GraphQL layer, lets the dispatch code
// and the schema share one source of truth.
type ChannelType struct {
	Id          string
	Label       string
	Description string
}

const (
	// ChannelTypeSMTP delivers a notification as an email over SMTP.
	ChannelTypeSMTP = "smtp"
	// ChannelTypeWebhook POSTs a notification as JSON to an HTTP endpoint. A Slack
	// incoming-webhook URL is one such endpoint, so Slack rides this adapter rather
	// than bundling a vendor SDK (ADR-017 keeps SMS/push/vendor SDKs out of core).
	ChannelTypeWebhook = "webhook"
)

// SupportedChannelTypes is the catalog of channel types this service delivers
// through, in display order. ADR-017 ships SMTP + webhook (Slack-compatible) in v1;
// SMS/push stay pluggable community adapters and are intentionally absent.
var SupportedChannelTypes = []ChannelType{
	{
		Id:          ChannelTypeSMTP,
		Label:       "Email (SMTP)",
		Description: "Deliver alarm notifications as email through an SMTP server.",
	},
	{
		Id:          ChannelTypeWebhook,
		Label:       "Webhook",
		Description: "POST alarm notifications as JSON to an HTTP endpoint (Slack incoming webhooks are compatible).",
	},
}
