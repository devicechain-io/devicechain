// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
)

// EVERY DOCUMENT THIS PACKAGE SENDS IS A STRING, and the schemas they must satisfy live in
// other modules. The fake tenant answers canned shapes and never reads a schema, so without
// this file nothing but a real cluster would notice a misspelled field or input key.
//
// The documents and variables validated here are RECORDED from the real code paths (a full
// seed, an edit that touches every kind, a removal, a move, a rotation, an export), not written
// out again, so what is checked is the exact request the server would receive. The variables
// matter: graphql-go rejects an input-object entry the schema does not define when it arrives
// through a variable.
//
// The SDL lives outside this module and Go's test cache does not track it; CI runs -count=1.

func servedSchema(t *testing.T, area string) *graphql.Schema {
	t.Helper()
	dir := filepath.Join("..", "..", "services", area, "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil {
		t.Fatalf("classify %s: %v", dir, err)
	}
	if !served {
		t.Fatalf("no tenant-plane schema under %s", dir)
	}
	s, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers(), graphql.MaxDepth(15), graphql.MaxQueryLength(100000))
	if err != nil {
		t.Fatalf("parse %s: %v", area, err)
	}
	return s
}

var areaOf = map[string]string{"dm": "device-management", "oc": "outbound-connectors", "dash": "dashboard-management"}

// recordEverything drives every code path that sends a document and returns what it sent.
func recordEverything(t *testing.T) []recordedCall {
	t.Helper()
	f, dir := seeded(t)
	// An edit touching every family, so each update document is sent with real variables.
	editBundle(t, dir, func(b *Bundle) {
		b.Connectors[0].Name = "n"
		b.Connectors[0].Config = Doc(`{"urls":["tcp://b2.example.com:1883"],"topic":"t2"}`)
		b.Profiles[1].Name = "p"
		b.Profiles[1].Location = nil
		b.Profiles[1].ConfigurationDeclaration = nil
		b.Profiles[1].Decoder = nil
		b.Profiles[1].Metrics[0].Unit = "K"
		b.Profiles[1].CalculatedMetrics[0].Expression = "temp-c + 1"
		b.Profiles[1].Commands[0].Impact = "NORMAL"
		b.Profiles[1].Rules[0].Enabled = false
		b.Profiles[0].Metrics = append(b.Profiles[0].Metrics, Metric{Token: "m-new", Key: "new", DataType: "INT"})
		b.DeviceTypes[0].Name = "dt"
		b.Integrations[0].Name = "i"
		b.Integrations[0].DeviceTypes = []string{"thermo-v1", "meter-v1"}
		b.Dashboards[0].Name = "d"
		b.Dashboards[0].Definition = Doc(`{"widgets":[]}`)
	})
	o := env(defaultEnv)
	o.RotateSecrets = true
	apply(t, f, dir, o)
	// A removal and a move, and the clearing of a decoder set again.
	editBundle(t, dir, func(b *Bundle) {
		b.Profiles[1].Metrics = b.Profiles[1].Metrics[:0]
		b.Profiles[1].CalculatedMetrics = nil
		b.Profiles[1].Commands = nil
		b.Profiles[1].Rules = nil
		b.Profiles[1].Decoder = Doc(`{"kind":"CEL","envVersion":1,"input":"FRAME"}`)
		var keep []Metric
		for _, m := range b.Profiles[0].Metrics {
			if m.Token != "m-kwh" {
				keep = append(keep, m)
			}
		}
		b.Profiles[0].Metrics = keep
		b.Profiles[1].Metrics = append(b.Profiles[1].Metrics, Metric{Token: "m-kwh", Key: "kwh", DataType: "DOUBLE"})
	})
	apply(t, f, dir, o)
	out := filepath.Join(t.TempDir(), "out")
	export(t, f, out)
	if _, err := Export(context.Background(), f, ep(), mustKinds(t, "connectors"), false, filepath.Join(t.TempDir(), "c"), ""); err != nil {
		t.Fatal(err)
	}
	return f.calls
}

func TestEveryDocumentValidatesAgainstTheServedSchemas(t *testing.T) {
	schemas := map[string]*graphql.Schema{}
	for _, a := range areaOf {
		schemas[a] = servedSchema(t, a)
	}
	calls := recordEverything(t)
	seen := map[string]bool{}
	for _, c := range calls {
		area, ok := areaOf[c.endpoint]
		if !ok {
			t.Fatalf("unknown endpoint %q", c.endpoint)
		}
		raw, _ := json.Marshal(c.vars)
		var vars map[string]any
		if err := json.Unmarshal(raw, &vars); err != nil {
			t.Fatal(err)
		}
		seen[c.query] = true
		if errs := schemas[area].ValidateWithVariables(c.query, vars); len(errs) > 0 {
			t.Errorf("%s\ndoes not validate against %s with %s: %v", c.query, area, raw, errs)
		}
	}
	// Every document the package declares was sent by some path under test, so none is
	// validated by omission.
	for _, d := range allDocuments {
		if !seen[d] {
			t.Errorf("a declared document is never sent by the code paths under test:\n%s", d)
		}
	}
}

var allDocuments = []string{
	qDeviceTypes, qProfiles, qIntegrations, qConnectors, qConnectorVersions, qDashboards, qDashboard, qPublishedDashboard,
	mCreateDeviceType, mUpdateDeviceType, mCreateProfile, mUpdateProfile, mSetConfigDecl, mSetDecoder, mClearDecoder,
	mPublishProfile, mCreateMetric, mUpdateMetric, mDeleteMetric, mCreateCommand, mUpdateCommand, mDeleteCommand,
	mCreateRule, mUpdateRule, mDeleteRule, mCreateIntegration, mUpdateIntegration, mCreateConnector, mUpdateConnector,
	mPublishConnector, mCreateDashboard, mUpdateDashboard, mPublishDashboard,
}

// NEGATIVE CONTROL: the validator must reject a misnamed field and a misnamed input key, and
// the rejection must be about that name.
func TestValidatorRejectsMisnamedFieldsAndVariables(t *testing.T) {
	dm := servedSchema(t, "device-management")
	bad := strings.Replace(qDeviceTypes, "maintenanceWindow{token}", "maintenanceWindowX{token}", 1)
	vars := map[string]any{"criteria": map[string]any{"pageNumber": 1, "pageSize": 5}}
	if errs := dm.ValidateWithVariables(bad, vars); len(errs) == 0 || !strings.Contains(errs[0].Message, "maintenanceWindowX") {
		t.Errorf("a misnamed selection must be rejected by name, got %v", errs)
	}
	req := map[string]any{"token": "t", "metricKeyX": "k", "deviceProfileToken": "p", "dataType": "INT"}
	errs := dm.ValidateWithVariables(mCreateMetric, map[string]any{"request": req})
	if len(errs) == 0 || !strings.Contains(strings.Join(errStrings(errs), " "), "metricKeyX") {
		t.Errorf("a misnamed input key must be rejected by name, got %v", errs)
	}
}

func errStrings(errs []*gqlerrors.QueryError) []string {
	var out []string
	for _, e := range errs {
		out = append(out, e.Message)
	}
	return out
}
