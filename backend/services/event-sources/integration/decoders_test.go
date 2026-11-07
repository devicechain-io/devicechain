// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/payloaddecode/plancost"
	"github.com/devicechain-io/dc-microservice/svcclient"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

// The decoder-plan cache, and the one error it must treat as an answer: the validation error of a
// device-management that predates the query.

type decoderFake struct {
	calls atomic.Int64
	plan  *DecoderPlan
	err   error
	// gate, when set, holds every call until it is closed.
	gate chan struct{}
}

func (f *decoderFake) Decoder(context.Context, string, string, string, string) (*DecoderPlan, error) {
	f.calls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	return f.plan, f.err
}

func TestAPresentAndAnAbsentPlanAreEachAskedOnceAMinute(t *testing.T) {
	clk := newClock()
	f := &decoderFake{plan: &DecoderPlan{Present: true, Input: "FRAME", MaxReadings: 4, ProfileVersionToken: "meter@3"}}
	d := NewDecoders(f, DecoderConfig{Now: clk.now})
	ctx := context.Background()

	got, err := d.Lookup(ctx, "acme", "cs", "tok", "meter")
	require.NoError(t, err)
	require.True(t, got.Plan.Present)
	require.False(t, got.Cached)
	require.Equal(t, 4, got.Plan.MaxReadings)

	got, err = d.Lookup(ctx, "acme", "cs", "tok", "meter")
	require.NoError(t, err)
	require.True(t, got.Cached, "a second lookup inside the minute made a call")
	require.EqualValues(t, 1, f.calls.Load())

	// The key is the tenant and the type: another tenant's type of the same name is its own entry.
	_, err = d.Lookup(ctx, "globex", "cs", "tok", "meter")
	require.NoError(t, err)
	require.EqualValues(t, 2, f.calls.Load())

	// A negative answer is remembered as long as a positive one.
	f.plan = &DecoderPlan{}
	got, err = d.Lookup(ctx, "acme", "cs", "tok", "pump")
	require.NoError(t, err)
	require.False(t, got.Plan.Present)
	_, err = d.Lookup(ctx, "acme", "cs", "tok", "pump")
	require.NoError(t, err)
	require.EqualValues(t, 3, f.calls.Load())

	clk.advance(DefaultDecoderTTL + time.Second)
	f.plan = &DecoderPlan{Present: true, MaxReadings: 8, ProfileVersionToken: "meter@4"}
	got, err = d.Lookup(ctx, "acme", "cs", "tok", "meter")
	require.NoError(t, err)
	require.False(t, got.Cached)
	require.Equal(t, 8, got.Plan.MaxReadings, "a plan older than the TTL was served")
	require.Equal(t, "meter@4", got.Plan.ProfileVersionToken)
}

