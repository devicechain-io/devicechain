// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installjson"
	"github.com/devicechain-io/dcctl/internal/installtui"
)

const deprecatedBackupEndpointFile = `{
  "endpointUrl": "https://dcEndpointUser:dcEndpointPassword@s3.example.com/archive?token=dcEndpointQuery#dcEndpointFragment",
  "bucketRdb": "rdb-archive",
  "bucketTsdb": "tsdb-archive",
  "accessKeyId": "example-access-key",
  "secretAccessKey": "example-secret-access-key"
}`

const cleanBackupEndpointFile = `{
  "endpointUrl": "https://s3.example.com/archive",
  "bucketRdb": "rdb-archive",
  "bucketTsdb": "tsdb-archive",
  "accessKeyId": "example-access-key",
  "secretAccessKey": "example-secret-access-key"
}`

func TestBackupEndpointDeprecationReachesPlainAndJSON(t *testing.T) {
	for _, name := range []string{"backup-endpoint-deprecated", "json-backup-endpoint-deprecated",
		"backup-endpoint-clean", "json-backup-endpoint-clean"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sc := findInstallScenario(t, name)
			r := runInstallChildRaw(t, sc)
			if r.exit != 0 {
				t.Fatalf("deprecated endpoints must still work: exit %d, stderr %s", r.exit, r.stderr)
			}
			all := string(r.stdout) + string(r.stderr)
			if strings.Contains(all, "dcEndpoint") {
				t.Fatalf("output disclosed an endpoint credential: %s", all)
			}
			const notice = "credentials in backup endpointUrl"
			want := strings.Contains(name, "deprecated")
			if strings.Contains(all, notice) != want {
				t.Fatalf("warning present = %v, want %v: %s", strings.Contains(all, notice), want, all)
			}
			if !isJSONScenario(sc) {
				if want && !strings.Contains(all, "warning: "+notice) {
					t.Fatalf("deprecation is not labelled as a warning: %s", all)
				}
				return
			}
			envs, err := installjson.DecodeStream(r.stdout)
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, e := range envs {
				if l, ok := e.Payload.(installflow.LogLine); ok && strings.Contains(l.Text.String(), notice) {
					found++
				}
			}
			if (found == 1) != want || found > 1 {
				t.Fatalf("JSON carries %d deprecation events, want warning = %v", found, want)
			}
		})
	}
}

func TestBackupEndpointDeprecationReachesTUIReviewAndRun(t *testing.T) {
	saveChoiceFlags(t)
	p := filepath.Join(t.TempDir(), "backup.json")
	if err := os.WriteFile(p, []byte(deprecatedBackupEndpointFile), 0o600); err != nil {
		t.Fatal(err)
	}
	w, _ := wizardFor(t, "--backup-credentials-file", p)
	text := reviewText(w.Review)
	if strings.Contains(text, "dcEndpoint") || !strings.Contains(text, "credentials in backup endpointUrl") {
		t.Fatalf("the review must show a redacted deprecation: %s", text)
	}
	r, _ := replanned(t, w, installtui.Edits{})
	e := r.Engine.(*bootstrap.InstallEngine)
	if len(e.Warnings) != 1 || !strings.Contains(text, e.Warnings[0]) {
		t.Fatalf("the TUI run and review have different warnings: %q", e.Warnings)
	}
}
