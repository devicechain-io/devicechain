// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/devicechain-io/dc-microservice/conflict"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/semantic"
	"github.com/devicechain-io/dc-microservice/semantic/packs"
	"gorm.io/datatypes"
)

// This file is the semantic layer's write path: a tenant's context (which vocabulary packs it
// has enabled, which custom prefixes it declared) and the validation every binding on the
// native model goes through. Validation is core/semantic's; this file decides WHICH kind each
// binding must be and what happens around it.
//
// Every binding is stored as an EXPANDED IRI, whatever the caller wrote, so a stored binding
// never depends on a prefix map staying as it was.

// brickPoint is the class every semanticPointClass must be, directly or through its parents.
const brickPoint semantic.IRI = "https://brickschema.org/schema/Brick#Point"

// packMeta is what the layer needs of a pack without cloning it: Registry.Packs deep-copies
// every term, which is far too much to pay to learn a namespace.
type packMeta struct {
	ID, Prefix, Namespace, Version string
}

var shippedPacks = sync.OnceValue(func() []packMeta {
	all := packs.All()
	out := make([]packMeta, 0, len(all))
	for _, p := range all {
		out = append(out, packMeta{ID: p.ID, Prefix: p.Prefix, Namespace: p.Namespace, Version: p.Version})
	}
	return out
})

// packOf returns the pack whose namespace contains iri.
func packOf(iri string) (packMeta, bool) {
	for _, p := range shippedPacks() {
		if strings.HasPrefix(iri, p.Namespace) {
			return p, true
		}
	}
	return packMeta{}, false
}

// SemanticCURIE renders an expanded IRI for display: the shipping pack's prefix when a pack
// owns it, else the tenant's custom prefix whose namespace contains it (the longest namespace
// wins), else the IRI itself. custom may be nil, in which case only packs are consulted.
func SemanticCURIE(iri string, custom map[string]string) string {
	if p, ok := packOf(iri); ok {
		return p.Prefix + ":" + strings.TrimPrefix(iri, p.Namespace)
	}
	bestPrefix, bestNS := "", ""
	for prefix, ns := range custom {
		if strings.HasPrefix(iri, ns) && len(ns) > len(bestNS) {
			bestPrefix, bestNS = prefix, ns
		}
	}
	if bestNS != "" {
		return bestPrefix + ":" + strings.TrimPrefix(iri, bestNS)
	}
	return iri
}

// tenantSemanticContext reads the caller tenant's context, or an empty one when it never set
// one. An empty context enables nothing, so every binding is refused until a context exists:
// the layer is opt-in per tenant.
func (api *Api) tenantSemanticContext(ctx context.Context) (*SemanticContext, error) {
	var rows []SemanticContext
	if err := api.RDB.DB(ctx).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return &SemanticContext{EnabledPacks: rdb.Anchors{}, CustomPrefixes: datatypes.JSON("{}"),
			PackVersions: datatypes.JSON("{}")}, nil
	}
	return &rows[0], nil
}

// SemanticContext returns the caller tenant's context (empty when none was ever set).
func (api *Api) SemanticContext(ctx context.Context) (*SemanticContext, error) {
	return api.tenantSemanticContext(ctx)
}

// CustomPrefixMap decodes the context's custom prefixes.
func (c *SemanticContext) CustomPrefixMap() (map[string]string, error) {
	out := map[string]string{}
	if len(c.CustomPrefixes) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(c.CustomPrefixes, &out); err != nil {
		return nil, fmt.Errorf("semantic context custom prefixes: %w", err)
	}
	return out, nil
}

// PackVersionMap decodes the context's recorded pack versions.
func (c *SemanticContext) PackVersionMap() (map[string]string, error) {
	out := map[string]string{}
	if len(c.PackVersions) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(c.PackVersions, &out); err != nil {
		return nil, fmt.Errorf("semantic context pack versions: %w", err)
	}
	return out, nil
}

// DecodePackVersions decodes a stored {pack id: version} document.
func DecodePackVersions(raw datatypes.JSON) (map[string]string, error) {
	out := map[string]string{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("semantic pack versions: %w", err)
	}
	return out, nil
}

// tenantContextOf converts the stored row to the validator's input.
func tenantContextOf(c *SemanticContext) (semantic.TenantContext, error) {
	custom, err := c.CustomPrefixMap()
	if err != nil {
		return semantic.TenantContext{}, err
	}
	return semantic.TenantContext{EnabledPacks: append([]string(nil), c.EnabledPacks...), CustomPrefixes: custom}, nil
}

