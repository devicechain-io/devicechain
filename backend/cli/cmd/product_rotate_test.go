// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestProductRotateIntegrationToken(t *testing.T) {
	for _, serverError := range []string{"", "unknown integration", "forbidden", "connection refused", "empty issued token"} {
		t.Run("server="+serverError, func(t *testing.T) {
			const issued = "one-time-bearer-secret"
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Query     string         `json:"query"`
					Variables map[string]any `json:"variables"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.Contains(body.Query, "login("):
					fmt.Fprintf(w, `{"data":{"login":{"identityToken":"identity","expiresAt":%q}}}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
				case strings.Contains(body.Query, "selectTenant("):
					fmt.Fprintf(w, `{"data":{"selectTenant":{"accessToken":"tenant-access","expiresAt":%q}}}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
				default:
					calls++
					if r.URL.Path != "/api/device-management/graphql" || r.Header.Get("Authorization") != "Bearer tenant-access" {
						t.Errorf("wrong endpoint or session: %s %s", r.URL.Path, r.Header.Get("Authorization"))
					}
					if !strings.Contains(body.Query, "rotateIntegrationToken(token:$token)") || body.Variables["token"] != "lorawan" || len(body.Variables) != 1 {
						t.Errorf("wrong mutation or variables: %s %v", body.Query, body.Variables)
					}
					if serverError != "" && serverError != "empty issued token" {
						fmt.Fprintf(w, `{"errors":[{"message":%q}]}`, serverError)
						return
					}
					token := issued
					if serverError == "empty issued token" {
						token = ""
					}
					fmt.Fprintf(w, `{"data":{"rotateIntegrationToken":{"issuedToken":%q}}}`, token)
				}
			}))
			defer srv.Close()
			wantCalls := 1
			if serverError == "connection refused" {
				srv.Close()
				wantCalls = 0
			}
			// Execute the public command with its real persistent flags and session.
			productCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
				old := f.Value.String()
				changed := f.Changed
				t.Cleanup(func() {
					_ = f.Value.Set(old)
					f.Changed = changed
				})
			})
			root := &cobra.Command{Use: "dcctl", SilenceUsage: true}
			rootCmd.RemoveCommand(productCmd)
			root.AddCommand(productCmd)
			t.Cleanup(func() {
				root.RemoveCommand(productCmd)
				rootCmd.AddCommand(productCmd)
			})
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs([]string{"product", "rotate-integration-token", "lorawan", "--server", strings.TrimPrefix(srv.URL, "http://"), "--tenant", "tenant", "--email", "operator@example.com", "--password", "pw"})
			err := root.Execute()
			if serverError != "" {
				// The dial error's wording is the OS's ("connection refused" on Linux,
				// "actively refused" on Windows), so that case asserts only the failure.
				if err == nil || (serverError != "connection refused" && !strings.Contains(err.Error(), serverError)) || stdout.Len() != 0 {
					t.Fatalf("want server error and empty stdout: err=%v stdout=%q", err, stdout.String())
				}
			} else {
				if err != nil || stdout.String() != issued+"\n" {
					t.Fatalf("want only issued token and newline: err=%v stdout=%q", err, stdout.String())
				}
				for _, warning := range []string{"new token", "lorawan", "previous token", "at once"} {
					if !strings.Contains(stderr.String(), warning) {
						t.Errorf("stderr lacks %q: %q", warning, stderr.String())
					}
				}
			}
			if calls != wantCalls || strings.Contains(stderr.String(), issued) {
				t.Errorf("calls=%d; stderr must never contain the issued token: %q", calls, stderr.String())
			}
		})
	}
}
