// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// fakeAdmin answers the deletion queries from one purging tenant and one record.
type fakeAdmin struct{ erased bool }

func (f fakeAdmin) Query(_ context.Context, _ string, q string, _ map[string]any, out any) error {
	var resp map[string]any
	epoch := "2026-10-29T12:00:00.5Z"
	switch {
	case strings.Contains(q, "tenantDeletion("):
		d := map[string]any{"token": "acme", "epoch": epoch, "awaiting": "STORES", "rowsErased": 3,
			"blockedBy": []string{"tsdb: holds rows"}, "stores": []any{}, "erased": f.erased}
		if f.erased {
			d["erasedAt"] = "2026-10-29T12:05:00Z"
			d["awaiting"] = "TOKEN_HOLD"
		}
		resp = map[string]any{"tenantDeletion": d}
	case strings.Contains(q, "tenants"):
		resp = map[string]any{"tenants": []any{map[string]any{
			"token": "acme", "purgeState": "purging", "purgeEpoch": epoch}}}
	default:
		return errors.New("unexpected query")
	}
	b, _ := json.Marshal(resp)
	return json.Unmarshal(b, out)
}

func runStatus(t *testing.T, erased bool) (stdout string, err error) {
	t.Helper()
	old, oldWait := tdOutput, tdWait
	tdOutput, tdWait = "json", false
	defer func() { tdOutput, tdWait = old, oldWait }()
	c := &cobra.Command{}
	var o, e bytes.Buffer
	c.SetOut(&o)
	c.SetErr(&e)
	err = tenantDeletionStatus(context.Background(), c, fakeAdmin{erased: erased}, "u", "acme", "", "erased")
	return o.String(), err
}

// -o json prints the record whether or not the target was reached, and the exit status
// is still the verdict: a deletion that is not erased is still an error.
func TestTenantDeletionStatusJSONKeepsTheVerdict(t *testing.T) {
	for _, erased := range []bool{false, true} {
		out, err := runStatus(t, erased)
		if (err == nil) != erased {
			t.Fatalf("erased=%v: err = %v", erased, err)
		}
		var d map[string]any
		if jerr := json.Unmarshal([]byte(out), &d); jerr != nil {
			t.Fatalf("erased=%v: stdout is not JSON: %v\n%s", erased, jerr, out)
		}
		if d["token"] != "acme" || d["erased"] != erased {
			t.Fatalf("erased=%v: record = %v", erased, d)
		}
	}
}

func TestOutputFormatsRefuseAnUnknownValue(t *testing.T) {
	old := instancesOutput
	defer func() { instancesOutput = old }()
	instancesOutput = "yaml"
	err := instancesListCmd.RunE(instancesListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--output must be table or json") {
		t.Fatalf("instances list: err = %v", err)
	}

	oldDl, oldE, oldP := dlOutput, dlEmail, dlPassword
	defer func() { dlOutput, dlEmail, dlPassword = oldDl, oldE, oldP }()
	dlOutput, dlEmail, dlPassword = "yaml", "a@b.c", "pw"
	err = runDeadLettersList(deadLettersListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--output must be table or json") {
		t.Fatalf("dead-letters list: err = %v", err)
	}

	oldTd, oldTe, oldTp := tdOutput, tdEmail, tdPassword
	defer func() { tdOutput, tdEmail, tdPassword = oldTd, oldTe, oldTp }()
	tdOutput, tdEmail, tdPassword = "yaml", "a@b.c", "pw"
	err = runTenantDeletionStatus(tenantDeletionStatusCmd, []string{"acme"})
	if err == nil || !strings.Contains(err.Error(), "--output must be table or json") {
		t.Fatalf("tenant-deletion status: err = %v", err)
	}
}

// With no instances, -o json is an empty array and not prose, so a script can parse it.
func TestInstancesJSONIsAnArray(t *testing.T) {
	var b bytes.Buffer
	if err := writeInstancesJSON(context.Background(), &b, nil, listingProbes{}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(b.String()) != "[]" {
		t.Fatalf("empty listing = %q, want []", b.String())
	}

	b.Reset()
	known := []bootstrap.KnownInstance{
		knownLocal(),
		{Instance: "old"}, // bootstrapped before records existed
	}
	probes := probesReturning(false, nil, nil, nil)
	if err := writeInstancesJSON(context.Background(), &b, known, probes); err != nil {
		t.Fatal(err)
	}
	var rows []instanceRow
	if err := json.Unmarshal(b.Bytes(), &rows); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b.String())
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	r := rows[0]
	if r.Instance != "inst" || r.Namespace != bootstrap.InstanceNamespace("inst") || r.Provider != "local" ||
		r.Cluster != "c" || r.KubeContext != "kind-c" || !r.HasRecord || r.Status == "" {
		t.Fatalf("row 0 = %+v", r)
	}
	if rows[1].HasRecord || rows[1].Provider != "" || rows[1].Status == "" {
		t.Fatalf("row 1 = %+v", rows[1])
	}
}

type fakeHaReport struct {
	ok   bool
	Name string
}

func (f fakeHaReport) OK() bool       { return f.ok }
func (f fakeHaReport) Format() string { return "TEXT REPORT\n" }

// --json prints the report alone on stdout, and --expect-fail still inverts the status:
// the negative control turns red when a check passes that was expected to fail.
func TestHaConcludeHonoursJSONAndExpectFail(t *testing.T) {
	cases := []struct {
		name               string
		ok, asJSON, expect bool
		code               int
		wantMsg            string
	}{
		{"pass", true, true, false, 0, ""},
		{"fail", false, true, false, 1, ""},
		{"control held", false, true, true, 0, "negative control held"},
		{"control red", true, true, true, 1, "NEGATIVE CONTROL FAILED"},
		{"text control red", true, false, true, 1, "NEGATIVE CONTROL FAILED"},
	}
	for _, c := range cases {
		var out, errOut bytes.Buffer
		code := concludeHaReport(&out, &errOut, fakeHaReport{ok: c.ok, Name: "x"}, c.asJSON, c.expect, "passed", "failed")
		if code != c.code {
			t.Fatalf("%s: code = %d, want %d", c.name, code, c.code)
		}
		if c.asJSON {
			var v map[string]any
			if err := json.Unmarshal(out.Bytes(), &v); err != nil {
				t.Fatalf("%s: stdout is not only JSON: %v\n%s", c.name, err, out.String())
			}
			if c.wantMsg != "" && !strings.Contains(errOut.String(), c.wantMsg) {
				t.Fatalf("%s: stderr = %q", c.name, errOut.String())
			}
		} else if !strings.Contains(out.String(), "TEXT REPORT") || !strings.Contains(out.String(), c.wantMsg) {
			t.Fatalf("%s: stdout = %q", c.name, out.String())
		}
	}
}
