// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/bulkchange"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestBulkChangeCommandsArePreviewAndNeverLegacy(t *testing.T) {
	for _, name := range []string{"plan", "create", "start", "status", "lines", "pause", "resume", "abort", "reverse", "resolve"} {
		c, rest, err := rootCmd.Find([]string{"bulk-change", name})
		if err != nil || len(rest) != 0 || c.Name() != name {
			t.Errorf("bulk-change %s missing: %v %v", name, rest, err)
			continue
		}
		if !strings.HasPrefix(c.Short, "Preview.") || !strings.HasPrefix(c.Long, "Preview.") {
			t.Errorf("%s help must start Preview.", name)
		}
		c.InheritedFlags()
		check := func(f *pflag.Flag) {
			if !strings.HasPrefix(f.Usage, "Preview.") {
				t.Errorf("%s --%s help must start Preview.", name, f.Name)
			}
		}
		c.Flags().VisitAll(check)
		c.InheritedFlags().VisitAll(check)
		legacy, _, _ := rootCmd.Find([]string{"fleet-op", name})
		if legacy != fleetOpCmd {
			t.Errorf("%s was born under deprecated fleet-op", name)
		}
	}
}

type bulkCommandClient func(string, map[string]any) (any, error)

func (f bulkCommandClient) Query(_ context.Context, _ string, query string, vars map[string]any, out any) error {
	value, err := f(query, vars)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func executeBulkCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "dcctl", SilenceErrors: true, SilenceUsage: true}
	parent, _ := newBulkChangeCommand("bulk-change")
	root.AddCommand(parent)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"bulk-change"}, args...))
	err := root.ExecuteContext(t.Context())
	return out.String(), err
}

func TestBulkChangeCommandRequestsAndExitCodes(t *testing.T) {
	previous := bulkChangeSession
	t.Cleanup(func() { bulkChangeSession = previous })
	var query string
	var variables map[string]any
	dispatch, evidence := "READY", "OPEN"
	bulkChangeSession = func(*cobra.Command) (bulkchange.Client, string, error) {
		return bulkCommandClient(func(q string, vars map[string]any) (any, error) {
			query, variables = q, vars
			if strings.Contains(q, "planBulkChange(") {
				return map[string]any{"plan": map[string]any{"admittedCount": 1, "contractDigest": "server-digest"}}, nil
			}
			return map[string]any{"change": bulkchange.Change{Token: "chosen", Dispatch: dispatch, Evidence: evidence, ContractDigest: "server-digest"}}, nil
		}), "test", nil
	}
	for _, action := range []string{"plan", "create"} {
		out, err := executeBulkCommand(t, action, "--kind", "config.set", "--key", "mode", "--value", "", "--type", "STRING", "--device", "d,1", "--device", "d2", "--tenant", "tenant", "--token", "chosen", "--name", "review", "--max-posts-per-second", "2", "--failure-threshold", "3", "--ambiguity-threshold", "4", "--settle-deadline", "5m", "-o", "json")
		if err != nil || !json.Valid([]byte(out)) || !strings.Contains(query, action+"BulkChange(") {
			t.Fatalf("%s: %v %s", action, err, out)
		}
		request := variables["request"].(bulkchange.Request)
		if request.Token != "chosen" || request.Name != "review" || request.DeviceTokens[0] != "d,1" || *request.ConfigSet.Value != "" || request.Safety["settleDeadlineSeconds"] != int64(300) || request.Safety["maxPostsPerSecond"] != 2 {
			t.Fatalf("lost flags: %+v", request)
		}
	}
	for _, action := range []string{"start", "pause", "resume", "abort"} {
		args := []string{action, "chosen", "-o", "json"}
		if action == "start" || action == "resume" {
			args = append(args, "--contract-digest", "reviewed")
		}
		if out, err := executeBulkCommand(t, args...); err != nil || !json.Valid([]byte(out)) || !strings.Contains(query, action+"BulkChange(") {
			t.Fatalf("%s: %v %s", action, err, out)
		}
		if action == "start" || action == "resume" {
			if variables["digest"] != "reviewed" {
				t.Fatal("reviewed digest changed")
			}
		}
	}
	for _, tc := range []struct {
		dispatch, evidence string
		code               int
	}{{"DISPATCHED", "CLOSED", 0}, {"DISPATCHED", "OPEN", 1}, {"PAUSED", "OPEN", 3}, {"ABORTED", "OPEN", 4}} {
		dispatch, evidence = tc.dispatch, tc.evidence
		out, err := executeBulkCommand(t, "status", "chosen", "-o", "json")
		code := 0
		if err != nil {
			code = exitCode(err)
		}
		if code != tc.code || !strings.Contains(out, tc.evidence) {
			t.Fatalf("status %s/%s: code=%d %v %s", dispatch, evidence, code, err, out)
		}
	}
}

