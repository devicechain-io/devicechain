// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installchoice

import (
	"fmt"
	"os"
	"strings"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// Alerts carries references to credential files, never their contents.
type Alerts struct {
	Webhook      string
	Watchdog     string
	Email        []string
	SMTPHost     string
	SMTPFrom     string
	SMTPUser     string
	SMTPPassFile string
	Config       string
	Clear        bool
}

// Request reads protected file inputs and validates the existing routing rules.
func (f Alerts) Request() (bootstrap.AlertRoutingRequest, error) {
	r := bootstrap.AlertRoutingRequest{
		Webhook:         strings.TrimSpace(f.Webhook),
		WatchdogWebhook: strings.TrimSpace(f.Watchdog),
		EmailTo:         f.Email,
		SMTPHost:        f.SMTPHost,
		SMTPFrom:        f.SMTPFrom,
		SMTPUser:        f.SMTPUser,
		Clear:           f.Clear,
	}
	if f.SMTPPassFile != "" {
		b, err := os.ReadFile(f.SMTPPassFile)
		if err != nil {
			return r, fmt.Errorf("--alert-smtp-password-file: %w", err)
		}
		r.SMTPPassword = strings.TrimRight(string(b), "\r\n")
		if r.SMTPPassword == "" {
			return r, fmt.Errorf("--alert-smtp-password-file %s is empty", f.SMTPPassFile)
		}
	}
	if f.Config != "" {
		b, err := os.ReadFile(f.Config)
		if err != nil {
			return r, fmt.Errorf("--alert-config: %w", err)
		}
		r.Config = string(b)
		if strings.TrimSpace(r.Config) == "" {
			return r, fmt.Errorf("--alert-config %s is empty", f.Config)
		}
	}
	return r, r.Validate()
}
