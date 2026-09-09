// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// DeviceReplacement.device is declared `Device!`, so the resolver has exactly two
// honest answers for a record that arrived without its association: load the device,
// or fail the field. It used to have a third — a zero-valued Device, which renders
// `device { token }` as `""` — and that is the answer these tests exist to keep out.
//
// The resolvers are constructed DIRECTLY here rather than reached through a query,
// because both shipping construction paths supply the association (the query
// Preloads, the mutation attaches after commit). A test that could only get there
// through them would be testing that they still work, not that the resolver is
// honest when they do not — and a future third path is precisely the caller this
// guards against.

// deviceIdOf returns the primary key of a seeded device.
func deviceIdOf(t *testing.T, ctx context.Context, token string) uint {
	t.Helper()
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	found, err := api.DevicesByToken(ctx, []string{token})
	if err != nil {
		t.Fatalf("look up device %s: %v", token, err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 device for %s, got %d", token, len(found))
	}
	return found[0].ID
}

// A record whose association was never populated resolves the device by its id.
// This is the case a third construction path would produce, and it is the reason
// erring on failure is affordable: the load succeeds whenever the row is sound.
func TestDeviceReplacementDeviceLazyLoadsByIdWhenTheAssociationIsAbsent(t *testing.T) {
	ctx := newReplacementWireCtx(t, auth.DeviceRead)
	seedReplacementWireDevice(t, ctx)

	r := &DeviceReplacementResolver{
		M: model.DeviceReplacement{DeviceId: deviceIdOf(t, ctx, "dozer-01")},
		S: &SchemaResolver{},
		C: ctx,
	}
	device := r.Device()
	if device == nil {
		t.Fatalf("device resolved to nil for a record whose device exists")
	}
	if got := device.Token(); got != "dozer-01" {
		t.Errorf("lazy-loaded device token = %q, want dozer-01", got)
	}
}

// The counterweight: when the association IS loaded, it is what answers. The lazy
// load must not displace a value the caller already has in hand, so the id is
// pointed somewhere that cannot resolve — if the load ran, the field would fail.
func TestDeviceReplacementDeviceAnswersFromThePreloadedAssociation(t *testing.T) {
	ctx := newReplacementWireCtx(t, auth.DeviceRead)
	seedReplacementWireDevice(t, ctx)

	loaded := &model.Device{}
	loaded.Token = "dozer-01"
	r := &DeviceReplacementResolver{
		M: model.DeviceReplacement{DeviceId: 424242, Device: loaded},
		S: &SchemaResolver{},
		C: ctx,
	}
	device := r.Device()
	if device == nil {
		t.Fatalf("device resolved to nil even though the association was loaded")
	}
	if got := device.Token(); got != "dozer-01" {
		t.Errorf("device token = %q, want dozer-01", got)
	}
}

// 🔴 THE CASE THE ZERO VALUE USED TO SWALLOW. No association and no loadable device
// is a broken read, and the field must say so rather than answer an empty token.
func TestDeviceReplacementDeviceIsNilWhenTheDeviceCannotBeLoaded(t *testing.T) {
	ctx := newReplacementWireCtx(t, auth.DeviceRead)
	seedReplacementWireDevice(t, ctx)

	r := &DeviceReplacementResolver{
		M: model.DeviceReplacement{DeviceId: 424242},
		S: &SchemaResolver{},
		C: ctx,
	}
	device := r.Device()
	if device == nil {
		return
	}
	if device.Token() == "" {
		t.Fatalf("device resolved to a zero-valued device (token = %q); want a nil resolver "+
			"so the non-null field errors", device.Token())
	}
	t.Fatalf("device resolved to %q, but no device with that id exists", device.Token())
}

// And the consequence of that nil, over the real schema: a GraphQL error, not a
// successful response carrying an empty token. Returning nil is only the honest
// answer if the non-null field actually rejects it, which is a property of the
// schema rather than of the resolver — so it is asserted here rather than assumed.
//
// The row is inserted with a dangling device_id, which is the state the resolver is
// being asked about. Nothing the API offers can create one: that is the point.
func TestDeviceReplacementWithNoLoadableDeviceErrorsOverTheSchema(t *testing.T) {
	ctx := newReplacementWireCtx(t, auth.DeviceRead)
	seedReplacementWireDevice(t, ctx)

	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	orphan := &model.DeviceReplacement{
		DeviceId:                424242,
		OccurredTime:            time.Now(),
		Actor:                   "tech@acme.example",
		RetiredCredentialTokens: []byte(`[]`),
		NewCredentialToken:      "dozer-01-cred-2",
		NewCredentialType:       string(model.CredentialMqttBasic),
	}
	if err := api.RDB.DB(ctx).Create(orphan).Error; err != nil {
		t.Fatalf("seed replacement with an unresolvable device: %v", err)
	}

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, `query($criteria: DeviceReplacementSearchCriteria!) {
  deviceReplacements(criteria: $criteria) {
    results { device { token } }
  }
}`, "", map[string]any{
		"criteria": map[string]any{"pageNumber": 1, "pageSize": 10},
	})
	if len(res.Errors) == 0 {
		t.Fatalf("deviceReplacements succeeded over a row whose device cannot be loaded: %s", res.Data)
	}
	// The error has to be ABOUT the device being null. "some error occurred" would
	// also be satisfied by a seed failure or a change to the query's auth gate, which
	// would leave this test green while covering nothing.
	msg := strings.ToLower(res.Errors[0].Error())
	if !strings.Contains(msg, "device") || !strings.Contains(msg, "non-null") {
		t.Errorf("query failed for an unrelated reason: %v", res.Errors)
	}
	if strings.Contains(string(res.Data), `"token":""`) {
		t.Errorf("response carried an empty token rather than only an error: %s", res.Data)
	}
}

