// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

// assertedTestCtx builds a context with a real device-state Api, a tenant, and state:read
// — everything the asserted query needs to run its whole path.
func assertedTestCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.DeviceState{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
	return withAuthorities(ctx, auth.StateRead)
}

// assertedArgs is the resolver's argument struct, named once so the tests read as calls
// rather than as literals.
type assertedArgs = struct {
	Source     string
	ActiveOnly bool
	AfterId    *gql.ID
	PageSize   int32
}

// TestTheResolverRefusesAnUnusablePageSize is the fail-safe at the door.
//
// 🔴 REFUSED, NOT CLAMPED — and 0 is included deliberately. GraphQL will supply a zero for
// an Int! the client forgot to bind, and a zero that meant "no limit" would silently
// restore the unbounded read this whole change removes. A bound whose absent value reads
// as "no bound" stops governing exactly when the caller stopped thinking.
func TestTheResolverRefusesAnUnusablePageSize(t *testing.T) {
	ctx := assertedTestCtx(t)
	r := &SchemaResolver{}

	for _, pageSize := range []int32{0, -1, model.MaxAssertedPageSize + 1} {
		rows, err := r.AssertedDeviceStates(ctx, assertedArgs{Source: "mqtt1", PageSize: pageSize})
		if err == nil {
			t.Fatalf("pageSize %d must be refused, got %d rows and no error", pageSize, len(rows))
		}
		if !strings.Contains(err.Error(), "pageSize") {
			t.Fatalf("the refusal must name the offending argument, got %v", err)
		}
	}
}

// TestTheResolverRefusesAMalformedCursor keeps a bad cursor from silently starting the
// walk somewhere in the middle.
//
// 🔑 fmt.Sscanf WOULD HAVE ACCEPTED "12abc" AS 12. Parsing a prefix and calling it a
// cursor means the caller asks for a page after device 12 and is told it is a page after
// whatever it named — so the devices before it are never walked, and they read to the
// reconciler exactly like devices the projection holds no row for.
func TestTheResolverRefusesAMalformedCursor(t *testing.T) {
	ctx := assertedTestCtx(t)
	r := &SchemaResolver{}

	bad := gql.ID("12abc")
	if _, err := r.AssertedDeviceStates(ctx, assertedArgs{
		Source: "mqtt1", PageSize: 10, AfterId: &bad,
	}); err == nil {
		t.Fatal("a cursor that is not a row id must be refused, not parsed as its numeric prefix")
	}
}

// TestAnAbsentCursorStartsAtTheBeginning is the first-page case, and it is what keeps the
// refusal above from also rejecting every legitimate first call.
func TestAnAbsentCursorStartsAtTheBeginning(t *testing.T) {
	ctx := assertedTestCtx(t)
	r := &SchemaResolver{}

	if _, err := r.AssertedDeviceStates(ctx, assertedArgs{
		Source: "mqtt1", PageSize: 10, AfterId: nil,
	}); err != nil {
		t.Fatalf("a first page carries no cursor and must be accepted, got %v", err)
	}
	// An empty string is what a client binding a null variable can produce, and it means
	// the same thing.
	empty := gql.ID("")
	if _, err := r.AssertedDeviceStates(ctx, assertedArgs{
		Source: "mqtt1", PageSize: 10, AfterId: &empty,
	}); err != nil {
		t.Fatalf("an empty cursor must read as a first page, got %v", err)
	}
}

// TestTheAssertedQueryStillRequiresStateRead keeps the new arguments from having quietly
// moved the authority check — the gate must stay ahead of the validation, or an
// unauthorized caller learns which page sizes are legal.
func TestTheAssertedQueryStillRequiresStateRead(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	ctx = context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
	r := &SchemaResolver{}

	// No claims at all, and a page size that is ALSO invalid: the authority error is the
	// one that must come back.
	_, err = r.AssertedDeviceStates(ctx, assertedArgs{Source: "mqtt1", PageSize: 0})
	if err == nil {
		t.Fatal("an unauthenticated caller must be refused")
	}
	if strings.Contains(err.Error(), "pageSize") {
		t.Fatalf("the authority check must run BEFORE argument validation, got %v", err)
	}
}
