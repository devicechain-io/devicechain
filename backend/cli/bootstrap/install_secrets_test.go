// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installjson"
)

func canaryValue(t *testing.T, label string) string {
	t.Helper()
	for _, c := range testkit.Canaries() {
		if c.Label == label {
			return c.Value
		}
	}
	t.Fatalf("no canary %s", label)
	return ""
}

// 🔴 THE INSTALL'S OWN SECRETS NEVER REACH THE EVENT STREAM, even when a tool echoes
// one into the console the stream takes as log lines. The engine registers every
// secret value an install carries with the hub: those given on argv (the alert
// webhooks) when the run starts, and those read from files after the preflight (the
// SMTP password, backup credentials) once they are read. This runs the real engine
// through the JSON presenter, with a cluster stand-in that prints each secret the way
// a careless tool would, and holds the stream to the canary harness.
func TestInstallSecretsAreRedactedFromTheStream(t *testing.T) {
	webhook, watchdog := canaryValue(t, "webhook-url"), canaryValue(t, "webhook-url")+"/watchdog"
	smtp, s3 := canaryValue(t, "smtp-password"), canaryValue(t, "s3-secret-key")
	argv := InstallOptions{}
	argv.AlertRouting = AlertRoutingRequest{Webhook: webhook, WatchdogWebhook: watchdog}
	argv.AssumeYes = true
	resolved := argv
	resolved.AlertRouting.SMTPPassword = smtp
	resolved.BackupDestination = &BackupDestination{AccessKeyID: "dcCanaryAccessKeyID0001", SecretAccessKey: s3}

	var printed []string
	e := &InstallEngine{
		Provider: &fakeProvider{name: "local"}, Options: argv, SkipPreflight: true,
		ResolveOptions: func() (InstallOptions, error) { return resolved, nil },
		Cluster: func(ctx context.Context, _ Provider, opts InstallOptions) error {
			c := out(ctx)
			for _, v := range []string{opts.AlertRouting.Webhook, opts.AlertRouting.WatchdogWebhook,
				opts.AlertRouting.SMTPPassword, opts.BackupDestination.SecretAccessKey, opts.BackupDestination.AccessKeyID} {
				printed = append(printed, v)
				c.println("tool says: " + v)
				c.printf("%s\n", color8(v))
			}
			return nil
		},
	}
	var stream bytes.Buffer
	err := installjson.Presenter{Stdout: &stream, RunID: "secrets", Clock: testkit.NewManualClock(testkit.Epoch)}.Run(
		t.Context(), e, installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(printed) != 5 {
		t.Fatalf("the stand-in printed %d values: the test proves nothing", len(printed))
	}
	envs, derr := installjson.DecodeStream(stream.Bytes())
	if derr != nil {
		t.Fatal(derr)
	}
	if verr := testkit.VerifyStream(envs); verr != nil {
		t.Fatal(verr)
	}
	testkit.AssertNoCanary(t, "the stream", stream.Bytes())
	for _, v := range printed {
		if bytes.Contains(stream.Bytes(), []byte(v)) {
			t.Errorf("the stream carries %q", v)
		}
	}
	if n := bytes.Count(stream.Bytes(), []byte("[redacted]")); n < len(printed) {
		t.Errorf("%d redactions for %d printed secrets", n, len(printed))
	}
}

// color8 wraps v in an SGR sequence, as a colourising tool would: the escape is
// stripped before redaction, so the secret inside is still found.
func color8(v string) string { return "\x1b[33m" + v + "\x1b[0m" }
