// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/fleetop"
	"github.com/spf13/cobra"
)

type fleetReadinessClient struct {
	state   string
	orphans int
}

func (c fleetReadinessClient) Query(_ context.Context, _ string, _ string, _ map[string]any, out any) error {
	data, err := json.Marshal(map[string]any{"fleetOperationReadiness": map[string]any{
		"protocolVersion": 1, "enforcement": c.state, "executableKinds": []string{}, "orphanedAttributeRows": c.orphans,
	}})
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func TestFleetOperationReadinessCommandStatus(t *testing.T) {
	previous := fleetOpSession
	t.Cleanup(func() { fleetOpSession = previous })
	for _, state := range []string{"NOT_INSTALLED", "DISABLED", "UNREADABLE", "ENFORCED"} {
		for _, orphans := range []int{0, 2} {
			fleetOpSession = func(*cobra.Command) (fleetop.Client, string, error) {
				return fleetReadinessClient{state, orphans}, "test", nil
			}
			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			cmd.Flags().String("output", "json", "")
			err := runFleetOpReadiness(cmd, nil)
			if (err == nil) != (state == "ENFORCED" && orphans == 0) {
				t.Fatalf("%s/%d: %v", state, orphans, err)
			}
			if err != nil && exitCode(err) == 0 {
				t.Fatal("non-ready command exits zero")
			}
			if !json.Valid(out.Bytes()) {
				t.Fatalf("invalid JSON output: %s", out.String())
			}
		}
	}
}

func TestFleetOperationReadinessCommandRegistration(t *testing.T) {
	cmd, args, err := rootCmd.Find([]string{"fleet-op", "readiness"})
	if err != nil || len(args) != 0 || cmd != fleetOpReadinessCmd {
		t.Fatalf("readiness command missing: %v %v", args, err)
	}
	for _, c := range []*cobra.Command{fleetOpCmd, cmd} {
		if !strings.HasPrefix(c.Short, "Preview.") || !strings.HasPrefix(c.Long, "Preview.") {
			t.Fatal("fleet operation command does not identify Preview")
		}
	}
	for _, name := range []string{"tenant", "server", "tls", "email", "password", "otp"} {
		if fleetOpCmd.PersistentFlags().Lookup(name) == nil {
			t.Fatalf("missing session flag --%s", name)
		}
	}
}
