// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

// The four documents this package sends are STRINGS the compiler does not read, validated by a
// schema in another module. So when a field is dropped or renamed there, nothing here would fail
// to compile and every test with a faked backend would pass; the route would fail at runtime. This
// asks the schema, with the server's own validator.
//
// 🔴 RUN WITH -count=1 AFTER TOUCHING A SCHEMA: the SDL lives outside this module and Go's test
// cache does not track it.

func deviceManagementSchema(t *testing.T) *graphql.Schema {
	t.Helper()
	dir := filepath.Join("..", "..", "device-management", "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	require.NoError(t, err)
	require.True(t, served, "device-management serves no schema at the tenant mount; a test that parsed nothing would validate everything")
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	require.NoError(t, err)
	return schema
}

// validate validates doc with no variable VALUES; the one rule dropped is VariablesOfCorrectType,
// which graphql-go raises for a required variable given no value.
func validate(schema *graphql.Schema, doc string) []string {
	var out []string
	for _, e := range schema.Validate(doc) {
		if e.Rule != "VariablesOfCorrectType" {
			out = append(out, e.Message+" ["+e.Rule+"]")
		}
	}
	return out
}

func TestEveryDocumentValidatesAgainstDeviceManagementsServedSchema(t *testing.T) {
	schema := deviceManagementSchema(t)
	docs := map[string]string{
		"integrationAuth": authQuery, "resolveIntegrationDevice": resolveMutation,
		"recordIntegrationUndecoded": undecodedMutation, "recordIntegrationAuthFailure": authFailureMutation,
	}
	for name, doc := range docs {
		require.Contains(t, doc, name)
		require.Empty(t, validate(schema, doc), name)
	}
	// The filter must not swallow a real defect.
	bad := `query($token: String!, $secret: String!) { integrationAuth(token: $token, secret: $secret) { noSuchField } }`
	errs := validate(schema, bad)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0], "FieldsOnCorrectTypeRule")
}

// stub answers the backend's GraphQL calls from a script and records what was sent.
type stub struct {
	srv      *httptest.Server
	response string
	got      struct {
		query     string
		variables map[string]any
		tenant    string
	}
}

