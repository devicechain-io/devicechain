// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

func TestTenantCommandUsesRecordedRelease(t *testing.T) {
	oldBinding, oldRead := resolveTargetBinding, readTargetInstanceCR
	oldTenant, oldEmail, oldPassword := attrTenant, attrEmail, attrPassword
	t.Cleanup(func() {
		resolveTargetBinding, readTargetInstanceCR = oldBinding, oldRead
		attrTenant, attrEmail, attrPassword = oldTenant, oldEmail, oldPassword
	})
	attrTenant, attrEmail, attrPassword = "tenant", "email", "password"
	resolveTargetBinding = func(bootstrap.Options) (bootstrap.ClusterBinding, bootstrap.BindingSource) {
		return bootstrap.ClusterBinding{KubeContext: "recorded"}, bootstrap.BindingFromRecord
	}
	for _, version := range []string{"v1.7.3", "v1.8.2", "", "v1.9.0", "v1.8.0-rc.1"} {
		for _, status := range []int{400, 404, 405, 204} {
			t.Run(version+"/"+http.StatusText(status), func(t *testing.T) {
				operations, discoveries := 0, 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodOptions {
						discoveries++
						if version == "v1.9.0" {
							_ = json.NewEncoder(w).Encode(map[string]any{"format": 1, "schema": "type Query { modern: String }"})
						} else {
							w.WriteHeader(status)
						}
						return
					}
					var body struct{ Query string }
					_ = json.NewDecoder(r.Body).Decode(&body)
					expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
					switch {
					case strings.Contains(body.Query, "login("):
						_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"login": map[string]any{"identityToken": "identity", "expiresAt": expires}}})
					case strings.Contains(body.Query, "selectTenant("):
						_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"selectTenant": map[string]any{"accessToken": "access", "refreshToken": "refresh", "expiresAt": expires}}})
					default:
						operations++
						_, _ = w.Write([]byte("{\"data\":{\"__typename\":\"Query\",\"modern\":\"ok\"}}"))
					}
				}))
				defer srv.Close()
				u, _ := url.Parse(srv.URL)
				readTargetInstanceCR = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
					return &dcv1beta1.Instance{Spec: dcv1beta1.InstanceSpec{Host: u.Host, ImageVersion: version}}, nil
				}
				cmd := &cobra.Command{Use: "attr"}
				cmd.SetContext(context.Background())
				cmd.Flags().String("server", "localhost", "")
				cmd.Flags().Bool("tls", false, "")
				addInstanceTargetFlags(cmd)
				if err := cmd.ParseFlags([]string{"--instance", "prod"}); err != nil {
					t.Fatal(err)
				}
				client, endpoint, err := attrSession(cmd)
				if err != nil {
					t.Fatal(err)
				}
				query := "{__typename}"
				if version == "v1.9.0" {
					query = "{modern}"
				}
				err = client.Query(context.Background(), endpoint, query, nil, nil)
				if version == "" || version == "v1.8.0-rc.1" {
					if err == nil || !strings.Contains(err.Error(), "--instance") || !strings.Contains(err.Error(), "v1.9") || operations != 0 {
						t.Fatalf("unknown release must refuse with remedy: err=%v operations=%d", err, operations)
					}
					return
				}
				if err != nil || operations != 1 || discoveries != 1 {
					t.Fatalf("recorded %s: err=%v operations=%d discoveries=%d", version, err, operations, discoveries)
				}
				if version != "v1.9.0" {
					var unsupported *userclient.UnsupportedFeatureError
					err = client.Query(context.Background(), endpoint, "{modern}", nil, nil)
					if !errors.As(err, &unsupported) || !unsupported.Conservative || operations != 1 {
						t.Fatalf("must enforce v1.7 contract before dispatch: %v (operations=%d)", err, operations)
					}
				}
			})
		}
	}
}
