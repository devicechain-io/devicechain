// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/credbinding"
)

func channelBinding(token, kind string, config *string) credbinding.Binding {
	b := credbinding.New(kind)
	var value any
	var err error
	var fields map[string]bool
	switch kind {
	case ChannelTypeWebhook:
		fields = webhookFields
		value, err = ParseWebhookConfig(token, config)
	case ChannelTypeSlack, ChannelTypeTeams:
		fields = linkFields
		value, err = ParseLinkConfig(token, kind, config)
	case ChannelTypePagerDuty:
		fields = pagerDutyFields
		value, err = ParsePagerDutyConfig(token, config)
	case ChannelTypeSMTP:
		fields = smtpFields
		value, err = ParseSMTPConfig(token, config)
	default:
		var raw []byte
		if config != nil {
			raw = []byte(*config)
		}
		return credbinding.Opaque(kind, raw)
	}
	if err != nil {
		var raw []byte
		if config != nil {
			raw = []byte(*config)
		}
		return credbinding.Opaque(kind, raw)
	}
	return credbinding.Fields(b, value, fields)
}

var webhookFields = map[string]bool{"url": false, "method": true, "headers": true, "auth": false, "authHeader": false, "authScheme": false, "signing": true, "linkBaseUrl": true}

var linkFields = map[string]bool{"linkBaseUrl": true}

var pagerDutyFields = map[string]bool{"region": false, "source": true, "linkBaseUrl": true}

var smtpFields = map[string]bool{"host": false, "port": false, "security": false, "from": true, "username": true}