// The same class, on the mutation result. DeviceReplaceResult carries no id for any
// of its three components, so there is nothing to lazy-load and nil is the only
// honest answer — a zero value would render a successful response describing a swap
// that happened and did nothing. api.ReplaceDevice populates all three, so nothing
// reaches these branches today; they are asserted because the empty-token answer got
// here by way of a second construction path once already.
func TestDeviceReplaceResultComponentsAreNilRatherThanZeroValued(t *testing.T) {
	r := &DeviceReplaceResultResolver{
		M: model.DeviceReplaceResult{},
		S: &SchemaResolver{},
		C: context.Background(),
	}
	if d := r.Device(); d != nil {
		t.Errorf("device on an empty result = %q, want nil so the non-null field errors", d.Token())
	}
	if rep := r.Replacement(); rep != nil {
		t.Errorf("replacement on an empty result = %+v, want nil so the non-null field errors", rep.Id())
	}
	if c := r.NewCredential(); c != nil {
		t.Errorf("newCredential on an empty result = %q, want nil so the non-null field errors", c.Token())
	}
}

// The counterweight for the three above: a populated result still answers from what
// it carries. Without this, returning nil unconditionally would pass.
func TestDeviceReplaceResultComponentsAnswerFromThePopulatedResult(t *testing.T) {
	device := &model.Device{}
	device.Token = "dozer-01"
	credential := &model.DeviceCredential{}
	credential.Token = "dozer-01-cred-2"
	r := &DeviceReplaceResultResolver{
		M: model.DeviceReplaceResult{
			Device:        device,
			Replacement:   &model.DeviceReplacement{Device: device},
			NewCredential: credential,
		},
		S: &SchemaResolver{},
		C: context.Background(),
	}
	if d := r.Device(); d == nil || d.Token() != "dozer-01" {
		t.Errorf("device on a populated result = %v, want dozer-01", d)
	}
	if rep := r.Replacement(); rep == nil {
		t.Errorf("replacement on a populated result = nil, want the record")
	} else if d := rep.Device(); d == nil || d.Token() != "dozer-01" {
		t.Errorf("replacement.device on a populated result = %v, want dozer-01", d)
	}
	if c := r.NewCredential(); c == nil || c.Token() != "dozer-01-cred-2" {
		t.Errorf("newCredential on a populated result = %v, want dozer-01-cred-2", c)
	}
}