func newStub(t *testing.T) *stub {
	s := &stub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		s.got.query, s.got.variables, s.got.tenant = req.Query, req.Variables, r.Header.Get("X-Tenant")
		_, _ = w.Write([]byte(s.response))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// Query is the slice of svcclient.Client the backend uses, over plain HTTP, with a GraphQL error
// array turned into an error as svcclient does.
func (s *stub) Query(ctx context.Context, baseURL, tenant, query string, variables map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	req.Header.Set("X-Tenant", tenant)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if len(env.Errors) > 0 {
		return errors.New(env.Errors[0].Message)
	}
	return json.Unmarshal(env.Data, out)
}

// THE OUTCOME MAPPING, over the wire: each answer device-management can give becomes exactly one
// Outcome, and anything this build does not understand is an error and never an outcome.
func TestResolveMapsEveryOutcomeAndRefusesWhatItDoesNotUnderstand(t *testing.T) {
	s := newStub(t)
	dm := NewDM(s, s.srv.URL)
	resolve := func(resp string) (Resolution, error) {
		s.response = resp
		return dm.Resolve(context.Background(), "acme", "cs", "tok", "lorawan.deveui", "0101010101010101")
	}

	got, err := resolve(`{"data":{"resolveIntegrationDevice":{"outcome":"RESOLVED","deviceToken":"pump-1","deviceTypeToken":"pump","overflow":false}}}`)
	require.NoError(t, err)
	require.Equal(t, Resolution{Outcome: Resolved, DeviceToken: "pump-1", DeviceTypeToken: "pump"}, got)
	require.Equal(t, resolveMutation, s.got.query)
	require.Equal(t, map[string]any{"integration": "cs", "secret": "tok", "scheme": "lorawan.deveui", "value": "0101010101010101"}, s.got.variables)
	require.Equal(t, "acme", s.got.tenant)

	got, err = resolve(`{"data":{"resolveIntegrationDevice":{"outcome":"UNREGISTERED","deviceToken":null,"deviceTypeToken":null,"overflow":true}}}`)
	require.NoError(t, err)
	require.Equal(t, Resolution{Outcome: Unregistered, Overflow: true}, got)

	got, err = resolve(`{"data":{"resolveIntegrationDevice":{"outcome":"DISABLED","deviceToken":null,"deviceTypeToken":null,"overflow":false}}}`)
	require.NoError(t, err)
	require.Equal(t, Disabled, got.Outcome)

	got, err = resolve(`{"data":{"resolveIntegrationDevice":{"outcome":"UNAUTHENTICATED","deviceToken":null,"deviceTypeToken":null,"overflow":false}}}`)
	require.NoError(t, err)
	require.Equal(t, Unauthenticated, got.Outcome)

	for name, resp := range map[string]string{
		"an outcome added later":        `{"data":{"resolveIntegrationDevice":{"outcome":"MAYBE","overflow":false}}}`,
		"a resolved device naming none": `{"data":{"resolveIntegrationDevice":{"outcome":"RESOLVED","deviceToken":"","overflow":false}}}`,
		"no resolution at all":          `{"data":{"resolveIntegrationDevice":null}}`,
		"an old schema":                 `{"errors":[{"message":"Cannot query field \"resolveIntegrationDevice\" on type \"Mutation\"."}]}`,
		"not JSON":                      `<html>`,
	} {
		_, err := resolve(resp)
		require.Error(t, err, name)
	}
}

func TestAuthAnswersNilForAnUnknownTokenAndADescriptorOtherwise(t *testing.T) {
	s := newStub(t)
	dm := NewDM(s, s.srv.URL)

	s.response = `{"data":{"integrationAuth":null}}`
	d, err := dm.Auth(context.Background(), "acme", "cs", "wrong")
	require.NoError(t, err)
	require.Nil(t, d, "a wrong token and an unknown integration are one answer")
	require.Equal(t, authQuery, s.got.query)
	require.Equal(t, map[string]any{"token": "cs", "secret": "wrong"}, s.got.variables)

	s.response = `{"data":{"integrationAuth":{"token":"cs","type":"CHIRPSTACK_V4","enabled":false,"identity":"DEV_EUI","generic":null,"linkMetrics":true}}}`
	d, err = dm.Auth(context.Background(), "acme", "cs", "right")
	require.NoError(t, err)
	require.Equal(t, ChirpStackV4, d.Type)
	require.False(t, d.Enabled, "a switched-off integration comes back with enabled false")
	require.True(t, d.LinkMetrics)

	s.response = `{"data":{"integrationAuth":{"token":"g","type":"GENERIC_JSON","enabled":true,"identity":"ALIAS:custom.pump","generic":"{\"idFrom\":\"pointer:/id\",\"itemsPointer\":\"/rows\"}","linkMetrics":false}}}`
	d, err = dm.Auth(context.Background(), "acme", "g", "right")
	require.NoError(t, err)
	require.Equal(t, "/rows", d.Settings().ItemsPointer)

	// A descriptor this build cannot use is an error, not an integration to guess at.
	for name, resp := range map[string]string{
		"an unknown type":          `{"data":{"integrationAuth":{"token":"x","type":"MODBUS","enabled":true,"identity":"DEV_EUI"}}}`,
		"an unknown identity":      `{"data":{"integrationAuth":{"token":"x","type":"CHIRPSTACK_V4","enabled":true,"identity":"SERIAL"}}}`,
		"generic without settings": `{"data":{"integrationAuth":{"token":"x","type":"GENERIC_JSON","enabled":true,"identity":"ALIAS:custom.pump","generic":null}}}`,
		"a server error":           `{"errors":[{"message":"boom"}]}`,
	} {
		s.response = resp
		_, err := dm.Auth(context.Background(), "acme", "x", "right")
		require.Error(t, err, name)
	}
}

func TestTheMarkerMutationsSendTheirVariables(t *testing.T) {
	s := newStub(t)
	dm := NewDM(s, s.srv.URL)

	s.response = `{"data":{"recordIntegrationUndecoded":true}}`
	require.NoError(t, dm.RecordUndecoded(context.Background(), "acme", "cs", "tok", 3, "aabb"))
	require.Equal(t, undecodedMutation, s.got.query)
	require.EqualValues(t, map[string]any{"integration": "cs", "secret": "tok", "count": float64(3), "sample": "aabb"}, s.got.variables)

	require.NoError(t, dm.RecordUndecoded(context.Background(), "acme", "cs", "tok", 1, ""))
	require.NotContains(t, s.got.variables, "sample", "no frame, no sample")

	s.response = `{"data":{"recordIntegrationAuthFailure":true}}`
	require.NoError(t, dm.RecordAuthFailure(context.Background(), "acme", "cs"))
	require.Equal(t, authFailureMutation, s.got.query)
	require.Equal(t, map[string]any{"integration": "cs"}, s.got.variables)

	s.response = `{"errors":[{"message":"forbidden"}]}`
	require.Error(t, dm.RecordUndecoded(context.Background(), "acme", "cs", "tok", 1, ""))
}
