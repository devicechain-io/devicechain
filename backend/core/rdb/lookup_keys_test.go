// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type keyThing struct {
	gorm.Model
	TenantScoped
	Token string
}

func newKeysDB(t *testing.T) (*gorm.DB, context.Context) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterTenantScoping(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&keyThing{}); err != nil {
		t.Fatal(err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	for _, tok := range []string{"a", "b", "c"} {
		if err := db.WithContext(ctx).Create(&keyThing{Token: tok}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Another tenant's row under a token the tests ask for: it must never come back.
	other := core.WithTenant(context.Background(), "other")
	if err := db.WithContext(other).Create(&keyThing{Token: "a"}).Error; err != nil {
		t.Fatal(err)
	}
	return db.WithContext(ctx), ctx
}

func TestFindByKeysReturnsMatches(t *testing.T) {
	db, _ := newKeysDB(t)
	var out []keyThing
	if err := FindByKeys(db, &out, "token", []string{"a", "c", "zz"}); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d rows, want 2 (the other tenant's \"a\" must not be returned)", len(out))
	}
}

func TestFindByKeysEmptyReturnsNothing(t *testing.T) {
	db, _ := newKeysDB(t)
	var out []keyThing
	if err := FindByKeys(db, &out, "token", []string{}); err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("empty key list returned %d rows, want 0", len(out))
	}
}

func TestFindByKeysBoundary(t *testing.T) {
	db, _ := newKeysDB(t)
	mk := func(n int) []string {
		k := make([]string, n)
		for i := range k {
			k[i] = fmt.Sprintf("k%d", i)
		}
		return k
	}
	var out []keyThing
	if err := FindByKeys(db, &out, "token", mk(MaxLookupKeys)); err != nil {
		t.Fatalf("exactly MaxLookupKeys must pass: %v", err)
	}
	err := FindByKeys(db, &out, "token", mk(MaxLookupKeys+1))
	le, ok := limit.As(err)
	if !ok {
		t.Fatalf("MaxLookupKeys+1 must be a limit refusal, got %v", err)
	}
	if le.Got != MaxLookupKeys+1 || le.Max != MaxLookupKeys {
		t.Fatalf("refusal carries wrong numbers: %+v", le)
	}
	if le.Extensions()["code"] != "LIMIT_EXCEEDED" {
		t.Fatalf("wrong code: %v", le.Extensions())
	}
}

func TestFindByKeysRefusesBadInput(t *testing.T) {
	db, _ := newKeysDB(t)
	var out []keyThing
	err := FindByKeys(db, &out, "token", "notaslice")
	if err == nil || !strings.Contains(err.Error(), "keys must be a slice") {
		t.Fatalf("non-slice keys must be refused by FindByKeys itself, got %v", err)
	}
	for _, col := range []string{"token; drop table x", "", "1token", "to ken", "token)--"} {
		err := FindByKeys(db, &out, col, []string{"a"})
		if err == nil || !strings.Contains(err.Error(), "is not a plain column name") {
			t.Fatalf("column %q must be refused with the identifier error, got %v", col, err)
		}
	}
}

func TestMaxLookupKeysIsMaxPageSize(t *testing.T) {
	if MaxLookupKeys != MaxPageSize {
		t.Fatalf("MaxLookupKeys %d != MaxPageSize %d", MaxLookupKeys, MaxPageSize)
	}
}
