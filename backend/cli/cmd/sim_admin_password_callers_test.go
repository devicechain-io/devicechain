// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/devicechain-io/dcctl/sim"
)

// 🔴 THE RESOLVER IS TESTED ABOVE; THIS TESTS ITS CALLERS. `sim create` and `sim destroy`
// used to read --admin-password straight off the flag, whose default was the published
// literal. The flag now has no default, so a caller that went back to reading it would
// sign in with an EMPTY password — and the loadtest gate runs `dcctl sim create` with no
// password at all, relying on the Secret. So each command is driven for real, against a
// server that records what it was sent, and must present the password the instance's
// Secret holds — read for the instance the command is about.

// freshSimCommand builds a command carrying copies of simCmd's persistent flags and sub's
// own, parsed from argv. Copied rather than shared: AddFlagSet shares each *Flag, so a
// value parsed here would stay set on the real command and leak into the next test.
func freshSimCommand(t *testing.T, sub *cobra.Command, argv ...string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: sub.Use, RunE: func(*cobra.Command, []string) error { return nil }}
	copyFlag := func(f *pflag.Flag) {
		if c.Flags().Lookup(f.Name) != nil {
			return
		}
		switch f.Value.Type() {
		case "string":
			c.Flags().StringP(f.Name, f.Shorthand, f.DefValue, f.Usage)
		case "bool":
			v, _ := strconv.ParseBool(f.DefValue)
			c.Flags().BoolP(f.Name, f.Shorthand, v, f.Usage)
		case "int":
			v, _ := strconv.Atoi(f.DefValue)
			c.Flags().IntP(f.Name, f.Shorthand, v, f.Usage)
		case "int64":
			v, _ := strconv.ParseInt(f.DefValue, 10, 64)
			c.Flags().Int64P(f.Name, f.Shorthand, v, f.Usage)
		default:
			t.Fatalf("flag --%s has type %s, which this helper does not copy", f.Name, f.Value.Type())
		}
	}
	simCmd.PersistentFlags().VisitAll(copyFlag)
	sub.Flags().VisitAll(copyFlag)
	if err := c.Flags().Parse(argv); err != nil {
		t.Fatal(err)
	}
	c.SetContext(context.Background())
	c.SetOut(io.Discard)
	return c
}

// aLoginRecorder is a user-management stand-in that records every request body and
// refuses them all, so the command stops at its first sign-in.
func aLoginRecorder(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	var bodies strings.Builder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies.Write(b)
		mu.Unlock()
		http.Error(w, "refused by the test", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return srv, func() string {
		mu.Lock()
		defer mu.Unlock()
		return bodies.String()
	}
}

func stubTheSecret(t *testing.T, password string) *[]string {
	t.Helper()
	var asked []string
	orig := readSuperuserPassword
	t.Cleanup(func() { readSuperuserPassword = orig })
	readSuperuserPassword = func(_ context.Context, kubeContext, instance string) (string, error) {
		asked = append(asked, kubeContext+"|"+instance)
		return password, nil
	}
	return &asked
}

func TestSimCreateSignsInWithTheInstancesSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(adminPasswordEnv, "")
	asked := stubTheSecret(t, "the-instances-seed")
	srv, sent := aLoginRecorder(t)

	c := freshSimCommand(t, simCreateCmd,
		"--server", strings.TrimPrefix(srv.URL, "http://"), "--instance", "acme", "--kube-context", "kind-x")
	if err := runSimCreate(c, []string{"probe"}); err == nil {
		t.Fatal("sim create succeeded against a server that refuses every request")
	}
	if len(*asked) != 1 || (*asked)[0] != "kind-x|acme" {
		t.Errorf("the Secret was read %v, want once for instance acme on kind-x", *asked)
	}
	if !strings.Contains(sent(), "the-instances-seed") {
		t.Errorf("sim create did not sign in with the password the instance's Secret holds; it sent:\n%s", sent())
	}
}

func TestSimDestroySignsInWithTheSecretOfTheInstanceItsRecordNames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(adminPasswordEnv, "")
	asked := stubTheSecret(t, "the-instances-seed")
	srv, sent := aLoginRecorder(t)

	// Created on "acme"; destroyed with --instance left at its default, which names a
	// different instance.
	if err := sim.Save(&sim.Record{
		Name: "probe", Tenant: "sim-probe", InstanceId: "acme",
		Endpoints: sim.Endpoints{UserGraphQL: srv.URL + "/api/user-management/graphql"},
		AdminURL:  srv.URL + "/admin/graphql", ControlAddr: srv.URL,
	}); err != nil {
		t.Fatal(err)
	}
	c := freshSimCommand(t, simDestroyCmd, "--kube-context", "kind-x")
	if err := runSimDestroy(c, []string{"probe"}); err == nil {
		t.Fatal("sim destroy succeeded against a server that refuses every request")
	}
	if len(*asked) != 1 || (*asked)[0] != "kind-x|acme" {
		t.Errorf("the Secret was read %v, want once for the record's instance acme on kind-x", *asked)
	}
	if !strings.Contains(sent(), "the-instances-seed") {
		t.Errorf("sim destroy did not sign in with the password the instance's Secret holds; it sent:\n%s", sent())
	}
}
