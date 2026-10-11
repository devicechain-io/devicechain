// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// A configuration revision's digest covers the exact bytes of its document, and those
// bytes are what a device receives. SQLite stores whatever it is given; Postgres re-renders
// a json/jsonb value on read (whitespace after separators, its own key order), so only a
// real server can show that the document comes back byte-for-byte.
//
// Run the same way as the other integration tests in this package:
//
//	docker run -d --name dc-it -e POSTGRES_PASSWORD=postgres -P postgres:16
//	DC_IT_PGPORT=$(docker port dc-it 5432/tcp | head -n1 | sed 's/.*://') \
//	  go test -tags integration -count=1 ./model/... -run 'OnPostgres' -v
package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// A document read back from Postgres still hashes to its digest, and is still the exact
// canonical text: sorted keys, no whitespace, nested JSON canonicalized.
func TestConfigurationDocumentReadsBackByteForByteOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	ctx := core.WithTenant(context.Background(), fmt.Sprintf("cfg%d", time.Now().UnixNano()))
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{
		{Key: "zeta", ValueType: "STRING"},
		{Key: "alpha", ValueType: "JSON"},
		{Key: "rate", ValueType: "DOUBLE"},
	})
	setConfigAttr(t, api, ctx, AttributeScopeShared, "zeta", AttributeValueString, "last")
	setConfigAttr(t, api, ctx, AttributeScopeShared, "alpha", AttributeValueJson, `{ "b": [1.0, 2], "a": {"y": "z"} }`)
	setConfigAttr(t, api, ctx, AttributeScopeShared, "rate", AttributeValueDouble, "2.50")

	const want = `{"alpha":{"a":{"y":"z"},"b":[1,2]},"rate":2.5,"zeta":"last"}`
	check := func(where string, rev *DeviceConfigurationRevision) {
		t.Helper()
		if rev == nil {
			t.Fatalf("%s: no revision", where)
		}
		if rev.Document != want {
			t.Fatalf("%s: document read back as %q, want %q", where, rev.Document, want)
		}
		sum := sha256.Sum256([]byte(rev.Document))
		if got := "sha256:" + hex.EncodeToString(sum[:]); got != rev.Digest {
			t.Fatalf("%s: sha256(document) = %s, stored digest %s", where, got, rev.Digest)
		}
	}
	check("deviceConfiguration", readConfig(t, api, ctx).Desired)
	page, err := api.DeviceConfigurationRevisions(ctx, "dev", rdb.Pagination{PageNumber: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 3 {
		t.Fatalf("want 3 revisions, got %d", len(page.Results))
	}
	check("deviceConfigurationRevisions", &page.Results[0])
	if readConfig(t, api, ctx).Stale {
		t.Fatal("a document read back from Postgres reads stale against its own recomputation")
	}
}
