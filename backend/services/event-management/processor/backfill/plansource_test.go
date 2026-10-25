// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/svcclient"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The plan document the start sends is validated against the schema device-management serves, for
// the reason the roster's is: the compiler does not read it, and a door that renamed a field would
// otherwise fail only when somebody starts a backfill.
//
// RUN WITH -count=1 AFTER TOUCHING THAT SCHEMA: the SDL lives outside this module and Go's test
// cache does not track it.
func TestPlanQueryIsValidAgainstTheServedSchema(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "device-management", "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	require.NoError(t, err)
	require.True(t, served)
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	require.NoError(t, err)
	for _, e := range schema.Validate(planQuery) {
		if e.Rule == "VariablesOfCorrectType" {
			continue
		}
		t.Errorf("the plan query is invalid against the served schema: %v", e)
	}
	assert.NotEmpty(t, schema.Validate(`query { calculatedMetricPlan(profileToken: "p") { nope } }`), "the check can fail")
}

// planServer answers every query with data, or with GraphQL errors carrying codes when errs is set.
func planServer(t *testing.T, data any, errs []map[string]any) (string, *svcclient.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == auth.ServiceTokenPath {
			_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "t", ExpiresAt: 1 << 40})
			return
		}
		if errs != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": errs, "data": nil})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	host, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return srv.URL, svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port), InternalPort: uint32(port)}, "s",
		"event-management", []string{string(auth.DeviceRead)})
}

func codedError(msg, code string) []map[string]any {
	return []map[string]any{{"message": msg, "extensions": map[string]any{"code": code}}}
}

const doorPlan = `{"versionToken":"meter@2","version":2,"activeSince":null,
 "measured":[{"key":"voltage","dataType":"DOUBLE"}],
 "calculated":[{"key":"power","expression":"voltage * 2.0","dataType":"DOUBLE","minValue":null,"maxValue":null,
   "unit":"W","classifier":7,"newInVersion":true,"changedSinceEarlier":false}],
 "constantKeys":[]}`

func TestPlanSourceReturnsTheDoorsDocumentVerbatim(t *testing.T) {
	url, client := planServer(t, map[string]any{"calculatedMetricPlan": json.RawMessage(doorPlan)}, nil)
	v := 2
	doc, raw, err := NewGraphQLPlanSource(client, url).Plan(context.Background(), "acme", "meter", &v)
	require.NoError(t, err)
	assert.Equal(t, "meter@2", doc.VersionToken)
	require.Len(t, doc.Calculated, 1)
	assert.True(t, doc.Calculated[0].NewInVersion)
	assert.JSONEq(t, doorPlan, string(raw), "what a job freezes is what the door said")
	// And the frozen document is one the worker can read back.
	_, err = model.ParseCalculatedPlanDoc(raw)
	require.NoError(t, err)
}

func TestPlanSourceMapsDoorFailuresToTheirCodes(t *testing.T) {
	ctx := context.Background()

	url, client := planServer(t, nil, codedError("no such profile", "NOT_FOUND"))
	_, _, err := NewGraphQLPlanSource(client, url).Plan(ctx, "acme", "ghost", nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "NOT_FOUND stays NOT_FOUND")

	url, client = planServer(t, map[string]any{"calculatedMetricPlan": nil}, nil)
	_, _, err = NewGraphQLPlanSource(client, url).Plan(ctx, "acme", "ghost", nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "a null plan is not an empty plan")

	url, client = planServer(t, nil, codedError(`"d9" is not a device of the profile`, "INVALID_VALUE"))
	err = NewGraphQLPlanSource(client, url).CheckDevices(ctx, "acme", "meter", []string{"d9"})
	class, ok := integrity.Refused(err)
	require.True(t, ok, "an unknown device is a refusal of the request")
	assert.Equal(t, integrity.ClassInvalid, class)
	assert.Contains(t, err.Error(), `"d9"`)

	url, client = planServer(t, nil, codedError("plan cannot be built", "INTERNAL"))
	_, _, err = NewGraphQLPlanSource(client, url).Plan(ctx, "acme", "meter", nil)
	require.Error(t, err)
	var un *model.BackfillUnavailable
	assert.NotErrorAs(t, err, &un, "a refusal from device-management is not 'unreachable'")

	// Nothing listening: device-management is down, which is UNAVAILABLE.
	down := svcclient.New(config.UserManagementConfiguration{Hostname: "127.0.0.1", Port: 1, InternalPort: 1}, "s", "event-management", nil)
	_, _, err = NewGraphQLPlanSource(down, "http://127.0.0.1:1/graphql").Plan(ctx, "acme", "meter", nil)
	require.ErrorAs(t, err, &un)
}
