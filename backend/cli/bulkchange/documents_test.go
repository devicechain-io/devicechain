// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bulkchange

import (
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
)

// The SDL lives outside this module; run with -count=1 after schema edits.
func TestReadinessDocumentMatchesServedSchema(t *testing.T) {
	dir := filepath.Join("..", "..", "services", "device-management", "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil || !served {
		t.Fatalf("tenant SDL: served=%v, err=%v", served, err)
	}
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{readinessQuery, legacyReadinessQuery} {
		if errs := schema.Validate(query); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
}
