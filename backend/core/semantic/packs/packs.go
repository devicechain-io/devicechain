// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package packs holds the generated vocabulary tables and assembles them into
// the registry the platform validates against. The *_gen.go files are written by
// backend/tools/vocabgen from the pinned inputs in hack/vocab/sources.lock;
// nothing here reads RDF.
package packs

import (
	"sync"

	"github.com/devicechain-io/dc-microservice/semantic"
)

// iri is a helper for the generated tables' Inverse field.
func iri(s string) *semantic.IRI { v := semantic.IRI(s); return &v }

// All lists every generated pack, ordered by id.
func All() []*semantic.Pack {
	return []*semantic.Pack{
		Brick,
		NGSILD,
		Saref,
		Saref4Bldg,
		Saref4City,
		SDMEnvironment,
		SDMParking,
		SDMStreetlighting,
	}
}

var defaultRegistry = sync.OnceValues(func() (*semantic.Registry, error) {
	return semantic.NewRegistry(All(), Unit)
})

// Default returns the registry over every generated pack and the unit table. It
// is built once and is safe for concurrent use. A non-nil error means a
// generated table is internally inconsistent, which the package tests rule out.
func Default() (*semantic.Registry, error) { return defaultRegistry() }

// MustDefault is Default for process start-up, where an inconsistent table is a
// build defect that must stop the process rather than validate against it.
func MustDefault() *semantic.Registry {
	r, err := Default()
	if err != nil {
		panic("semantic/packs: " + err.Error())
	}
	return r
}
