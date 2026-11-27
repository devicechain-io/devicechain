// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui"
)

// Every credential below carries the marker "ZX", and nothing else does.
var cmdCredentialRegistries = []string{
	"user:dcPwZX@registry.example/org",
	"https://u:dcPwZX@registry.example/org",
	"oci://dcTokZX@[2001:db8::1]:5000/org",
	"registry.example/org?token=dcTokZX",
	"registry.example/org#dc%43anaryZX",
}

// The terminal UI's welcome screen names the image source, registry included.
func TestTheWizardRedactsTheRegistryItNames(t *testing.T) {
	provider, err := bootstrap.Get("local")
	if err != nil {
		t.Fatal(err)
	}
	for _, build := range []bool{false, true} {
		for _, reg := range append([]string{"ghcr.io/devicechain-io"}, cmdCredentialRegistries...) {
			saveChoiceFlags(t)
			args := []string{"local", "--registry", reg, "--version", "v1.12.0"}
			if build {
				args = []string{"local", "--registry", reg, "--build", "--version", "v1.12.0"}
			}
			c := parseChoices(t, args)
			prepared, err := c.Prepare(Version, nil)
			if err != nil {
				t.Fatalf("%s: %v", reg, err)
			}
			images := installWizardFor(provider, c, prepared).Facts.Images
			if strings.Contains(strings.ToLower(images), "zx") {
				t.Errorf("the welcome screen shows a credential: %q", images)
			}
			if reg == "ghcr.io/devicechain-io" {
				if !strings.Contains(images, reg) {
					t.Errorf("a registry with no credential is not named as given: %q", images)
				}
			} else if !strings.Contains(images, installflow.URLRedaction) {
				t.Errorf("the welcome screen does not show the redaction: %q", images)
			}
		}
	}
}

// `dcctl backup` and `dcctl restore` dial --nats-url; the client quotes a URL it
// cannot parse whole, userinfo and all.
func TestABackupBrokerDialFailureRedactsTheURL(t *testing.T) {
	for _, u := range []string{"nats://u:dcPwZX@host:x1", "nats://dcTokZX@host:x1"} {
		_, err := backupBroker(t.Context(), u, config.NatsConfiguration{})
		if err == nil {
			t.Fatalf("%s: the dial did not fail", u)
		}
		if strings.Contains(strings.ToLower(err.Error()), "zx") {
			t.Errorf("the dial error shows a credential: %v", err)
		}
		if !strings.Contains(err.Error(), installflow.URLRedaction) {
			t.Errorf("the dial error no longer names the URL: %v", err)
		}
	}
}

// The advanced drawer lists every flag given, with its value: a URL-valued one
// carrying a credential is shown as configured, never as typed — including the forms
// url.Parse misreads (no scheme, a fragment with no host).
func TestTheDrawerHidesAURLValuedFlagCarryingACredential(t *testing.T) {
	for _, tc := range []struct{ flag, value string }{
		{"registry", "user:dcPwZX@registry.example/org"},
		{"registry", "registry.example/org#dc%43anaryZX"},
		{"restore-rdb-from", "dc-rdb#dcTokZX"},
		{"restore-rdb-from", "s3://u:dcPwZX@bucket/archive"},
		{"restore-rdb-from", "dc-rdb?sig=dcSigZX"},
	} {
		saveChoiceFlags(t)
		c := parseChoices(t, []string{"local", "--" + tc.flag + "=" + tc.value})
		var row *installtui.Setting
		for _, s := range wizardSettings(c, c, installtui.Edits{}) {
			if s.Flag == "--"+tc.flag {
				row = &s
			}
		}
		if row == nil {
			t.Fatalf("--%s is not in the drawer", tc.flag)
		}
		if strings.Contains(strings.ToLower(row.Value+row.Source), "zx") || row.Value != configuredHidden {
			t.Errorf("--%s=%s is shown in the drawer as %q (%s)", tc.flag, tc.value, row.Value, row.Source)
		}
	}
	saveChoiceFlags(t)
	c := parseChoices(t, []string{"local", "--registry=ghcr.io/devicechain-io"})
	for _, s := range wizardSettings(c, c, installtui.Edits{}) {
		if s.Flag == "--registry" && s.Value != "ghcr.io/devicechain-io" {
			t.Errorf("a registry with no credential is shown as %q", s.Value)
		}
	}
}
