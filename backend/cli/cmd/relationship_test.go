// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/apitest"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelationshipCommandsAreRegistered(t *testing.T) {
	for _, sub := range []string{"add", "list", "remove"} {
		c, _, err := rootCmd.Find([]string{"relationship", sub})
		if err != nil || c.Name() != sub {
			t.Errorf("dcctl relationship %s is not registered: %v", sub, err)
		}
	}
}

// Capabilities come from apitest's real service SDL, outside this module. CI must
// select this package when that schema changes, and test with -count=1.
type relationshipFixture struct {
	server          *httptest.Server
	calls           int
	logins          int
	discoveries     int
	discoveryStatus int
	respond         func(string, map[string]any) string
}

const relationshipFixtureRow = `{"token":"edge-1","sourceType":"device","source":{"token":"meter-1"},"targetType":"asset","target":{"token":"pump-1"},"relationshipType":{"token":"feeds"},"metadata":"site A"}`
const relationshipFixturePage = `{"results":[` + relationshipFixtureRow + `],"pagination":{"pageStart":8,"pageEnd":12,"totalRecords":12}}`

func newRelationshipFixture(t *testing.T) *relationshipFixture {
	t.Helper()
	f := &relationshipFixture{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions && r.URL.Path == "/api/device-management/graphql" {
			f.discoveries++
			if f.discoveryStatus != 0 {
				w.WriteHeader(f.discoveryStatus)
				return
			}
		}
		if apitest.ServeCapabilities(t, w, r) {
			return
		}
		var body struct {
			Query     string
			Variables map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		switch {
		case strings.Contains(body.Query, "login("):
			f.logins++
			assert.Equal(t, "/api/user-management/graphql", r.URL.Path)
			fmt.Fprintf(w, `{"data":{"login":{"identityToken":"identity","expiresAt":%q}}}`, expires)
		case strings.Contains(body.Query, "selectTenant("):
			assert.Equal(t, "/api/user-management/graphql", r.URL.Path)
			fmt.Fprintf(w, `{"data":{"selectTenant":{"accessToken":"tenant-access","expiresAt":%q}}}`, expires)
		default:
			f.calls++
			assert.Equal(t, "/api/device-management/graphql", r.URL.Path)
			assert.Equal(t, "Bearer tenant-access", r.Header.Get("Authorization"))
			if f.respond == nil {
				t.Errorf("unexpected operation: %s", body.Query)
				return
			}
			fmt.Fprint(w, f.respond(body.Query, body.Variables))
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func executeRelationship(args ...string) (string, error) {
	c := newRelationshipCmd()
	var out, stderr bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&stderr)
	c.SetArgs(args)
	err := c.Execute()
	return out.String(), err
}

func (f *relationshipFixture) credentials() []string {
	return []string{"--server", strings.TrimPrefix(f.server.URL, "http://"), "--tenant", "acme", "--email", "ops@example.com", "--password", "pw"}
}

func relationshipAddArgs() []string {
	return []string{"add", "edge-1", "--source-type", "device", "--source", "meter-1",
		"--target-type", "asset", "--target", "pump-1", "--relationship-type", "feeds"}
}

func TestRelationshipRequestsAndOutput(t *testing.T) {
	for _, action := range []string{"add", "list", "remove"} {
		for _, output := range []string{"table", "--json", "--output=json"} {
			t.Run(action+"/"+output, func(t *testing.T) {
				f := newRelationshipFixture(t)
				var args []string
				var wantJSON string
				switch action {
				case "add":
					args = append(relationshipAddArgs(), "--metadata", "site A")
					wantJSON = relationshipFixtureRow
					f.respond = func(q string, v map[string]any) string {
						assert.Contains(t, q, "createEntityRelationship(request:$request)")
						assert.Equal(t, map[string]any{"request": map[string]any{
							"token": "edge-1", "sourceType": "device", "source": "meter-1",
							"targetType": "asset", "target": "pump-1", "relationshipType": "feeds", "metadata": "site A",
						}}, v)
						return `{"data":{"createEntityRelationship":` + relationshipFixtureRow + `}}`
					}
				case "list":
					args = []string{"list", "--page-number", "2", "--page-size", "7", "--source-type", "device", "--source", "meter-1", "--target-type", "asset", "--target", "pump-1", "--relationship-type", "feeds", "--tracked=false"}
					wantJSON = relationshipFixturePage
					f.respond = func(q string, v map[string]any) string {
						assert.Contains(t, q, "entityRelationships(criteria:$criteria)")
						assert.Equal(t, map[string]any{"criteria": map[string]any{
							"pageNumber": float64(2), "pageSize": float64(7), "sourceType": "device", "source": "meter-1",
							"targetType": "asset", "target": "pump-1", "relationshipType": "feeds", "tracked": false,
						}}, v)
						return `{"data":{"entityRelationships":` + relationshipFixturePage + `}}`
					}
				case "remove":
					args = []string{"remove", "edge-1", "edge-2"}
					wantJSON = `{"removed":true}`
					f.respond = func(q string, v map[string]any) string {
						assert.Contains(t, q, "removeEntityRelationships(tokens:$tokens)")
						assert.Equal(t, map[string]any{"tokens": []any{"edge-1", "edge-2"}}, v)
						return `{"data":{"removeEntityRelationships":true}}`
					}
				}
				if output != "table" {
					args = append(args, output)
				}
				stdout, err := executeRelationship(append(args, f.credentials()...)...)
				require.NoError(t, err)
				assert.Equal(t, 1, f.calls)
				assert.Equal(t, 1, f.discoveries, "must negotiate the operation endpoint using the session factory")
				if output != "table" {
					assert.JSONEq(t, wantJSON, stdout)
				} else if action == "remove" {
					assert.Contains(t, stdout, "Removed relationships: edge-1, edge-2")
				} else {
					for _, s := range []string{"TOKEN", "edge-1", "device", "meter-1", "asset", "pump-1", "feeds"} {
						assert.Contains(t, stdout, s)
					}
					if action == "list" {
						assert.Contains(t, stdout, "Page 2 (size 7); 12 total relationships.")
					}
				}
			})
		}
	}
}

func TestRelationshipOptionalFieldsAndEmptyResults(t *testing.T) {
	for _, tracked := range []string{"", "--tracked=true"} {
		f := newRelationshipFixture(t)
		f.respond = func(q string, v map[string]any) string {
			criteria := map[string]any{"pageNumber": float64(1), "pageSize": float64(100)}
			if tracked != "" {
				criteria["tracked"] = true
			}
			assert.Equal(t, map[string]any{"criteria": criteria}, v)
			return `{"data":{"entityRelationships":{"results":[],"pagination":{"pageStart":1,"pageEnd":0,"totalRecords":0}}}}`
		}
		args := []string{"list", "--json"}
		if tracked != "" {
			args = append(args, tracked)
		}
		out, err := executeRelationship(append(args, f.credentials()...)...)
		require.NoError(t, err)
		assert.JSONEq(t, `{"results":[],"pagination":{"pageStart":1,"pageEnd":0,"totalRecords":0}}`, out)
	}
	f := newRelationshipFixture(t)
	f.respond = func(q string, v map[string]any) string {
		assert.NotContains(t, v["request"].(map[string]any), "metadata")
		return `{"data":{"createEntityRelationship":` + relationshipFixtureRow + `}}`
	}
	_, err := executeRelationship(append(relationshipAddArgs(), f.credentials()...)...)
	require.NoError(t, err)
	for _, output := range []string{"--json", "--output=table"} {
		f.respond = func(string, map[string]any) string { return `{"data":{"removeEntityRelationships":false}}` }
		out, err := executeRelationship(append([]string{"remove", "missing", output}, f.credentials()...)...)
		require.NoError(t, err)
		if output == "--json" {
			assert.JSONEq(t, `{"removed":false}`, out)
		} else {
			assert.Contains(t, out, "nothing removed")
		}
	}
}

func TestRelationshipRefusedRequestsFailWithoutSuccessOutput(t *testing.T) {
	for _, args := range [][]string{relationshipAddArgs(), {"list"}, {"remove", "edge-1"}} {
		for _, output := range []string{"--json", "--output=table"} {
			t.Run(args[0]+output, func(t *testing.T) {
				f := newRelationshipFixture(t)
				f.respond = func(string, map[string]any) string {
					return `{"errors":[{"message":"relationship request refused","extensions":{"code":"FORBIDDEN"}}]}`
				}
				argv := append(append([]string{}, args...), output)
				out, err := executeRelationship(append(argv, f.credentials()...)...)
				require.ErrorContains(t, err, "relationship request refused")
				assert.Equal(t, 1, exitCode(err))
				assert.Empty(t, out)
				assert.Equal(t, 1, f.calls, "a refusal must not be retried")
			})
		}
	}
}

func TestRelationshipValidationHappensBeforeAuthentication(t *testing.T) {
	f := newRelationshipFixture(t)
	cases := [][]string{
		{"add"}, {"remove"}, {"list", "unexpected"}, {"remove", " "},
		{"list", "--output=yaml"}, {"list", "--json", "--output=table"},
		{"list", "--page-number=0"}, {"list", "--page-size=-1"},
		{"list", "--source=meter-1"}, {"list", "--target=pump-1"},
		{"list", "--tenant= "}, {"list", "--email= "}, {"list", "--password= "},
	}
	for _, flag := range []string{"source-type", "source", "target-type", "target", "relationship-type"} {
		cases = append(cases, append(relationshipAddArgs(), "--"+flag+"= "))
	}
	for _, args := range cases {
		// Later flags override credentials for the missing-credential cases.
		argv := append([]string{args[0]}, f.credentials()...)
		argv = append(argv, args[1:]...)
		out, err := executeRelationship(argv...)
		require.Error(t, err, "%v", args)
		assert.Empty(t, out)
	}
	assert.Zero(t, f.logins)
	assert.Zero(t, f.calls)
}

func TestRelationshipInstanceAndRecordedSkew(t *testing.T) {
	oldTarget, oldBinding, oldRead := readRecordedTarget, resolveTargetBinding, readTargetInstanceCR
	t.Cleanup(func() {
		readRecordedTarget, resolveTargetBinding, readTargetInstanceCR = oldTarget, oldBinding, oldRead
	})
	for _, version := range []string{"v1.8.2", ""} {
		t.Run(version, func(t *testing.T) {
			f := newRelationshipFixture(t)
			f.discoveryStatus = http.StatusNotFound
			f.respond = func(string, map[string]any) string { return `{"data":{"removeEntityRelationships":true}}` }
			host := strings.TrimPrefix(f.server.URL, "http://")
			targetReads := 0
			readRecordedTarget = func(_ context.Context, _, instance string) (string, bool, error) {
				assert.Equal(t, "prod", instance)
				targetReads++
				return host, false, nil
			}
			resolveTargetBinding = func(bootstrap.Options) (bootstrap.ClusterBinding, bootstrap.BindingSource) {
				return bootstrap.ClusterBinding{KubeContext: "recorded"}, bootstrap.BindingFromRecord
			}
			readTargetInstanceCR = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
				return &dcv1beta1.Instance{Spec: dcv1beta1.InstanceSpec{Host: host, ImageVersion: version}}, nil
			}
			args := []string{"remove", "edge-1", "--instance", "prod", "--tenant", "acme", "--email", "ops@example.com", "--password", "pw"}
			out, err := executeRelationship(args...)
			assert.Equal(t, 1, targetReads)
			assert.Equal(t, 1, f.discoveries)
			if version == "" {
				require.Error(t, err)
				assert.Empty(t, out)
				assert.Zero(t, f.calls, "unknown schema must refuse before dispatch")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 1, f.calls, "recorded release must be registered for device-management")
			_, err = executeRelationship(append(args, "--server", host)...)
			require.NoError(t, err)
			assert.Equal(t, 1, targetReads, "explicit --server overrides --instance")
		})
	}
}

func TestRelationshipHandlesOTP(t *testing.T) {
	withOTPSeams(t, false, "")
	s := &mfaServer{}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	args := []string{"list", "--server", strings.TrimPrefix(srv.URL, "http://"), "--tenant", "acme", "--email", "ops@example.com", "--password", "pw"}
	_, err := executeRelationship(args...)
	require.ErrorContains(t, err, "--otp")
	assert.Equal(t, 1, s.logins)
	assert.Empty(t, s.verified)
	_, _ = executeRelationship(append(args, "--otp", "987654")...)
	assert.Equal(t, []string{"987654"}, s.verified)
}

// Negative control: this direct reader also pins the CI reader declaration.
func TestRelationshipServedSchemaRejectsAnInventedField(t *testing.T) {
	sdl, served, err := schemaplane.SDLAt(filepath.Join("..", "..", "services", "device-management", "graphql"), schemaplane.MountTenant)
	require.NoError(t, err)
	require.True(t, served)
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	require.NoError(t, err)
	vars := map[string]any{"request": map[string]any{
		"token": "edge-1", "sourceType": "device", "source": "meter-1",
		"targetType": "asset", "target": "pump-1", "relationshipType": "feeds",
	}}
	require.Empty(t, schema.ValidateWithVariables(relationshipAddMutation, vars))
	bad := strings.Replace(relationshipAddMutation, "sourceType", "inventedSourceType", 1)
	errs := schema.ValidateWithVariables(bad, vars)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Message, "inventedSourceType")
}