func (api *Api) tenantValidatorContext(ctx context.Context) (semantic.TenantContext, error) {
	c, err := api.tenantSemanticContext(ctx)
	if err != nil {
		return semantic.TenantContext{}, err
	}
	return tenantContextOf(c)
}

// SetSemanticContext replaces the caller tenant's semantic context.
//
// The request is checked by core/semantic (a pack this build does not ship, a custom prefix
// that shadows a pack, an overlapping namespace, a size limit), and then against the
// bindings that already exist: if the new context would not explain every stored binding
// (see refuseOrphaningContext), it is REFUSED with SEMANTIC_CONTEXT_IN_USE and the number of
// bindings it would orphan. Silently orphaning a binding is not an option, because a stored
// IRI that no enabled vocabulary explains is a binding nothing can validate, display or
// reason over.
//
// Remapping a custom prefix to the same namespace, or changing its spelling, is always
// allowed: bindings hold the expanded IRI.
func (api *Api) SetSemanticContext(ctx context.Context, request *SemanticContextRequest) (*SemanticContext, error) {
	if request == nil {
		return nil, semanticRefusal("", fmt.Errorf("a semantic context is required"))
	}
	enabled := dedupeSorted(request.EnabledPacks)
	custom := map[string]string{}
	if request.CustomPrefixes != nil {
		for _, cp := range *request.CustomPrefixes {
			if _, dup := custom[cp.Prefix]; dup {
				return nil, semanticRefusal("customPrefixes", &semantic.TermError{
					Err: semantic.ErrMalformed, Term: cp.Prefix, Detail: "the prefix is declared twice"})
			}
			custom[cp.Prefix] = cp.Namespace
		}
	}
	next := semantic.TenantContext{EnabledPacks: enabled, CustomPrefixes: custom}
	if err := packs.MustDefault().ValidateContext(next); err != nil {
		return nil, semanticRefusal("", err)
	}

	row, err := api.tenantSemanticContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := api.refuseOrphaningContext(ctx, next); err != nil {
		return nil, err
	}

	versions := make(map[string]string, len(enabled))
	for _, p := range shippedPacks() {
		if containsString(enabled, p.ID) {
			versions[p.ID] = p.Version
		}
	}
	customJSON, err := json.Marshal(custom)
	if err != nil {
		return nil, err
	}
	versionsJSON, err := json.Marshal(versions)
	if err != nil {
		return nil, err
	}

	row.EnabledPacks = rdb.Anchors(enabled)
	row.CustomPrefixes = datatypes.JSON(customJSON)
	row.PackVersions = datatypes.JSON(versionsJSON)
	// A tenant's first context is a create; the unique index on live rows makes the loser of
	// two concurrent first sets fail rather than leave two contexts. That loss is CONFLICT:
	// the request was sound, another one won, and reading the context and retrying resolves it.
	if err := api.RDB.DB(ctx).Save(row).Error; err != nil {
		if conflict.Is(err) {
			return nil, conflict.New("another request set this tenant's semantic context at the same time; read it and retry")
		}
		return nil, err
	}
	// Answer with the row as STORED, re-read rather than the value just written: the
	// database keeps updatedAt to the microsecond, and a response carrying the in-memory
	// nanoseconds would differ from every later read of the same row.
	return api.tenantSemanticContext(ctx)
}

// refuseOrphaningContext refuses a context under which any stored binding would be
// unexplained: a bound IRI that the NEXT context's packs and custom prefixes do not expand.
//
// It checks EVERY bound IRI against the next context, not just the packs or namespaces this
// change drops. A binding write validates against the context it read, so one racing a
// context change can commit a binding the new context no longer explains; checking only the
// delta would let that orphan live on silently, because the next change drops nothing it
// uses. Checked whole, any orphan, however it arose, makes the next context change a loud
// refusal with the true count, which re-enabling the vocabulary or unbinding resolves.
//
// 🔴 A SMALL WINDOW REMAINS, AND THIS IS WHAT IT IS. A binding write that validated
// against the old context and commits AFTER this check ran is not seen by it. Closing that
// needs one tenant lock held from validation to commit across every binding write path; until
// then, such an orphan is caught by the next setSemanticContext, never silently accepted.
func (api *Api) refuseOrphaningContext(ctx context.Context, next semantic.TenantContext) error {
	iris, err := api.boundIRIs(ctx)
	if err != nil {
		return err
	}
	reg := packs.MustDefault()
	counts := map[string]int{} // what the binding needs -> how many bindings
	total := 0
	for _, iri := range iris {
		if _, err := reg.Expand(iri, next); err == nil {
			continue
		}
		total++
		counts[semanticVocabularyOf(iri)]++
	}
	if total == 0 {
		return nil
	}
	needs := make([]string, 0, len(counts))
	for v := range counts {
		needs = append(needs, v)
	}
	sort.Strings(needs)
	parts := make([]string, 0, len(needs))
	for _, v := range needs {
		parts = append(parts, fmt.Sprintf("%s (%d)", v, counts[v]))
	}
	return &SemanticError{
		Code: CodeSemanticContextInUse,
		Err: fmt.Errorf("%d binding(s) would not be explained by this context; they use %s. "+
			"Keep those vocabularies, or unbind them first", total, strings.Join(parts, ", ")),
		References: total,
	}
}

