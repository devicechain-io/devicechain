// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// attrCommand is a flag set shaped like the real subcommands', carrying the values a run
// reads from it.
func attrCommand(server, otp string) *cobra.Command {
	c := &cobra.Command{}
	c.SetContext(context.Background())
	c.Flags().String("server", server, "")
	c.Flags().Bool("tls", false, "")
	c.Flags().String("otp", otp, "")
	c.Flags().String("scope", "", "")
	c.Flags().String("type", "", "")
	c.Flags().String("output", "table", "")
	return c
}

func setAttrCreds(t *testing.T, tenant, email, password string) {
	t.Helper()
	oldT, oldE, oldP := attrTenant, attrEmail, attrPassword
	attrTenant, attrEmail, attrPassword = tenant, email, password
	t.Cleanup(func() { attrTenant, attrEmail, attrPassword = oldT, oldE, oldP })
}

// Every credential flag is required, and the refusal comes before any network call.
func TestAttrRefusesMissingTenantOrCredentials(t *testing.T) {
	for name, creds := range map[string][3]string{
		"tenant":   {"", "a@b.c", "pw"},
		"email":    {"t", "", "pw"},
		"password": {"t", "a@b.c", ""},
	} {
		setAttrCreds(t, creds[0], creds[1], creds[2])
		err := runAttrUnset(attrCommand("127.0.0.1:1", ""), []string{"device", "d1", "k"})
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("%s missing: want a 'required' refusal, got %v", name, err)
		}
	}
}

// `attr` takes a password, so it takes --otp: a sign-in to an account with a second factor
// stops in ONE attempt naming the flag, and the code reaches verifyMfa when given.
func TestAttrHandlesTheSecondFactor(t *testing.T) {
	withOTPSeams(t, false, "")
	s := &mfaServer{}
	srv := httptest.NewServer(s)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	setAttrCreds(t, "acme", "root@example.com", "pw")

	err := runAttrUnset(attrCommand(host, ""), []string{"device", "d1", "k"})
	if err == nil || !strings.Contains(err.Error(), "--otp") {
		t.Fatalf("want an error naming --otp, got %v", err)
	}
	if s.logins != 1 || len(s.verified) != 0 {
		t.Errorf("logins=%d verifies=%v; one challenge, nothing verified", s.logins, s.verified)
	}

	s.logins = 0
	_ = runAttrUnset(attrCommand(host, "987654"), []string{"device", "d1", "k"})
	if len(s.verified) != 1 || s.verified[0] != "987654" {
		t.Errorf("the --otp code did not reach verifyMfa: %v", s.verified)
	}
}

func TestAttrOutputFlagIsValidated(t *testing.T) {
	c := attrCommand("h", "")
	_ = c.Flags().Set("output", "yaml")
	if _, err := attrOutputIsJSON(c); err == nil {
		t.Error("an unknown --output must be refused")
	}
	_ = c.Flags().Set("output", "json")
	if j, err := attrOutputIsJSON(c); err != nil || !j {
		t.Errorf("json: %v %v", j, err)
	}
}

func TestAttrCommandsTakeOTPAndAreRegistered(t *testing.T) {
	if attrCmd.PersistentFlags().Lookup("otp") == nil {
		t.Error("dcctl attr takes a password but not --otp")
	}
	for _, sub := range []string{"set", "get", "unset"} {
		if c, _, err := rootCmd.Find([]string{"attr", sub}); err != nil || c.Name() != sub {
			t.Errorf("dcctl attr %s is not registered: %v", sub, err)
		}
	}
}
