// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"sync"

	"github.com/rs/zerolog/log"
)

// webhookLinkBaseWarnings suppresses repeated warnings for the same channel and link error
// across webhook, Slack, Teams and PagerDuty deliveries.
var webhookLinkBaseWarnings sync.Map

func warnInvalidLinkBase(token string, err error) {
	if err == nil {
		return
	}
	key := struct{ token, text string }{token, err.Error()}
	if _, loaded := webhookLinkBaseWarnings.LoadOrStore(key, struct{}{}); !loaded {
		log.Warn().Err(err).Str("channel", token).Msg("Ignoring invalid linkBaseUrl; links omitted")
	}
}
