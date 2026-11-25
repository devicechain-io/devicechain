// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/apiclient"
	graphql "github.com/graph-gophers/graphql-go"
)

func TestProductApplyStampsRuleDoor(t *testing.T) {
	b := fixture(t)
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	f := newFake(t)
	apply(t, f, dir, env(defaultEnv))
	for i := range b.Profiles {
		if len(b.Profiles[i].Rules) > 0 {
			b.Profiles[i].Rules[0].Name = "Revised"
		}
	}
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	apply(t, f, dir, env(defaultEnv))
	seen := map[string]bool{}
	for _, call := range f.calls {
		for _, op := range []string{"ProductCreateRule", "ProductUpdateRule"} {
			if !strings.Contains(call.query, op+"(") {
				continue
			}
			seen[op] = true
			input := call.vars["request"].(map[string]any)
			if input["authoredVia"] != "PRODUCT_AS_CODE" {
				t.Errorf("%s authoredVia = %v", op, input["authoredVia"])
			}
		}
	}
	if len(seen) != 2 {
		t.Fatalf("did not exercise create and update: %v", seen)
	}
	// Authoring history is observational; it must not manufacture product drift.
	if p := plan(t, f, dir, env(defaultEnv)); !p.Empty() {
		t.Fatal("unchanged product has drift")
	}
}

// olderInstance negotiates rule saves against a device-management schema released
// before authoredVia existed, as userclient's schema negotiation does: the request
// is validated and refused before anything is sent.
type olderInstance struct {
	*fakeTenant
	schema  *graphql.Schema
	refused int
}

func (o *olderInstance) Query(ctx context.Context, endpoint, query string, vars map[string]any, out any) error {
	if strings.Contains(query, "ProductCreateRule(") || strings.Contains(query, "ProductUpdateRule(") {
		raw, _ := json.Marshal(vars)
		var wire map[string]any
		_ = json.Unmarshal(raw, &wire)
		if errs := o.schema.ValidateWithVariables(query, wire); len(errs) > 0 {
			o.refused++
			messages := []string{}
			for _, e := range errs {
				messages = append(messages, e.Message)
			}
			return &userclient.UnsupportedFeatureError{Detail: strings.Join(messages, "; ")}
		}
	}
	return o.fakeTenant.Query(ctx, endpoint, query, vars, out)
}

// A dcctl that records the door must still apply rules to an instance that cannot
// store it, rather than refusing the whole apply as an unsupported feature.
func TestProductApplyRuleDoorOnOlderInstance(t *testing.T) {
	sdl := apiclient.LegacySchema("https://instance.example/api/device-management/graphql")
	if sdl == "" || strings.Contains(sdl, "authoredVia") {
		t.Fatal("the legacy device-management contract must predate authoredVia")
	}
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseStringDescriptions())
	if err != nil {
		t.Fatal(err)
	}
	b := fixture(t)
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	f := newFake(t)
	older := &olderInstance{fakeTenant: f, schema: schema}
	applyWith(t, older, dir, env(defaultEnv))
	for i := range b.Profiles {
		if len(b.Profiles[i].Rules) > 0 {
			b.Profiles[i].Rules[0].Name = "Revised"
		}
	}
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	applyWith(t, older, dir, env(defaultEnv))
	saved := map[string]bool{}
	for _, call := range f.calls {
		for _, op := range []string{"ProductCreateRule", "ProductUpdateRule"} {
			if !strings.Contains(call.query, op+"(") {
				continue
			}
			if _, has := call.vars["request"].(map[string]any)["authoredVia"]; !has {
				saved[op] = true
			}
		}
	}
	if older.refused == 0 || !saved["ProductCreateRule"] || !saved["ProductUpdateRule"] {
		t.Fatalf("refused %d, saved without the door %v", older.refused, saved)
	}
	if p := planWith(t, older, dir, env(defaultEnv)); !p.Empty() {
		t.Fatal("unchanged product has drift")
	}
}

// Only the door is dropped: a refusal of anything else is still the caller's answer.
func TestProductApplyRuleDoorRetryIsNarrow(t *testing.T) {
	calls := 0
	pl := &planner{c: clientFunc(func(vars map[string]any) error {
		calls++
		return &userclient.UnsupportedFeatureError{Detail: `In field "scope": Unknown field.`}
	}), ep: ep()}
	err := pl.sendRule(context.Background(), mCreateRule, map[string]any{"request": map[string]any{"authoredVia": ruleDoor}})
	var unsupported *userclient.UnsupportedFeatureError
	if !errors.As(err, &unsupported) || calls != 1 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}

type clientFunc func(vars map[string]any) error

func (c clientFunc) Query(_ context.Context, _, _ string, vars map[string]any, _ any) error {
	return c(vars)
}

func applyWith(t *testing.T, c Client, dir string, o Options) {
	t.Helper()
	p := planWith(t, c, dir, o)
	if err := p.Apply(context.Background(), &bytes.Buffer{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

func planWith(t *testing.T, c Client, dir string, o Options) *Plan {
	t.Helper()
	b, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanBundle(context.Background(), c, ep(), b, o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