func TestAColdBurstForOneTypeIsOneCall(t *testing.T) {
	f := &decoderFake{plan: &DecoderPlan{Present: true}, gate: make(chan struct{})}
	d := NewDecoders(f, DecoderConfig{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.Lookup(context.Background(), "acme", "cs", "tok", "meter")
			require.NoError(t, err)
		}()
	}
	require.Eventually(t, func() bool { return f.calls.Load() >= 1 }, time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	close(f.gate)
	wg.Wait()
	require.EqualValues(t, 1, f.calls.Load())
}

// A14. An old device-management is "no decoder": remembered, counted, and NOT an error.
func TestAnOldDeviceManagementIsRememberedAsNoDecoderAndCounted(t *testing.T) {
	clk := newClock()
	f := &decoderFake{err: ErrDecoderUnsupported}
	var probes atomic.Int64
	d := NewDecoders(f, DecoderConfig{Now: clk.now, OnUnsupported: func() { probes.Add(1) }})

	got, err := d.Lookup(context.Background(), "acme", "cs", "tok", "meter")
	require.NoError(t, err, "a device-management without the query was treated as an outage")
	require.True(t, got.Unsupported)
	require.False(t, got.Plan.Present)

	got, err = d.Lookup(context.Background(), "acme", "cs", "tok", "meter")
	require.NoError(t, err)
	require.True(t, got.Cached)
	require.True(t, got.Unsupported)
	require.EqualValues(t, 1, f.calls.Load(), "the old device-management was asked on every uplink")
	require.EqualValues(t, 1, probes.Load(), "the probe is counted once per fill")

	// Asked again after the minute, so a finished rollout is noticed.
	clk.advance(DefaultDecoderTTL + time.Second)
	f.err, f.plan = nil, &DecoderPlan{Present: true}
	got, err = d.Lookup(context.Background(), "acme", "cs", "tok", "meter")
	require.NoError(t, err)
	require.True(t, got.Plan.Present)
	require.False(t, got.Unsupported)
}

// Every other failure is an error and is never remembered: not knowing whether a decoder exists
// must not become flattening.
func TestAnyOtherFailureIsAnErrorAndIsNotRemembered(t *testing.T) {
	f := &decoderFake{err: errors.New("device-management is unreachable")}
	d := NewDecoders(f, DecoderConfig{})
	for i := 0; i < 3; i++ {
		_, err := d.Lookup(context.Background(), "acme", "cs", "tok", "meter")
		require.Error(t, err)
	}
	require.EqualValues(t, 3, f.calls.Load())

	// A token device-management stopped accepting is its own error, also not remembered.
	f.err, f.plan = nil, nil
	_, err := d.Lookup(context.Background(), "acme", "cs", "tok", "meter")
	require.ErrorIs(t, err, ErrDecoderUnauthenticated)
	f.plan = &DecoderPlan{Present: true}
	got, err := d.Lookup(context.Background(), "acme", "cs", "tok", "meter")
	require.NoError(t, err)
	require.True(t, got.Plan.Present)
}

func TestTheCacheIsBounded(t *testing.T) {
	f := &decoderFake{plan: &DecoderPlan{}}
	d := NewDecoders(f, DecoderConfig{Entries: 32})
	for i := 0; i < 500; i++ {
		_, err := d.Lookup(context.Background(), "acme", "cs", "tok", "type-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		require.NoError(t, err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	require.LessOrEqual(t, len(d.entries), 32)
}

func TestForgetDropsOnlyThatTenantsPlans(t *testing.T) {
	f := &decoderFake{plan: &DecoderPlan{Present: true}}
	d := NewDecoders(f, DecoderConfig{})
	for _, tenant := range []string{"acme", "globex"} {
		_, err := d.Lookup(context.Background(), tenant, "cs", "tok", "meter")
		require.NoError(t, err)
	}
	d.Forget("acme")
	got, _ := d.Lookup(context.Background(), "globex", "cs", "tok", "meter")
	require.True(t, got.Cached)
	got, _ = d.Lookup(context.Background(), "acme", "cs", "tok", "meter")
	require.False(t, got.Cached)
}

// The error is recognised from the server's OWN validator, not from a string this test wrote.
func validationErrorsOf(t *testing.T, sdl string) *svcclient.GraphQLError {
	t.Helper()
	schema, err := graphql.ParseSchema(sdl, nil)
	require.NoError(t, err)
	e := &svcclient.GraphQLError{URL: "http://dm"}
	for _, ve := range schema.Validate(decoderQuery) {
		if ve.Rule != "VariablesOfCorrectType" {
			e.Messages = append(e.Messages, ve.Message)
		}
	}
	return e
}

func TestOnlyTheValidationErrorOfAnOlderDeviceManagementIsTolerated(t *testing.T) {
	// The release before payload decoders: no such query at all.
	old := validationErrorsOf(t, `schema { query: Query } type Query { integrationAuth: String }`)
	require.NotEmpty(t, old.Messages)
	require.True(t, IsDecoderUnsupported(old), "%v", old.Messages)

	// The release that serves the query without the fields the charge needs can have a decoder:
	// flattening that type's uplinks would store what the decoder was meant to produce, so it is
	// an error (a 503), not "no decoder".
	partial := validationErrorsOf(t, `schema { query: Query }
type Query { integrationDecoder(integration: String!, secret: String!, deviceTypeToken: String!): IntegrationDecoderPlan }
type IntegrationDecoderPlan { present: Boolean! input: String maxReadings: Int! estimatedCost: Int! profileVersionToken: String! }`)
	require.NotEmpty(t, partial.Messages)
	require.False(t, IsDecoderUnsupported(partial), "%v", partial.Messages)

	// The release that has them all validates clean.
	current := validationErrorsOf(t, `schema { query: Query }
type Query { integrationDecoder(integration: String!, secret: String!, deviceTypeToken: String!): IntegrationDecoderPlan }
type IntegrationDecoderPlan { present: Boolean! input: String maxReadings: Int! composedCost: Int! parsesJson: Boolean! parsesDecoded: Boolean! profileVersionToken: String! }`)
	require.Empty(t, current.Messages)

	// Nothing else is an answer.
	for name, err := range map[string]error{
		"nil":                        nil,
		"a transport error":          errors.New("connection refused"),
		"a status error":             &svcclient.StatusError{Status: 503},
		"a different missing field":  &svcclient.GraphQLError{Messages: []string{`Cannot query field "resolveIntegrationDevice" on type "Mutation".`}},
		"a missing argument":         &svcclient.GraphQLError{Messages: []string{`Unknown argument "deviceTypeToken" on field "integrationDecoder" of type "Query".`}},
		"a permission error":         &svcclient.GraphQLError{Messages: []string{"permission denied"}},
		"a mix of ours and not ours": &svcclient.GraphQLError{Messages: []string{old.Messages[0], "permission denied"}},
		"no messages":                &svcclient.GraphQLError{},
	} {
		require.False(t, IsDecoderUnsupported(err), name)
	}
}

func TestTheDecoderQueryAsksForExactlyWhatTheChargeNeeds(t *testing.T) {
	schema := deviceManagementSchema(t)
	require.Empty(t, validate(schema, decoderQuery))
}

func TestDMDecoderReadsThePlanAndMapsTheOldSchemaToUnsupported(t *testing.T) {
	s := newStub(t)
	dm := NewDM(s, s.srv.URL)
	ctx := context.Background()

	s.response = `{"data":{"integrationDecoder":{"present":true,"input":"JSON","maxReadings":8,"composedCost":120,"parsesJson":true,"parsesDecoded":false,"profileVersionToken":"meter@2"}}}`
	p, err := dm.Decoder(ctx, "acme", "cs", "tok", "meter")
	require.NoError(t, err)
	require.Equal(t, &DecoderPlan{
		Present: true, Input: "JSON", MaxReadings: 8, ProfileVersionToken: "meter@2",
		Cost: plancost.Shape{Composed: 120, ParsesJSON: true},
	}, p)
	require.Equal(t, decoderQuery, s.got.query)
	require.Equal(t, map[string]any{"integration": "cs", "secret": "tok", "deviceTypeToken": "meter"}, s.got.variables)

	// A decoder this build cannot name the input of is still present.
	s.response = `{"data":{"integrationDecoder":{"present":true,"input":null,"maxReadings":0,"composedCost":0,"parsesJson":false,"parsesDecoded":false,"profileVersionToken":"meter@2"}}}`
	p, err = dm.Decoder(ctx, "acme", "cs", "tok", "meter")
	require.NoError(t, err)
	require.True(t, p.Present)
	require.Empty(t, p.Input)

	s.response = `{"data":{"integrationDecoder":null}}`
	p, err = dm.Decoder(ctx, "acme", "cs", "tok", "meter")
	require.NoError(t, err)
	require.Nil(t, p, "an authentication failure is a nil plan")

	s.response = `{"errors":[{"message":"Cannot query field \"integrationDecoder\" on type \"Query\"."}]}`
	_, err = dm.Decoder(ctx, "acme", "cs", "tok", "meter")
	require.ErrorIs(t, err, ErrDecoderUnsupported)

	s.response = `{"errors":[{"message":"internal error"}]}`
	_, err = dm.Decoder(ctx, "acme", "cs", "tok", "meter")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrDecoderUnsupported)
}

type perTenantFake struct{ plans map[string]*DecoderPlan }

func (f perTenantFake) Decoder(_ context.Context, tenant, _, _, _ string) (*DecoderPlan, error) {
	return f.plans[tenant], nil
}

// A type token is only unique inside a tenant: tenant A's decoder is never served for tenant B's
// device type of the same name, nor B's absence for A's.
func TestAPlanIsNeverSharedAcrossTenants(t *testing.T) {
	d := NewDecoders(perTenantFake{plans: map[string]*DecoderPlan{
		"acme":   {Present: true, MaxReadings: 8, ProfileVersionToken: "acme-profile@1"},
		"globex": {},
	}}, DecoderConfig{})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		a, err := d.Lookup(ctx, "acme", "cs", "tok", "meter")
		require.NoError(t, err)
		require.True(t, a.Plan.Present)
		require.Equal(t, "acme-profile@1", a.Plan.ProfileVersionToken)
		g, err := d.Lookup(ctx, "globex", "cs", "tok", "meter")
		require.NoError(t, err)
		require.False(t, g.Plan.Present, "tenant A's decoder was served for tenant B's device type")
	}
}

// One integration's view of a type is not another's: DM answers "no decoder" for a type an
// integration's scope no longer reaches, and that answer must not be served to an integration whose
// scope still does.
func TestAnIntegrationsOutOfScopeAnswerIsNotServedToAnother(t *testing.T) {
	f := &decoderFake{plan: &DecoderPlan{}}
	d := NewDecoders(f, DecoderConfig{})
	ctx := context.Background()
	got, err := d.Lookup(ctx, "acme", "cs-a", "tok", "meter")
	require.NoError(t, err)
	require.False(t, got.Plan.Present)

	f.plan = &DecoderPlan{Present: true, ProfileVersionToken: "meter@1"}
	got, err = d.Lookup(ctx, "acme", "cs-b", "tok", "meter")
	require.NoError(t, err)
	require.True(t, got.Plan.Present, "integration A's negative answer was served to integration B")
}

// The last real answer is served stale when asking fails, up to StaleMax, and an error never
// creates or refreshes an entry.
func TestTheLastRealAnswerIsServedStaleWhenAskingFails(t *testing.T) {
	clk := newClock()
	f := &decoderFake{plan: &DecoderPlan{Present: true, ProfileVersionToken: "m@1"}}
	d := NewDecoders(f, DecoderConfig{Now: clk.now})
	_, err := d.Lookup(context.Background(), "acme", "cs", "tok", "meter")
	require.NoError(t, err)

	f.plan, f.err = nil, errors.New("down")
	clk.advance(2 * time.Minute)
	got, err := d.Lookup(context.Background(), "acme", "cs", "tok", "meter")
	require.NoError(t, err)
	require.True(t, got.Stale)
	require.True(t, got.Plan.Present)

	// The failed refresh did not renew it: past the bound it is an error.
	clk.advance(14 * time.Minute)
	_, err = d.Lookup(context.Background(), "acme", "cs", "tok", "meter")
	require.Error(t, err)

	// A rejected token is never answered from memory.
	f.plan, f.err = &DecoderPlan{}, nil
	_, err = d.Lookup(context.Background(), "acme", "cs", "tok", "meter")
	require.NoError(t, err)
	f.plan, f.err = nil, nil
	clk.advance(2 * time.Minute)
	_, err = d.Lookup(context.Background(), "acme", "cs", "tok", "meter")
	require.ErrorIs(t, err, ErrDecoderUnauthenticated)
}

// One caller's rejected token is not the answer of a waiter holding a good one.
func TestAFlightIsNotSharedAcrossBearers(t *testing.T) {
	gate := make(chan struct{})
	b := &bearerFake{gate: gate}
	d := NewDecoders(b, DecoderConfig{})
	var wg sync.WaitGroup
	res := make([]error, 2)
	for i, tok := range []string{"bad", "good"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, res[i] = d.Lookup(context.Background(), "acme", "cs", tok, "meter")
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	require.ErrorIs(t, res[0], ErrDecoderUnauthenticated)
	require.NoError(t, res[1])
}

type bearerFake struct{ gate chan struct{} }

func (b *bearerFake) Decoder(_ context.Context, _, _, bearer, _ string) (*DecoderPlan, error) {
	<-b.gate
	if bearer == "bad" {
		return nil, nil
	}
	return &DecoderPlan{}, nil
}

// The literal error the release that predates the decoder query (v1.1.0) answers, as served. The
// generated check above builds the same error from a schema; this pins the bytes, so a change to
// the matcher cannot drift from what is actually deployed. A schema-introspection probe was
// weighed and not taken: it is a second request per fill, and keying on the one error the old
// release is known to give keeps the tolerance exactly as wide as that release.
func TestTheV110ErrorStringIsTolerated(t *testing.T) {
	err := &svcclient.GraphQLError{Messages: []string{
		`Cannot query field "integrationDecoder" on type "Query". Did you mean "integration", "integrations", or "integrationAuth"?`}}
	require.True(t, IsDecoderUnsupported(err))
}
