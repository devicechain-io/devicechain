// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/devicechain-io/dcctl/bootstrap"
)

func resetAlertFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		installAlerts, upgradeAlerts = alertFlags{}, alertFlags{}
		upgradeAlertRequest = bootstrap.AlertRoutingRequest{}
		installNoMonitoring = false
		for _, c := range []*cobra.Command{installCmd, upgradeCmd} {
			for _, n := range []string{"alert-webhook", "alert-email", "alert-smtp-host", "alert-smtp-from",
				"alert-smtp-user", "alert-smtp-password-file", "alert-watchdog-webhook", "alert-config", "clear-alert-routing"} {
				if f := c.Flags().Lookup(n); f != nil {
					f.Changed = false
				}
			}
		}
	})
}

// The alert flags exist on BOTH verbs and are bound to the variables the builders read, and the
// builders carry them into the options the engine is handed. Driven through the cobra flag set
// itself, so a flag registered on the wrong variable, or a field dropped from a literal, fails.
func TestTheAlertFlagsReachTheOptionsOfBothVerbs(t *testing.T) {
	resetAlertFlags(t)
	dir := t.TempDir()
	pass := filepath.Join(dir, "smtp-pass")
	if err := os.WriteFile(pass, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		verb string
		cmd  *cobra.Command
	}{{"install", installCmd}, {"upgrade", upgradeCmd}} {
		for flag, value := range map[string]string{
			"alert-webhook": "https://hooks.example.test/x", "alert-watchdog-webhook": "https://hc.example.test/y",
			"alert-email": "ops@example.test", "alert-smtp-host": "smtp.example.test:587",
			"alert-smtp-from": "dc@example.test", "alert-smtp-user": "mailer", "alert-smtp-password-file": pass,
		} {
			if err := c.cmd.Flags().Set(flag, value); err != nil {
				t.Fatalf("%s has no --%s: %v", c.verb, flag, err)
			}
		}
	}

	in, err := installOptions(nil, bootstrap.RestorePlan{}, bootstrap.ImageSource{})
	if err != nil {
		t.Fatal(err)
	}
	req, err := upgradeAlerts.request()
	if err != nil {
		t.Fatal(err)
	}
	upgradeAlertRequest = req
	up := upgradeOptions("prod", nil, nil)

	for verb, got := range map[string]bootstrap.AlertRoutingRequest{"install": in.AlertRouting, "upgrade": up.AlertRouting} {
		if got.Webhook != "https://hooks.example.test/x" || got.WatchdogWebhook != "https://hc.example.test/y" ||
			len(got.EmailTo) != 1 || got.SMTPHost != "smtp.example.test:587" || got.SMTPFrom != "dc@example.test" ||
			got.SMTPUser != "mailer" || got.SMTPPassword != "s3cret" {
			t.Errorf("%s dropped part of the alert request: %+v", verb, got)
		}
	}
}

func TestAlertFlagsDefaultToNothing(t *testing.T) {
	resetAlertFlags(t)
	in, err := installOptions(nil, bootstrap.RestorePlan{}, bootstrap.ImageSource{})
	if err != nil {
		t.Fatal(err)
	}
	if in.AlertRouting.Given() || upgradeOptions("prod", nil, nil).AlertRouting.Given() {
		t.Error("with no flag, a run must not touch the routing")
	}
}

func TestAnInstallWithNoMonitoringRefusesAlertFlags(t *testing.T) {
	resetAlertFlags(t)
	installNoMonitoring = true
	if err := installCmd.Flags().Set("alert-webhook", "https://hooks.example.test/x"); err != nil {
		t.Fatal(err)
	}
	_, err := installOptions(nil, bootstrap.RestorePlan{}, bootstrap.ImageSource{})
	if err == nil || !strings.Contains(err.Error(), "monitoring") {
		t.Fatalf("got %v, want a refusal naming the monitoring stack", err)
	}
}

func TestAlertFlagFilesAreReadAndChecked(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "am.yaml")
	if err := os.WriteFile(cfg, []byte("route: {receiver: r}\nreceivers:\n  - name: r\n    webhook_configs: [{url: 'https://x.test/h'}]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("route: {receiver: ghost}\nreceivers:\n  - name: r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := alertFlags{config: cfg}
	if r, err := f.request(); err != nil || !strings.Contains(r.Config, "webhook_configs") {
		t.Fatalf("a good config: %+v, %v", r, err)
	}
	for name, f := range map[string]alertFlags{
		"a missing config":        {config: filepath.Join(dir, "nope")},
		"an empty config":         {config: empty},
		"a config naming a ghost": {config: bad},
		"an empty password file":  {email: []string{"a@example.test"}, smtpHost: "s:25", smtpFrom: "b@example.test", smtpUser: "u", smtpPassFile: empty},
		"a missing password file": {email: []string{"a@example.test"}, smtpHost: "s:25", smtpFrom: "b@example.test", smtpUser: "u", smtpPassFile: filepath.Join(dir, "nope")},
	} {
		if _, err := f.request(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// RunE settles the alert request (reads the files, validates) before anything else that can
// fail, and upgradeOptions copies what it settled. Driven through RunE itself, stopped by a
// refusal that comes right after the settle, so a hand-off dropped from RunE is not invisible.
func TestUpgradeRunESettlesTheAlertRequest(t *testing.T) {
	resetAlertFlags(t)
	t.Cleanup(func() { upgradeResetValues, upgradeValues = false, nil })
	if err := upgradeCmd.Flags().Set("alert-webhook", "https://hooks.example.test/x"); err != nil {
		t.Fatal(err)
	}
	upgradeResetValues, upgradeValues = true, []string{"x"} // refused after the settle, before any cluster
	err := upgradeCmd.RunE(upgradeCmd, []string{"local", "prod"})
	if err == nil || !strings.Contains(err.Error(), "--reset-values") {
		t.Fatalf("got %v, want the refusal that follows the settle", err)
	}
	if upgradeAlertRequest.Webhook != "https://hooks.example.test/x" {
		t.Fatalf("RunE did not settle the alert request: %+v", upgradeAlertRequest)
	}
}