func TestBulkChangeRejectsInvalidInputBeforeAuthentication(t *testing.T) {
	previous := bulkChangeSession
	t.Cleanup(func() { bulkChangeSession = previous })
	bulkChangeSession = func(*cobra.Command) (bulkchange.Client, string, error) {
		t.Error("invalid local input reached authentication")
		return nil, "", fmt.Errorf("should not authenticate")
	}
	valid := []string{"create", "--kind", "config.set", "--key", "k", "--unset", "--device", "d"}
	for _, args := range [][]string{
		{"start", "t"}, {"status"}, {"status", "one", "two"}, {"lines", "t", "-o", "xml"},
		{"create"}, {"create", "--kind", "config.set", "--key", "k", "--device", "d", "--value", "x"},
		append(append([]string{}, valid...), "--value", "x"),
		append(append([]string{}, valid...), "--devices-file", "other.txt"),
		append(append([]string{}, valid...), "--max-posts-per-second", "51"),
		append(append([]string{}, valid...), "--failure-threshold", "0"),
		append(append([]string{}, valid...), "--settle-deadline", "1.5s"),
		append(append([]string{}, valid...), "--settle-deadline", "59"),
		{"resume", "t", "--ambiguity-threshold", "-1"},
	} {
		if _, err := executeBulkCommand(t, args...); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestBulkChangeFileTokenDefaultsAndUnset(t *testing.T) {
	previous := bulkChangeSession
	t.Cleanup(func() { bulkChangeSession = previous })
	path := filepath.Join(t.TempDir(), "devices.txt")
	raw := []byte("d1\r\nd2\r\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	bulkChangeSession = func(*cobra.Command) (bulkchange.Client, string, error) {
		return bulkCommandClient(func(_ string, vars map[string]any) (any, error) {
			request := vars["request"].(bulkchange.Request)
			want, _ := bulkchange.DefaultToken("tenant", raw, "config.set", bulkchange.ConfigSet{Key: "k", Unset: true})
			if request.Token != want || request.Name != want || !request.ConfigSet.Unset || request.ConfigSet.Value != nil || len(request.DeviceTokens) != 2 || len(request.Safety) != 0 {
				t.Errorf("wrong default request: %+v", request)
			}
			return map[string]any{"change": bulkchange.Change{Token: request.Token}}, nil
		}), "test", nil
	}
	if _, err := executeBulkCommand(t, "create", "--tenant", "tenant", "--kind", "config.set", "--key", "k", "--unset", "--devices-file", path); err != nil {
		t.Fatal(err)
	}
}

func TestBulkChangeLinesCommandJoinsFileAndPreservesRedaction(t *testing.T) {
	previous := bulkChangeSession
	t.Cleanup(func() { bulkChangeSession = previous })
	path := filepath.Join(t.TempDir(), "devices.txt")
	if err := os.WriteFile(path, []byte("missing\nerased\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bulkChangeSession = func(*cobra.Command) (bulkchange.Client, string, error) {
		return bulkCommandClient(func(query string, vars map[string]any) (any, error) {
			if strings.Contains(query, "bulkChangeLines(") {
				criteria := vars["criteria"].(map[string]any)
				if criteria["posting"] != "REFUSED" {
					t.Error("lost posting filter")
				}
				return map[string]any{"lines": map[string]any{
					"results":    []bulkchange.Line{{Ordinal: 1, Posting: "REFUSED"}, {Ordinal: 2, Posting: "REFUSED", Redacted: true}},
					"pagination": map[string]any{"pageStart": 1, "pageEnd": 2, "totalRecords": 2},
				}}, nil
			}
			return map[string]any{"change": bulkchange.Change{Token: "t"}}, nil
		}), "test", nil
	}
	for _, format := range []string{"json", "csv", "table"} {
		out, err := executeBulkCommand(t, "lines", "t", "--posting", "REFUSED", "--devices-file", path, "-o", format)
		if err != nil || !strings.Contains(out, "missing") || strings.Contains(out, "erased") {
			t.Fatalf("%s provenance/redaction: %v %s", format, err, out)
		}
	}
}

// bulkStepUpClient is a command-level fake whose StepUp is counted.
type bulkStepUpClient struct {
	bulkCommandClient
	stepUps *int
}

func (c bulkStepUpClient) StepUp(context.Context) error {
	*c.stepUps++
	return nil
}

func TestBulkChangeReversePreviewsByDefaultAndCreatesOnlyWhenAsked(t *testing.T) {
	previous := bulkChangeSession
	t.Cleanup(func() { bulkChangeSession = previous })
	var queries []string
	var request bulkchange.ReversalRequest
	bulkChangeSession = func(*cobra.Command) (bulkchange.Client, string, error) {
		return bulkCommandClient(func(q string, vars map[string]any) (any, error) {
			queries = append(queries, q)
			request = vars["request"].(bulkchange.ReversalRequest)
			if strings.Contains(q, "previewBulkChangeReversal(") {
				return map[string]any{"preview": map[string]any{"reversibleCount": 2, "changedSinceCount": 1, "contractDigest": "server-digest"}}, nil
			}
			return map[string]any{"change": bulkchange.Change{Token: "undo-1", Dispatch: "READY", Evidence: "OPEN", ContractDigest: "server-digest"}}, nil
		}), "test", nil
	}
	out, err := executeBulkCommand(t, "reverse", "change-1", "--token", "undo-1", "--ordinals", "1,2", "--override-ordinals", "2", "--max-posts-per-second", "3", "--settle-deadline", "2m", "-o", "json")
	if err != nil || len(queries) != 1 || !strings.Contains(queries[0], "previewBulkChangeReversal(") || strings.Contains(queries[0], "reverseBulkChange(") {
		t.Fatalf("preview: %v %v %s", err, queries, out)
	}
	if !strings.Contains(out, "server-digest") || !strings.Contains(out, "Nothing was written") {
		t.Fatalf("preview output: %s", out)
	}
	if request.Original != "change-1" || request.Token != "undo-1" || request.Name != "undo-1" || len(request.Ordinals) != 2 ||
		len(request.OverrideOrdinals) != 1 || request.OverrideOrdinals[0] != 2 || request.OverrideChanged ||
		request.Safety["maxPostsPerSecond"] != 3 || request.Safety["settleDeadlineSeconds"] != int64(120) {
		t.Fatalf("lost flags: %+v", request)
	}
	queries = nil
	out, err = executeBulkCommand(t, "reverse", "change-1", "--token", "undo-1", "--name", "undo", "--override-changed", "--create", "-o", "json")
	if err != nil || len(queries) != 1 || !strings.Contains(queries[0], "reverseBulkChange(") || !strings.Contains(out, `"dispatch":"READY"`) {
		t.Fatalf("create: %v %v %s", err, queries, out)
	}
	if request.Ordinals != nil || request.OverrideOrdinals != nil || !request.OverrideChanged || request.Name != "undo" || request.Safety != nil {
		t.Fatalf("create request: %+v", request)
	}
}

func TestBulkChangeResolveSendsTheVerdictAndStepsUp(t *testing.T) {
	previous := bulkChangeSession
	t.Cleanup(func() { bulkChangeSession = previous })
	var resolution bulkchange.Resolution
	calls, stepUps := 0, 0
	bulkChangeSession = func(*cobra.Command) (bulkchange.Client, string, error) {
		return bulkStepUpClient{bulkCommandClient(func(q string, vars map[string]any) (any, error) {
			calls++
			if !strings.Contains(q, "resolveAmbiguousBulkChangeLines(") {
				t.Errorf("resolve sent %s", q)
			}
			resolution = vars["request"].(bulkchange.Resolution)
			if calls == 1 {
				return nil, &userclient.GraphQLError{Messages: []string{"stale"}, Codes: []string{"STEP_UP_REQUIRED"}}
			}
			return map[string]any{"change": bulkchange.Change{Token: "change-1", Dispatch: "DISPATCHED", Evidence: "OPEN"}}, nil
		}), &stepUps}, "test", nil
	}
	out, err := executeBulkCommand(t, "resolve", "change-1", "--ordinals", "3", "--ordinals", "5", "--verdict", "confirmed", "--note", "checked on site", "-o", "json")
	if err != nil || calls != 2 || stepUps != 1 || !json.Valid([]byte(out)) {
		t.Fatalf("resolve: %v calls=%d stepUps=%d %s", err, calls, stepUps, out)
	}
	if resolution.Token != "change-1" || len(resolution.Ordinals) != 2 || resolution.Ordinals[1] != 5 || resolution.AllAmbiguous ||
		resolution.Verdict != "CONFIRMED" || resolution.Note != "checked on site" {
		t.Fatalf("lost flags: %+v", resolution)
	}
	calls = 1 // answer the next call at once
	if _, err := executeBulkCommand(t, "resolve", "change-1", "--all-ambiguous", "--verdict", "FAILED", "--note", "no answer"); err != nil || !resolution.AllAmbiguous || resolution.Ordinals != nil || resolution.Verdict != "FAILED" {
		t.Fatalf("all ambiguous: %v %+v", err, resolution)
	}
}

func TestBulkChangeReverseAndResolveRejectInvalidInputBeforeAuthentication(t *testing.T) {
	previous := bulkChangeSession
	t.Cleanup(func() { bulkChangeSession = previous })
	bulkChangeSession = func(*cobra.Command) (bulkchange.Client, string, error) {
		t.Error("invalid local input reached authentication")
		return nil, "", fmt.Errorf("should not authenticate")
	}
	for _, args := range [][]string{
		{"reverse"}, {"reverse", "c"}, {"reverse", "c", "--token", ""},
		{"reverse", "c", "--token", "u", "--ordinals", ""},
		{"reverse", "c", "--token", "u", "--ordinals", "0"},
		{"reverse", "c", "--token", "u", "--override-ordinals", ""},
		{"reverse", "c", "--token", "u", "--override-changed", "--override-ordinals", "1"},
		{"reverse", "c", "--token", "u", "--ordinals", "1", "--override-ordinals", "2"},
		{"reverse", "c", "--token", "u", "--max-posts-per-second", "51"},
		{"reverse", "c", "--token", "u", "--settle-deadline", "30s"},
		{"reverse", "c", "--token", "u", "-o", "csv"},
		{"resolve", "c", "--verdict", "confirmed", "--note", "n"},
		{"resolve", "c", "--all-ambiguous", "--ordinals", "1", "--verdict", "confirmed", "--note", "n"},
		{"resolve", "c", "--all-ambiguous", "--verdict", "maybe", "--note", "n"},
		{"resolve", "c", "--all-ambiguous", "--verdict", "failed"},
		{"resolve", "c", "--all-ambiguous", "--verdict", "failed", "--note", strings.Repeat("x", 1001)},
		{"resolve", "--all-ambiguous", "--verdict", "failed", "--note", "n"},
	} {
		if _, err := executeBulkCommand(t, args...); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
