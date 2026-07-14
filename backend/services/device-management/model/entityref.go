// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/entity"
	"gorm.io/gorm"
)

// The entity-type registry centralizes the per-type dispatch that the
// polymorphic foreign-key columns used to encode (ADR-013). One entry per entity
// type provides the two operations the uniform (type, id) edge model needs:
// resolving a token to a row id on write (referential-integrity check), and
// loading an entity by id on read (typed loader for the GraphQL Entity
// interface). Adding a new entity type is a single entry here — no schema change.
type entityLoader struct {
	resolveToken   func(ctx context.Context, api *Api, token string) (uint, error)
	loadById       func(ctx context.Context, api *Api, id uint) (interface{}, error)
	existingIds    func(ctx context.Context, api *Api, ids []uint) ([]uint, error)
	existingTokens func(ctx context.Context, api *Api, tokens []string) ([]string, error)
}

// loaderFor builds an entityLoader from a type's by-token and by-id accessors and
// an id/token extractor, so each registry entry is a single line.
func loaderFor[T any](
	byToken func(*Api, context.Context, []string) ([]*T, error),
	byId func(*Api, context.Context, []uint) ([]*T, error),
	idOf func(*T) uint,
	tokenOf func(*T) string,
) entityLoader {
	return entityLoader{
		resolveToken: func(ctx context.Context, api *Api, token string) (uint, error) {
			matches, err := byToken(api, ctx, []string{token})
			if err != nil {
				return 0, err
			}
			if len(matches) == 0 {
				return 0, gorm.ErrRecordNotFound
			}
			return idOf(matches[0]), nil
		},
		loadById: func(ctx context.Context, api *Api, id uint) (interface{}, error) {
			matches, err := byId(api, ctx, []uint{id})
			if err != nil {
				return nil, err
			}
			if len(matches) == 0 {
				return nil, nil
			}
			return matches[0], nil
		},
		existingIds: func(ctx context.Context, api *Api, ids []uint) ([]uint, error) {
			if len(ids) == 0 {
				return []uint{}, nil
			}
			matches, err := byId(api, ctx, ids)
			if err != nil {
				return nil, err
			}
			existing := make([]uint, 0, len(matches))
			for _, m := range matches {
				existing = append(existing, idOf(m))
			}
			return existing, nil
		},
		existingTokens: func(ctx context.Context, api *Api, tokens []string) ([]string, error) {
			if len(tokens) == 0 {
				return []string{}, nil
			}
			matches, err := byToken(api, ctx, tokens)
			if err != nil {
				return nil, err
			}
			existing := make([]string, 0, len(matches))
			for _, m := range matches {
				existing = append(existing, tokenOf(m))
			}
			return existing, nil
		},
	}
}

// entityLoaders maps each entity type to its loader (ADR-013).
var entityLoaders = map[entity.Type]entityLoader{
	entity.TypeDevice:   loaderFor((*Api).DevicesByToken, (*Api).DevicesById, func(m *Device) uint { return m.ID }, func(m *Device) string { return m.Token }),
	entity.TypeAsset:    loaderFor((*Api).AssetsByToken, (*Api).AssetsById, func(m *Asset) uint { return m.ID }, func(m *Asset) string { return m.Token }),
	entity.TypeArea:     loaderFor((*Api).AreasByToken, (*Api).AreasById, func(m *Area) uint { return m.ID }, func(m *Area) string { return m.Token }),
	entity.TypeCustomer: loaderFor((*Api).CustomersByToken, (*Api).CustomersById, func(m *Customer) uint { return m.ID }, func(m *Customer) string { return m.Token }),
	entity.TypeGroup:    loaderFor((*Api).EntityGroupsByToken, (*Api).EntityGroupsById, func(m *EntityGroup) uint { return m.ID }, func(m *EntityGroup) string { return m.Token }),
}

// ResolveEntityToken resolves an entity reference (type + token) to its row id,
// returning an error if the type is unknown or no entity has that token. This is
// the write-time referential-integrity check that replaces the per-type foreign
// keys (ADR-013).
func (api *Api) ResolveEntityToken(ctx context.Context, etype string, token string) (uint, error) {
	loader, ok := entityLoaders[entity.Type(etype)]
	if !ok {
		return 0, fmt.Errorf("unknown entity type %q", etype)
	}
	return loader.resolveToken(ctx, api, token)
}

// LoadEntity loads an entity of the given type by id (the read-time typed loader
// for a relationship's source/target). It returns (nil, nil) when no row matches.
func (api *Api) LoadEntity(ctx context.Context, etype string, id uint) (interface{}, error) {
	loader, ok := entityLoaders[entity.Type(etype)]
	if !ok {
		return nil, fmt.Errorf("unknown entity type %q", etype)
	}
	return loader.loadById(ctx, api, id)
}

// ExistingEntityIds returns the subset of ids that resolve to an existing entity
// of the given type in the caller's tenant. Unknown type → error. (ADR-044
// decision 3: the reconciliation sweep uses this to find orphaned anchors.)
func (api *Api) ExistingEntityIds(ctx context.Context, etype string, ids []uint) ([]uint, error) {
	loader, ok := entityLoaders[entity.Type(etype)]
	if !ok {
		return nil, fmt.Errorf("unknown entity type %q", etype)
	}
	if len(ids) == 0 {
		return []uint{}, nil
	}
	return loader.existingIds(ctx, api, ids)
}

// ExistingEntityTokens returns the subset of tokens that resolve to an existing
// entity of the given type in the caller's tenant. Unknown type → error. (ADR-044:
// the reconciliation sweep addresses cross-service refs by token, so it asks which
// tokens still resolve to find orphaned anchors.)
func (api *Api) ExistingEntityTokens(ctx context.Context, etype string, tokens []string) ([]string, error) {
	loader, ok := entityLoaders[entity.Type(etype)]
	if !ok {
		return nil, fmt.Errorf("unknown entity type %q", etype)
	}
	if len(tokens) == 0 {
		return []string{}, nil
	}
	return loader.existingTokens(ctx, api, tokens)
}

// IsEntityType reports whether t names a known entity type.
func IsEntityType(t string) bool {
	return entity.Type(t).Valid()
}
