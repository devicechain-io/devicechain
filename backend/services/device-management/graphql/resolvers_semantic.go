// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/semantic/packs"
)

// The semantic layer's GraphQL surface (additive). A binding is STORED as an expanded IRI and
// SERVED as both that IRI and a display CURIE, so a client can show "brick:feeds" and still
// know exactly which term it is when a tenant later remaps a custom prefix.

// SemanticTermResolver resolves the SemanticTerm type.
type SemanticTermResolver struct {
	iri, curie string
}

func (r *SemanticTermResolver) Iri() string   { return r.iri }
func (r *SemanticTermResolver) Curie() string { return r.curie }

// semanticPrefixesKey memoizes the caller tenant's custom prefixes for one request.
type semanticPrefixesKey struct{}

// tenantCustomPrefixes reads the caller tenant's custom prefixes ONCE per request: every row
// of a list that binds a custom-prefix term needs the same map, and reading it per row would
// be an N+1 returning N identical answers.
func (s *SchemaResolver) tenantCustomPrefixes(ctx context.Context) (map[string]string, error) {
	return util.Memoize(ctx, semanticPrefixesKey{}, func() (map[string]string, error) {
		sc, err := s.GetApi(ctx).SemanticContext(ctx)
		if err != nil {
			return nil, err
		}
		return sc.CustomPrefixMap()
	})
}

// semanticTerms renders stored IRIs for display. A term inside a shipped pack needs no
// tenant data; only one that no pack explains needs the tenant's custom prefixes, which are
// read at most once per request.
func (s *SchemaResolver) semanticTerms(ctx context.Context, iris []string) (*[]*SemanticTermResolver, error) {
	out := make([]*SemanticTermResolver, 0, len(iris))
	for _, iri := range iris {
		curie := model.SemanticCURIE(iri, nil)
		if curie == iri {
			custom, err := s.tenantCustomPrefixes(ctx)
			if err != nil {
				return nil, err
			}
			curie = model.SemanticCURIE(iri, custom)
		}
		out = append(out, &SemanticTermResolver{iri: iri, curie: curie})
	}
	return &out, nil
}

