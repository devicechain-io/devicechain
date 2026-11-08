// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// alertFlags are the alert-routing flags `dcctl install` and `dcctl upgrade` share. One
// struct and one request builder, so the two verbs cannot come to mean different things
// by the same flag.
type alertFlags struct {
	webhook      string
	watchdog     string
	email        []string
	smtpHost     string
	smtpFrom     string
	smtpUser     string
	smtpPassFile string
	config       string
	clear        bool
}

const (
	alertWebhookUsage = "send alerts to this webhook URL (Alertmanager's webhook format, which Slack-compatible relays, PagerDuty, Opsgenie and most incident tools accept). " +
		"Every alert the platform raises is dropped without a receiver. The URL is a credential and is kept in a Secret, never in the infrastructure state. " +
		"Naming any --alert-* flag restates ALL the routing"
	alertEmailUsage    = "send alerts to this address (repeatable). Needs --alert-smtp-host and --alert-smtp-from"
	alertWatchdogUsage = "send the always-firing Watchdog alert to this URL, every minute, so a dead-man's-switch service (Healthchecks, Dead Man's Snitch, Cronitor) tells you when the alerting pipeline itself goes silent. " +
		"Without it a broken Alertmanager looks exactly like a healthy, quiet cluster"
	alertSMTPHostUsage = "SMTP server for --alert-email, as host:port (for example smtp.example.com:587)"
	alertSMTPFromUsage = "sender address for --alert-email"
	alertSMTPUserUsage = "SMTP user for --alert-email. Needs --alert-smtp-password-file"
	alertSMTPPassUsage = "read the SMTP password from this file, so it never appears in the process list or shell history"
	alertConfigUsage   = "use this Alertmanager configuration file as it is, for receivers beyond webhook and email (Slack, PagerDuty, OpsGenie, Teams...). " +
		"It replaces what --alert-webhook and --alert-email would build, so route the Watchdog alert inside it; dcctl checks that it parses and that every route names a receiver it defines"
	alertClearUsage = "remove the alert routing: alerts go back to no receiver and the instance reports ALERTS NOT ROUTED"
)

func (f *alertFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.webhook, "alert-webhook", "", alertWebhookUsage)
	fl.StringSliceVar(&f.email, "alert-email", nil, alertEmailUsage)
	fl.StringVar(&f.smtpHost, "alert-smtp-host", "", alertSMTPHostUsage)
	fl.StringVar(&f.smtpFrom, "alert-smtp-from", "", alertSMTPFromUsage)
	fl.StringVar(&f.smtpUser, "alert-smtp-user", "", alertSMTPUserUsage)
	fl.StringVar(&f.smtpPassFile, "alert-smtp-password-file", "", alertSMTPPassUsage)
	fl.StringVar(&f.watchdog, "alert-watchdog-webhook", "", alertWatchdogUsage)
	fl.StringVar(&f.config, "alert-config", "", alertConfigUsage)
	fl.BoolVar(&f.clear, "clear-alert-routing", false, alertClearUsage)
}

// request reads the files the flags name and validates the whole request from argv alone,
// before any cluster exists.
func (f *alertFlags) request() (bootstrap.AlertRoutingRequest, error) {
	r := bootstrap.AlertRoutingRequest{
		Webhook:         strings.TrimSpace(f.webhook),
		WatchdogWebhook: strings.TrimSpace(f.watchdog),
		EmailTo:         f.email,
		SMTPHost:        f.smtpHost,
		SMTPFrom:        f.smtpFrom,
		SMTPUser:        f.smtpUser,
		Clear:           f.clear,
	}
	if f.smtpPassFile != "" {
		b, err := os.ReadFile(f.smtpPassFile)
		if err != nil {
			return r, fmt.Errorf("--alert-smtp-password-file: %w", err)
		}
		r.SMTPPassword = strings.TrimRight(string(b), "\r\n")
		if r.SMTPPassword == "" {
			return r, fmt.Errorf("--alert-smtp-password-file %s is empty", f.smtpPassFile)
		}
	}
	if f.config != "" {
		b, err := os.ReadFile(f.config)
		if err != nil {
			return r, fmt.Errorf("--alert-config: %w", err)
		}
		r.Config = string(b)
		if strings.TrimSpace(r.Config) == "" {
			return r, fmt.Errorf("--alert-config %s is empty", f.config)
		}
	}
	return r, r.Validate()
}
