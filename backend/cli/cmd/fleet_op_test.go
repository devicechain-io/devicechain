// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bulkchange"
	"github.com/spf13/cobra"
)

type fleetReadinessClient struct {
	state   string
	orphans int
}

type spellingReadinessClient struct {
	fleetReadinessClient
	bulk bool
}

func (c spellingReadinessClient) Query(ctx context.Context, endpoint, query string, vars map[string]any, out any) error {
	if strings.Contains(query, "bulkChangeReadiness") != c.bulk {
		return fmt.Errorf("wrong query for command spelling: %s", query)
	}
	return c.fleetReadinessClient.Query(ctx, endpoint, query, vars, out)
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
			fleetOpSession = func(*cobra.Command) (bulkchange.Client, string, error) {
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

func TestBulkChangeReadinessSpellings(t *testing.T) {
	previous := fleetOpSession
	t.Cleanup(func() { fleetOpSession = previous })
	for _, spelling := range []string{"bulk-change", "fleet-op"} {
		for _, state := range []string{"ENFORCED", "NOT_INSTALLED"} {
			t.Run(spelling+"/"+state, func(t *testing.T) {
				fleetOpSession = func(*cobra.Command) (bulkchange.Client, string, error) {
					return spellingReadinessClient{fleetReadinessClient{state, 0}, spelling == "bulk-change"}, "test", nil
				}
				cmd, args, err := rootCmd.Find([]string{spelling, "readiness"})
				if err != nil || len(args) != 0 || cmd.Name() != "readiness" {
					t.Fatalf("%s readiness missing: %v %v", spelling, args, err)
				}
				var out, stderr bytes.Buffer
				testRoot := &cobra.Command{Use: "dcctl", SilenceErrors: true, SilenceUsage: true}
				parent, _ := newBulkChangeCommand(spelling)
				testRoot.AddCommand(parent)
				testRoot.SetOut(&out)
				testRoot.SetErr(&stderr)
				testRoot.SetArgs([]string{spelling, "readiness", "-o", "json"})
				err = testRoot.ExecuteContext(context.Background())
				if (err == nil) != (state == "ENFORCED") || !json.Valid(out.Bytes()) {
					t.Fatalf("%s: err=%v output=%s", spelling, err, out.String())
				}
				want := ""
				if spelling == "fleet-op" {
					want = "dcctl fleet-op is deprecated; use dcctl bulk-change.\n"
					if cmd.Parent().Deprecated == "" {
						t.Fatal("legacy command is not marked deprecated")
					}
				}
				if stderr.String() != want {
					t.Fatalf("stderr = %q, want %q", stderr.String(), want)
				}
			})
		}
	}
}