// semanticVocabularyOf names what a bound IRI needs from a context: the shipped pack whose namespace
// holds it, or else the IRI itself (a custom namespace is the tenant's own, so the term is
// the most specific thing that can be named).
func semanticVocabularyOf(iri string) string {
	if p, ok := packOf(iri); ok {
		return fmt.Sprintf("pack %q", p.ID)
	}
	return fmt.Sprintf("%q", iri)
}

// boundIRIs lists every expanded IRI the tenant's draft model binds, one entry per use. It
// reads only the binding columns of rows that have one.
func (api *Api) boundIRIs(ctx context.Context) ([]string, error) {
	var out []string

	var dts []DeviceType
	if err := api.RDB.DB(ctx).Select("semantic_types").Where("semantic_types IS NOT NULL").Find(&dts).Error; err != nil {
		return nil, err
	}
	for _, r := range dts {
		out = append(out, r.SemanticTypes...)
	}
	var ats []AssetType
	if err := api.RDB.DB(ctx).Select("semantic_types").Where("semantic_types IS NOT NULL").Find(&ats).Error; err != nil {
		return nil, err
	}
	for _, r := range ats {
		out = append(out, r.SemanticTypes...)
	}
	var areas []AreaType
	if err := api.RDB.DB(ctx).Select("semantic_types").Where("semantic_types IS NOT NULL").Find(&areas).Error; err != nil {
		return nil, err
	}
	for _, r := range areas {
		out = append(out, r.SemanticTypes...)
	}
	var cts []CustomerType
	if err := api.RDB.DB(ctx).Select("semantic_types").Where("semantic_types IS NOT NULL").Find(&cts).Error; err != nil {
		return nil, err
	}
	for _, r := range cts {
		out = append(out, r.SemanticTypes...)
	}
	var rts []EntityRelationshipType
	if err := api.RDB.DB(ctx).Select("semantic_predicate", "semantic_inverse").
		Where("semantic_predicate IS NOT NULL OR semantic_inverse IS NOT NULL").Find(&rts).Error; err != nil {
		return nil, err
	}
	for _, r := range rts {
		if r.SemanticPredicate.Valid {
			out = append(out, r.SemanticPredicate.String)
		}
		if r.SemanticInverse.Valid {
			out = append(out, r.SemanticInverse.String)
		}
	}
	var mds []MetricDefinition
	if err := api.RDB.DB(ctx).Select("semantic_property", "semantic_point_class").
		Where("semantic_property IS NOT NULL OR semantic_point_class IS NOT NULL").Find(&mds).Error; err != nil {
		return nil, err
	}
	for _, r := range mds {
		if r.SemanticProperty != nil {
			out = append(out, *r.SemanticProperty)
		}
		if r.SemanticPointClass != nil {
			out = append(out, *r.SemanticPointClass)
		}
	}
	return out, nil
}

// bindSemanticTypes validates a list of class terms against the caller tenant's context and
// returns the expanded IRIs to store: deduplicated, in the order given. An empty list binds
// nothing (nil). The context is read only when there is something to validate.
func (api *Api) bindSemanticTypes(ctx context.Context, terms []string) (rdb.Anchors, error) {
	const field = "semanticTypes"
	if len(terms) == 0 {
		return nil, nil
	}
	if len(terms) > MaxSemanticTypes {
		return nil, semanticRefusal(field, &semantic.TermError{Err: semantic.ErrLimit,
			Detail: fmt.Sprintf("%d terms given, at most %d may be bound", len(terms), MaxSemanticTypes)})
	}
	tc, err := api.tenantValidatorContext(ctx)
	if err != nil {
		return nil, err
	}
	reg := packs.MustDefault()
	out := make(rdb.Anchors, 0, len(terms))
	seen := make(map[semantic.IRI]bool, len(terms))
	for _, term := range terms {
		iri, err := reg.Validate(term, semantic.KindClass, tc)
		if err != nil {
			return nil, semanticRefusal(field, err)
		}
		if seen[iri] {
			continue
		}
		seen[iri] = true
		out = append(out, string(iri))
	}
	return out, nil
}

