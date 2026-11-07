// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package semantic answers one question without parsing any RDF: "is this
// CURIE or IRI a known term of the right kind in a vocabulary this tenant has
// enabled, and what are its parents, inverse and label?".
//
// The vocabularies (Brick, SAREF and two of its extensions, three Smart Data
// Models domains, the NGSI-LD core terms) are shipped as GENERATED Go tables in
// the packs sub-package, produced by backend/tools/vocabgen from pinned,
// hash-verified upstream files. There is no RDF parser on the request path: the
// tables are diffable in review, and what a release validates against is exactly
// what was committed.
//
// # What a binding stores
//
// Validate returns the EXPANDED IRI. Callers store that, never the CURIE, so a
// stored binding does not depend on a tenant's prefix map staying as it was.
//
// # The rules
//
//   - A prefix must belong to an enabled pack or be a custom prefix the tenant
//     declared. There is no default prefix: a bare word is refused.
//   - A pack term must exist in that pack, have the wanted Kind, and not be
//     deprecated. Deprecated terms stay in the table (so the refusal can say
//     "deprecated, use X" instead of "unknown") but are never accepted for a new
//     binding.
//   - A custom-prefix term is accepted as an OPAQUE IRI: no hierarchy, no kind
//     check, and labelled as opaque in the Resolution. A tenant's private
//     ontology is welcome; the platform does not pretend to reason over it.
//   - A full IRI is accepted only inside an enabled pack's namespace or a custom
//     namespace.
//   - A custom prefix may not shadow a pack prefix, and a custom namespace may
//     not overlap a pack namespace or another custom namespace.
//
// Every refusal is a *TermError wrapping one of the sentinel errors below, so a
// GraphQL layer can map them to INVALID_ARGUMENT and name the offending term.
package semantic

import (
	"errors"
	"fmt"
)

// IRI is an expanded, absolute identifier.
type IRI string

// Kind says what a term is FOR. It comes from the upstream modelling and is
// recorded per pack in vocabgen's mapping rules, each with its reason.
type Kind string

const (
	// KindClass is a type something can be: a device type, an area type.
	KindClass Kind = "CLASS"
	// KindPredicate is a relationship or attribute linking two things.
	KindPredicate Kind = "PREDICATE"
	// KindObservableProperty is a quantity or property a sensor can observe
	// (a metric's semantic property).
	KindObservableProperty Kind = "OBSERVABLE_PROPERTY"
)

// Valid reports whether k is one of the three kinds.
func (k Kind) Valid() bool {
	switch k {
	case KindClass, KindPredicate, KindObservableProperty:
		return true
	}
	return false
}

// Term is one vocabulary term.
type Term struct {
	IRI   IRI
	Label string
	Kind  Kind
	// Parents are the direct super-classes (or super-properties, or for an
	// individual its types) the upstream data names. They may lie outside the
	// pack; IsA follows them wherever the registry knows the term.
	Parents []IRI
	// Inverse is the declared inverse of a predicate, when the vocabulary
	// declares one (either direction is recorded on both terms).
	Inverse *IRI
	// Deprecated terms are never accepted for a new binding.
	Deprecated bool
	// ReplacedBy names the successor of a deprecated term, when upstream does.
	ReplacedBy IRI
}

// Pack is one embedded, versioned, read-only vocabulary.
type Pack struct {
	ID           string
	Prefix       string
	Namespace    string
	Version      string
	Licence      string
	SourceURL    string
	SourceSHA256 string
	Terms        map[IRI]Term
}

// Unit is one row of the UCUM to UN/CEFACT common code table.
type Unit struct {
	// UCUM is the Unified Code for Units of Measure code, e.g. "Cel".
	UCUM string
	// CommonCode is the UN/CEFACT common code (Recommendation 20), e.g. "CEL";
	// empty when upstream has none for this unit.
	CommonCode string
	Label      string
	// IRI identifies the unit in the QUDT vocabulary the row came from.
	IRI IRI
}

// UnitPack is the generated unit table.
type UnitPack struct {
	ID           string
	Version      string
	Licence      string
	SourceURL    string
	SourceSHA256 string
	// Units holds one row per UCUM code, sorted by UCUM code.
	Units []Unit
}

// TenantContext is a tenant's semantic configuration.
type TenantContext struct {
	// EnabledPacks are pack IDs.
	EnabledPacks []string
	// CustomPrefixes maps a tenant's own prefix to its namespace.
	CustomPrefixes map[string]string
}

// Limits.
const (
	// MaxCURIEBytes bounds a CURIE handed to Expand and Validate, and a
	// namespace.
	MaxCURIEBytes = 256
	// MaxIRIBytes bounds a full IRI handed to Expand and Validate. It is a
	// namespace plus a CURIE's local part, so every IRI Validate returns can be
	// handed back to Validate unchanged.
	MaxIRIBytes = 2 * MaxCURIEBytes
	// MaxCustomPrefixes bounds a tenant's custom prefixes.
	MaxCustomPrefixes = 32
)

// Sentinel errors. Match with errors.Is; the concrete error is a *TermError
// naming the offending term.
var (
	ErrUnknownPrefix  = errors.New("unknown prefix")
	ErrUnknownTerm    = errors.New("unknown term")
	ErrWrongKind      = errors.New("wrong kind")
	ErrDeprecatedTerm = errors.New("deprecated term")
	ErrMalformed      = errors.New("malformed")
	ErrPrefixShadow   = errors.New("prefix shadows a vocabulary")
	// ErrUnknownPack is a tenant context naming a pack this build does not ship.
	ErrUnknownPack = errors.New("unknown vocabulary pack")
	// ErrInvalidKind is a caller asking for a Kind that does not exist.
	ErrInvalidKind = errors.New("invalid kind")
	// ErrLimit is a tenant context over a size limit.
	ErrLimit = errors.New("limit exceeded")
)

// TermError is the error every refusal returns.
type TermError struct {
	// Err is one of the sentinel errors.
	Err error
	// Term is the offending CURIE, IRI or prefix, as the caller wrote it.
	Term string
	// Detail says why, for a human.
	Detail string
}

func (e *TermError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("semantic: %v: %q", e.Err, e.Term)
	}
	return fmt.Sprintf("semantic: %v: %q: %s", e.Err, e.Term, e.Detail)
}

// Unwrap lets errors.Is match the sentinel.
func (e *TermError) Unwrap() error { return e.Err }

func refuse(err error, term, format string, args ...any) *TermError {
	return &TermError{Err: err, Term: term, Detail: fmt.Sprintf(format, args...)}
}

// clone returns a copy that shares no slice or pointer with t.
func (t Term) clone() Term {
	c := t
	c.Parents = append([]IRI(nil), t.Parents...)
	if t.Inverse != nil {
		v := *t.Inverse
		c.Inverse = &v
	}
	return c
}

// clone returns a deep copy of p.
func (p *Pack) clone() *Pack {
	c := *p
	c.Terms = make(map[IRI]Term, len(p.Terms))
	for k, t := range p.Terms {
		c.Terms[k] = t.clone()
	}
	return &c
}
