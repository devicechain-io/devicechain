// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"fmt"
	"sort"
	"strings"
)

// AnnotationAlertRouting publishes where the cluster's Alertmanager sends the
// alerts this instance's rules raise. dcctl writes it wherever it settles the
// routing (install, bootstrap and every upgrade) from the routing it just wrote
// into the Alertmanager's configuration Secret.
//
// The operator is given no access to Secrets, and the routing is a fact about a
// Secret, so the step that knows it publishes a non-secret summary: only the KINDS
// of receiver, never a URL, an address or a credential. The operator reads it and
// reports the AlertsRouted condition. Absence means nothing has published one (an
// instance last touched by a dcctl from before alert routing existed), which is
// reported as Unknown and never as routed.
//
// The value is a comma-separated list of tokens: "none" alone (no receiver), or any
// of "webhook", "email" and "config" (a receiver of that kind), optionally with
// "watchdog" (the always-firing Watchdog alert is routed to an external dead-man's
// receiver). ParseAlertRouting is the one reader and Format the one writer.
const AnnotationAlertRouting = "core.devicechain.io/alert-routing"

// The tokens AnnotationAlertRouting takes.
const (
	// AlertRoutingNone says no alert reaches a human: the Alertmanager's receiver is
	// the null receiver, which drops everything.
	AlertRoutingNone = "none"
	// AlertReceiverWebhook is a webhook receiver (--alert-webhook).
	AlertReceiverWebhook = "webhook"
	// AlertReceiverEmail is an SMTP email receiver (--alert-email).
	AlertReceiverEmail = "email"
	// AlertReceiverConfig is an operator-supplied Alertmanager configuration
	// (--alert-config), whose receivers dcctl does not model.
	AlertReceiverConfig = "config"
	// AlertWatchdog says the Watchdog alert is routed to an external dead-man's
	// receiver, so the silence of the alerting pipeline itself is noticed.
	AlertWatchdog = "watchdog"
)

// AlertRouting is the parsed AnnotationAlertRouting.
//
// It is not an API type (nothing serves it), so no deepcopy is generated for it.
//
// +kubebuilder:object:generate=false
type AlertRouting struct {
	// Receivers is the kinds of receiver that reach a human: some of
	// AlertReceiverWebhook, AlertReceiverEmail and AlertReceiverConfig. Empty means
	// ALERTS ARE NOT ROUTED.
	Receivers []string
	// Watchdog is whether the Watchdog alert is routed to an external receiver.
	Watchdog bool
}

// Routed reports whether any alert reaches a human.
func (r AlertRouting) Routed() bool { return len(r.Receivers) > 0 }

// Format renders the annotation value: sorted, de-duplicated, and "none" when no
// receiver is configured. Parse(Format(r)) is r.
func (r AlertRouting) Format() string {
	seen := map[string]bool{}
	var out []string
	for _, k := range r.Receivers {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		out = []string{AlertRoutingNone}
	}
	if r.Watchdog {
		out = append(out, AlertWatchdog)
	}
	return strings.Join(out, ",")
}

// ParseAlertRouting reads the annotation value. It refuses what it does not
// recognise rather than guessing: a token a newer dcctl wrote, or a typo, must read
// as unreadable and never as a routed instance.
func ParseAlertRouting(value string) (AlertRouting, error) {
	var r AlertRouting
	none := false
	seen := map[string]bool{}
	for _, tok := range strings.Split(value, ",") {
		tok = strings.TrimSpace(tok)
		switch tok {
		case AlertRoutingNone:
			none = true
		case AlertWatchdog:
			r.Watchdog = true
		case AlertReceiverWebhook, AlertReceiverEmail, AlertReceiverConfig:
			if !seen[tok] {
				seen[tok] = true
				r.Receivers = append(r.Receivers, tok)
			}
		case "":
			return AlertRouting{}, fmt.Errorf("alert routing %q has an empty entry", value)
		default:
			return AlertRouting{}, fmt.Errorf("alert routing %q names %q, which is not one of none, webhook, email, config, watchdog", value, tok)
		}
	}
	if none && len(r.Receivers) > 0 {
		return AlertRouting{}, fmt.Errorf("alert routing %q says both none and a receiver", value)
	}
	if !none && len(r.Receivers) == 0 {
		return AlertRouting{}, fmt.Errorf("alert routing %q names no receiver and is not none", value)
	}
	sort.Strings(r.Receivers)
	return r, nil
}