// semanticTypesOnCreate binds a create request's optional list.
func (api *Api) semanticTypesOnCreate(ctx context.Context, terms *[]string) (rdb.Anchors, error) {
	if terms == nil {
		return nil, nil
	}
	return api.bindSemanticTypes(ctx, *terms)
}

// semanticTypesOnUpdate folds a partial update's list onto the stored one: omitted keeps it,
// null or empty clears it, a list sets it. An omitted field validates nothing, so an edit of
// an unrelated field never fails on a binding made under an older context.
func (api *Api) semanticTypesOnUpdate(ctx context.Context, field dcgraphql.OptionalStringList,
	current rdb.Anchors) (rdb.Anchors, error) {
	if !field.Set {
		return current, nil
	}
	return api.bindSemanticTypes(ctx, field.ApplyTo(nil))
}

// resolveBinding validates one term of the wanted kind and returns the resolution.
func resolveBinding(field, term string, kind semantic.Kind, tc semantic.TenantContext) (semantic.Resolution, error) {
	res, err := packs.MustDefault().Resolve(term, kind, tc)
	if err != nil {
		return semantic.Resolution{}, semanticRefusal(field, err)
	}
	return res, nil
}

// bindPredicateOnCreate resolves a relationship type's predicate and inverse. The inverse,
// when omitted, is the pack's declared one; the stored value is the resolved one.
func (api *Api) bindPredicateOnCreate(ctx context.Context, predicate, inverse *string) (sql.NullString, sql.NullString, error) {
	if predicate == nil && inverse == nil {
		return sql.NullString{}, sql.NullString{}, nil
	}
	if predicate == nil {
		return sql.NullString{}, sql.NullString{}, semanticRefusal("semanticInverse",
			fmt.Errorf("an inverse needs a semanticPredicate"))
	}
	tc, err := api.tenantValidatorContext(ctx)
	if err != nil {
		return sql.NullString{}, sql.NullString{}, err
	}
	res, err := resolveBinding("semanticPredicate", *predicate, semantic.KindPredicate, tc)
	if err != nil {
		return sql.NullString{}, sql.NullString{}, err
	}
	inv, err := inverseFor(res, inverse, tc)
	if err != nil {
		return sql.NullString{}, sql.NullString{}, err
	}
	return sql.NullString{String: string(res.IRI), Valid: true}, inv, nil
}

// inverseFor resolves an explicit inverse, or falls back to the one the pack declares for
// the predicate (none for an opaque custom term, or a predicate the pack gives no inverse).
func inverseFor(pred semantic.Resolution, inverse *string, tc semantic.TenantContext) (sql.NullString, error) {
	if inverse != nil {
		res, err := resolveBinding("semanticInverse", *inverse, semantic.KindPredicate, tc)
		if err != nil {
			return sql.NullString{}, err
		}
		return sql.NullString{String: string(res.IRI), Valid: true}, nil
	}
	if !pred.Opaque && pred.Term.Inverse != nil {
		return sql.NullString{String: string(*pred.Term.Inverse), Valid: true}, nil
	}
	return sql.NullString{}, nil
}

