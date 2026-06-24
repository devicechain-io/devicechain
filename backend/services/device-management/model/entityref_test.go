// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/entity"
)

// Every entity type must have a registry loader, or a relationship referencing it
// would fail to resolve/validate (ADR-013).
func TestEntityLoadersCoverAllTypes(t *testing.T) {
	for _, typ := range entity.All {
		if _, ok := entityLoaders[typ]; !ok {
			t.Errorf("no registry loader for entity type %q", typ)
		}
	}
	if len(entityLoaders) != len(entity.All) {
		t.Fatalf("registry has %d loaders, expected %d", len(entityLoaders), len(entity.All))
	}
}

func TestIsEntityType(t *testing.T) {
	if !IsEntityType("device") || !IsEntityType("customergroup") {
		t.Fatal("known entity types rejected")
	}
	if IsEntityType("widget") || IsEntityType("") {
		t.Fatal("unknown entity type accepted")
	}
}
