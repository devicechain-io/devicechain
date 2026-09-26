// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// The reconcile client reads device-management through the service client, which REFUSES a
// response over MaxResponseBytes — a refusal, not a shorter answer. A page that can exceed the cap
// therefore fails the walk on exactly the instances that need reconciling most, so each door's page
// size has to be shown to fit, or shown to adapt.

// A worst-case roster page and a worst-case threshold-attribute page — every token at the length
// the grammar allows, every attribute key at its column's width — fit under the cap at the doors'
// maximum page sizes. Measured through device-management's real schema, not estimated.
func TestReconcilePagesFitTheResponseCap(t *testing.T) {
	dm := newDmWorld(t)
	longProfile := strings.Repeat("p", core.MaxTokenLen)
	dm.profile(longProfile, nil)
	dm.deviceType("sensor", longProfile)
	reqs := make([]*dmmodel.DeviceCreateRequest, 0, dmmodel.MaxRosterPageSize)
	for i := 0; i < dmmodel.MaxRosterPageSize; i++ {
		tok := fmt.Sprintf("%0*d", core.MaxTokenLen, i)
		reqs = append(reqs, &dmmodel.DeviceCreateRequest{Token: tok, DeviceTypeToken: "sensor"})
	}
	for start := 0; start < len(reqs); start += 500 {
		if _, err := dm.api.CreateDevices(dm.ctx, reqs[start:min(start+500, len(reqs))]); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < dmmodel.MaxThresholdAttributePageSize; i++ {
		key := fmt.Sprintf("%0*d", 256, i) // the attr_key column's width
		dm.setAttr(reqs[i%len(reqs)].Token, "SERVER", key, "-123456789.123456789")
	}
	src := newSchemaFactSource(t, dm.api, "acme")
	var largest int
	measure := func(query string) {
		resp := dmSchema().Exec(schemaDmContext(dm.api, "acme"), query, "", nil)
		if len(resp.Errors) > 0 {
			t.Fatalf("query failed: %v", resp.Errors[0])
		}
		if len(resp.Data) > largest {
			largest = len(resp.Data)
		}
	}
	measure(fmt.Sprintf(`query { deviceRosterPage(limit: %d) { entries { deviceToken profileToken expectedSince } nextCursor } }`,
		dmmodel.MaxRosterPageSize))
	rosterBytes := largest
	measure(fmt.Sprintf(`query { deviceThresholdAttributePage(limit: %d) { entries { deviceToken scope key value updatedAt } nextCursor } }`,
		dmmodel.MaxThresholdAttributePageSize))
	if rosterBytes > svcclient.MaxResponseBytes || largest > svcclient.MaxResponseBytes {
		t.Fatalf("a worst-case page exceeds the %d-byte cap: roster %d, attributes %d",
			svcclient.MaxResponseBytes, rosterBytes, largest)
	}
	if rosterBytes < 150_000 || largest < 300_000 {
		t.Fatalf("the fixture is not worst-case (roster %d bytes, attributes %d bytes)", rosterBytes, largest)
	}

	// And the client walks both whole.
	roster, err := src.client.Roster(context.Background(), "acme")
	if err != nil || len(roster) != dmmodel.MaxRosterPageSize {
		t.Fatalf("roster walk returned %d entries (err %v), want %d", len(roster), err, dmmodel.MaxRosterPageSize)
	}
	attrs, err := src.client.ThresholdAttributes(context.Background(), "acme")
	if err != nil || len(attrs) != dmmodel.MaxThresholdAttributePageSize {
		t.Fatalf("attribute walk returned %d entries (err %v), want %d", len(attrs), err, dmmodel.MaxThresholdAttributePageSize)
	}
}

// The rules page carries every rule of every profile it lists, so its size is not a function of
// its row count. A page too large for the cap is HALVED and the same cursor retried, and the walk
// still returns every profile; a single profile too large on its own is an error naming the walk's
// position, never a silently shorter answer.
func TestTheRulesWalkHalvesAPageTooLargeForTheCap(t *testing.T) {
	dm := newDmWorld(t)
	// ~40 KB per profile: 50 of them (one full page) exceed the 1 MiB cap, 25 do not.
	big := `{"name":"hot","type":"threshold","description":"` + strings.Repeat("x", 40_000) +
		`","when":{"metric":"temp","op":"gt","threshold":30}}`
	for i := 0; i < dmmodel.MaxActiveProfileRulesPageSize+5; i++ {
		tok := fmt.Sprintf("p%02d", i)
		dm.profile(tok, map[string]string{"hot": big})
		dm.publish(tok)
	}
	src := newSchemaFactSource(t, dm.api, "acme")
	refusals := 0
	inner := src.client.transport
	src.client.transport = func(ctx context.Context, svc factService, tenant, query string, vars map[string]any, out any) error {
		err := inner(ctx, svc, tenant, query, vars, out)
		if errors.Is(err, svcclient.ErrResponseTooLarge) {
			refusals++
		}
		return err
	}
	profiles, err := src.client.ActiveProfiles(context.Background(), "acme")
	if err != nil {
		t.Fatalf("the walk failed instead of halving: %v", err)
	}
	if len(profiles) != dmmodel.MaxActiveProfileRulesPageSize+5 {
		t.Fatalf("the walk returned %d profiles, want %d", len(profiles), dmmodel.MaxActiveProfileRulesPageSize+5)
	}
	if refusals == 0 {
		t.Fatal("no page was ever refused; this fixture did not exercise the halving")
	}

	// One profile whose rules alone exceed the cap.
	dm2 := newDmWorld(t)
	dm2.profile("huge", map[string]string{"hot": `{"name":"hot","description":"` + strings.Repeat("y", svcclient.MaxResponseBytes) + `"}`})
	dm2.publish("huge")
	_, err = newSchemaFactSource(t, dm2.api, "acme").client.ActiveProfiles(context.Background(), "acme")
	if !errors.Is(err, svcclient.ErrResponseTooLarge) {
		t.Fatalf("a profile too large to carry must fail the walk with the cap error, got %v", err)
	}
}

// A device-management that predates the reconcile doors answers with a GraphQL validation error;
// the client reports it as version skew, so the log sends an operator to what is deployed.
func TestAnOlderDeviceManagementIsReportedAsSkew(t *testing.T) {
	c := &deviceManagementFactsClient{transport: func(context.Context, factService, string, string, map[string]any, any) error {
		return errors.New(`graphql: Cannot query field "deviceRosterPage" on type "Query".`)
	}}
	if _, err := c.Roster(context.Background(), "acme"); !errors.Is(err, errFactsSkew) {
		t.Fatalf("an unknown-field refusal was not reported as skew: %v", err)
	}
	c.transport = func(context.Context, factService, string, string, map[string]any, any) error {
		return errors.New("dial tcp: connection refused")
	}
	if _, err := c.Roster(context.Background(), "acme"); err == nil || errors.Is(err, errFactsSkew) {
		t.Fatalf("a transport failure must not read as skew: %v", err)
	}
}