// bindPredicateOnUpdate folds a partial update of the predicate and inverse onto the stored
// pair. Omitting both changes nothing and validates nothing. Changing the predicate while
// leaving the inverse omitted re-derives the pack's declared inverse, because the stored
// one belonged to the old predicate; nulling the predicate takes the inverse with it.
func (api *Api) bindPredicateOnUpdate(ctx context.Context, predicate, inverse dcgraphql.OptionalString,
	curPredicate, curInverse sql.NullString) (sql.NullString, sql.NullString, error) {
	if !predicate.Set && !inverse.Set {
		return curPredicate, curInverse, nil
	}
	tc, err := api.tenantValidatorContext(ctx)
	if err != nil {
		return sql.NullString{}, sql.NullString{}, err
	}
	nextPredicate := curPredicate
	var predRes *semantic.Resolution
	changed := false
	if predicate.Set {
		if predicate.Value == nil {
			nextPredicate = sql.NullString{}
			changed = curPredicate.Valid
		} else {
			res, err := resolveBinding("semanticPredicate", *predicate.Value, semantic.KindPredicate, tc)
			if err != nil {
				return sql.NullString{}, sql.NullString{}, err
			}
			predRes = &res
			nextPredicate = sql.NullString{String: string(res.IRI), Valid: true}
			changed = !curPredicate.Valid || curPredicate.String != string(res.IRI)
		}
	}
	nextInverse := curInverse
	switch {
	case inverse.Set && inverse.Value != nil:
		if !nextPredicate.Valid {
			return sql.NullString{}, sql.NullString{}, semanticRefusal("semanticInverse",
				fmt.Errorf("an inverse needs a semanticPredicate"))
		}
		res, err := resolveBinding("semanticInverse", *inverse.Value, semantic.KindPredicate, tc)
		if err != nil {
			return sql.NullString{}, sql.NullString{}, err
		}
		nextInverse = sql.NullString{String: string(res.IRI), Valid: true}
	case inverse.Set:
		nextInverse = sql.NullString{}
	case changed:
		nextInverse = sql.NullString{}
		if predRes != nil {
			nextInverse, err = inverseFor(*predRes, nil, tc)
			if err != nil {
				return sql.NullString{}, sql.NullString{}, err
			}
		}
	}
	return nextPredicate, nextInverse, nil
}

// bindMetricProperty validates a metric's observed property.
func (api *Api) bindMetricProperty(ctx context.Context, term *string) (*string, error) {
	if term == nil {
		return nil, nil
	}
	tc, err := api.tenantValidatorContext(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resolveBinding("semanticProperty", *term, semantic.KindObservableProperty, tc)
	if err != nil {
		return nil, err
	}
	s := string(res.IRI)
	return &s, nil
}

// bindMetricPointClass validates a metric's Brick point class: a CLASS of an enabled pack
// that is a brick:Point. A custom-prefix class is opaque and cannot be shown to be a point,
// so it is refused rather than assumed to be one.
func (api *Api) bindMetricPointClass(ctx context.Context, term *string) (*string, error) {
	if term == nil {
		return nil, nil
	}
	tc, err := api.tenantValidatorContext(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resolveBinding("semanticPointClass", *term, semantic.KindClass, tc)
	if err != nil {
		return nil, err
	}
	if res.Opaque || !packs.MustDefault().IsA(res.IRI, brickPoint) {
		return nil, semanticRefusal("semanticPointClass", &semantic.TermError{Err: semantic.ErrWrongKind, Term: *term,
			Detail: fmt.Sprintf("%s is not a brick:Point", res.IRI)})
	}
	s := string(res.IRI)
	return &s, nil
}

// foldMetricBinding folds one optional metric binding of a partial update onto the stored
// one: omitted keeps it, null clears it, a term validates and sets it.
func foldMetricBinding(ctx context.Context, field dcgraphql.OptionalString, current *string,
	bind func(context.Context, *string) (*string, error)) (*string, error) {
	if !field.Set {
		return current, nil
	}
	if field.Value == nil {
		return nil, nil
	}
	return bind(ctx, field.Value)
}

// semanticPackVersionsFor returns {pack id: version} for the packs that own any of iris, or
// nil when none does (custom-prefix terms are opaque and name no pack).
func semanticPackVersionsFor(iris []string) map[string]string {
	var out map[string]string
	for _, iri := range iris {
		if p, ok := packOf(iri); ok {
			if out == nil {
				out = map[string]string{}
			}
			out[p.ID] = p.Version
		}
	}
	return out
}

// restoreSemanticTypes re-checks a binding a rollback is about to restore. A rolled-back
// version may carry a term whose pack the tenant has since stopped enabling; restoring it
// would orphan the binding, so the rollback is refused instead. It checks the PREFIX only
// (the term was valid when published, and may have been deprecated upstream since, which
// is no reason to refuse an old contract).
func (api *Api) restoreSemanticTypes(ctx context.Context, iris rdb.Anchors) error {
	if len(iris) == 0 {
		return nil
	}
	tc, err := api.tenantValidatorContext(ctx)
	if err != nil {
		return err
	}
	reg := packs.MustDefault()
	for _, iri := range iris {
		if _, err := reg.Expand(iri, tc); err != nil {
			return semanticRefusal("semanticTypes", err)
		}
	}
	return nil
}

func dedupeSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