// semanticTerm is semanticTerms for one optional binding.
func (s *SchemaResolver) semanticTerm(ctx context.Context, iri *string) (*SemanticTermResolver, error) {
	if iri == nil {
		return nil, nil
	}
	terms, err := s.semanticTerms(ctx, []string{*iri})
	if err != nil {
		return nil, err
	}
	return (*terms)[0], nil
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func (r *DeviceTypeResolver) SemanticTypes() (*[]*SemanticTermResolver, error) {
	return r.S.semanticTerms(r.C, r.M.SemanticTypes)
}

func (r *AssetTypeResolver) SemanticTypes() (*[]*SemanticTermResolver, error) {
	return r.S.semanticTerms(r.C, r.M.SemanticTypes)
}

func (r *AreaTypeResolver) SemanticTypes() (*[]*SemanticTermResolver, error) {
	return r.S.semanticTerms(r.C, r.M.SemanticTypes)
}

func (r *CustomerTypeResolver) SemanticTypes() (*[]*SemanticTermResolver, error) {
	return r.S.semanticTerms(r.C, r.M.SemanticTypes)
}

func (r *AssetTypeVersionResolver) SemanticTypes() (*[]*SemanticTermResolver, error) {
	return r.S.semanticTerms(r.C, r.M.SemanticTypes)
}

func (r *AssetTypeVersionResolver) SemanticPackVersions() (*[]*SemanticPackVersionResolver, error) {
	if r.M.SemanticPackVersions == nil {
		return &[]*SemanticPackVersionResolver{}, nil
	}
	versions, err := model.DecodePackVersions(*r.M.SemanticPackVersions)
	if err != nil {
		return nil, err
	}
	return packVersionResolvers(versions)
}

func (r *EntityRelationshipTypeResolver) SemanticPredicate() (*SemanticTermResolver, error) {
	return r.S.semanticTerm(r.C, nullStringPtr(r.M.SemanticPredicate))
}

func (r *EntityRelationshipTypeResolver) SemanticInverse() (*SemanticTermResolver, error) {
	return r.S.semanticTerm(r.C, nullStringPtr(r.M.SemanticInverse))
}

func (r *MetricDefinitionResolver) SemanticProperty() (*SemanticTermResolver, error) {
	return r.S.semanticTerm(r.C, r.M.SemanticProperty)
}

func (r *MetricDefinitionResolver) SemanticPointClass() (*SemanticTermResolver, error) {
	return r.S.semanticTerm(r.C, r.M.SemanticPointClass)
}

// ---------------------------------
// Tenant semantic context
// ---------------------------------

// SemanticContextResolver resolves a tenant's semantic context.
type SemanticContextResolver struct {
	M model.SemanticContext
}

func (r *SemanticContextResolver) EnabledPacks() (*[]string, error) {
	ids := append([]string{}, r.M.EnabledPacks...)
	sort.Strings(ids)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		v, err := semanticPackEnum(id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return &out, nil
}

func (r *SemanticContextResolver) CustomPrefixes() (*[]*CustomPrefixResolver, error) {
	custom, err := r.M.CustomPrefixMap()
	if err != nil {
		return nil, err
	}
	prefixes := make([]string, 0, len(custom))
	for p := range custom {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	out := make([]*CustomPrefixResolver, 0, len(prefixes))
	for _, p := range prefixes {
		out = append(out, &CustomPrefixResolver{prefix: p, namespace: custom[p]})
	}
	return &out, nil
}

func (r *SemanticContextResolver) PackVersions() (*[]*SemanticPackVersionResolver, error) {
	versions, err := r.M.PackVersionMap()
	if err != nil {
		return nil, err
	}
	return packVersionResolvers(versions)
}

func (r *SemanticContextResolver) UpdatedAt() *string {
	if r.M.ID == 0 {
		return nil
	}
	return util.FormatTime(r.M.UpdatedAt)
}

// CustomPrefixResolver resolves the CustomPrefix type.
type CustomPrefixResolver struct{ prefix, namespace string }

func (r *CustomPrefixResolver) Prefix() string    { return r.prefix }
func (r *CustomPrefixResolver) Namespace() string { return r.namespace }

// SemanticPackVersionResolver resolves the SemanticPackVersion type.
type SemanticPackVersionResolver struct{ pack, version string }

// Pack is the pack's SemanticPack value. A recorded pack this build no longer ships cannot
// be named by the enum, and is an error rather than a guess.
func (r *SemanticPackVersionResolver) Pack() (string, error) { return semanticPackEnum(r.pack) }
func (r *SemanticPackVersionResolver) Version() string       { return r.version }

func packVersionResolvers(versions map[string]string) (*[]*SemanticPackVersionResolver, error) {
	ids := make([]string, 0, len(versions))
	for id := range versions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*SemanticPackVersionResolver, 0, len(ids))
	for _, id := range ids {
		out = append(out, &SemanticPackVersionResolver{pack: id, version: versions[id]})
	}
	return &out, nil
}

// SemanticContext returns the tenant's semantic context. It is a CATALOGUE read: tenant
// configuration a customer's console needs to render the bindings it can see, readable by a
// member limited to part of the tenant.
func (r *SchemaResolver) SemanticContext(ctx context.Context) (*SemanticContextResolver, error) {
	if err := auth.AuthorizeCatalogue(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	sc, err := r.GetApi(ctx).SemanticContext(ctx)
	if err != nil {
		return nil, err
	}
	return &SemanticContextResolver{M: *sc}, nil
}

// SetSemanticContext replaces the tenant's semantic context. It is guarded by the authority
// that guards authoring the types it governs (device:write), through Authorize, which refuses
// every scoped caller.
func (r *SchemaResolver) SetSemanticContext(ctx context.Context, args struct {
	Request semanticContextRequestInput
}) (*SemanticContextResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	prefixes := append([]model.CustomPrefixInput{}, args.Request.CustomPrefixes...)
	request := model.SemanticContextRequest{EnabledPacks: []string{}, CustomPrefixes: &prefixes}
	for _, v := range args.Request.EnabledPacks {
		id, err := semanticPackID(v)
		if err != nil {
			return nil, err
		}
		request.EnabledPacks = append(request.EnabledPacks, id)
	}
	sc, err := r.GetApi(ctx).SetSemanticContext(ctx, &request)
	if err != nil {
		return nil, err
	}
	// A later field of the same document renders CURIEs under the context just set.
	util.ForgetMemo(ctx, semanticPrefixesKey{})
	return &SemanticContextResolver{M: *sc}, nil
}

// semanticContextRequestInput is SemanticContextRequest as GraphQL delivers it: packs by
// their SemanticPack value, which the resolver maps to pack ids.
type semanticContextRequestInput struct {
	EnabledPacks   []string
	CustomPrefixes []model.CustomPrefixInput
}

// The SemanticPack enum names a pack by its id upper-cased with '-' as '_' (sdm-parking is
// SDM_PARKING). The mapping is derived from the packs this build ships, and
// TestSemanticPackEnumMatchesTheShippedPacks holds the served enum to the same list, so a
// pack cannot ship without its enum value, nor an enum value without its pack.
var semanticPackEnums = sync.OnceValues(func() (map[string]string, map[string]string) {
	toEnum, toID := map[string]string{}, map[string]string{}
	for _, p := range packs.All() {
		v := strings.ToUpper(strings.ReplaceAll(p.ID, "-", "_"))
		toEnum[p.ID], toID[v] = v, p.ID
	}
	return toEnum, toID
})

func semanticPackEnum(id string) (string, error) {
	toEnum, _ := semanticPackEnums()
	v, ok := toEnum[id]
	if !ok {
		return "", fmt.Errorf("semantic pack %q is not shipped by this build", id)
	}
	return v, nil
}

func semanticPackID(v string) (string, error) {
	_, toID := semanticPackEnums()
	id, ok := toID[v]
	if !ok {
		return "", fmt.Errorf("semantic pack %s is not shipped by this build", v)
	}
	return id, nil
}
