// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"net/url"
	"strings"

	"github.com/devicechain-io/dc-microservice/credbinding"
)

func providerBinding(kind string, endpoint sql.NullString) credbinding.Binding {
	effective := strings.TrimRight(strings.TrimSpace(endpoint.String), "/")
	if effective == "" && kind == string(AIProviderKindAnthropic) {
		effective = "https://api.anthropic.com"
	}
	if u, err := url.Parse(effective); err == nil {
		u.Scheme = strings.ToLower(u.Scheme)
		u.Host = strings.ToLower(u.Host)
		if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
			host := strings.ToLower(u.Hostname())
			if strings.Contains(host, ":") {
				host = "[" + host + "]"
			}
			u.Host = host
		}
		effective = u.String()
	}
	return credbinding.New("AI provider").With("kind", kind).With("endpoint", effective)
}
