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

// claimLinkBaseWarning reports whether this channel and error have not been warned about yet.
func claimLinkBaseWarning(token string, err error) bool {
	_, loaded := webhookLinkBaseWarnings.LoadOrStore(struct{ token, text string }{token, err.Error()}, struct{}{})
	return !loaded
}

func warnInvalidLinkBase(token string, err error) {
	if err == nil {
		return
	}
	if claimLinkBaseWarning(token, err) {
		log.Warn().Err(err).Str("channel", token).Msg("Ignoring invalid linkBaseUrl; links omitted")
	}
}
