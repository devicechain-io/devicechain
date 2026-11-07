// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package semantic

import (
	"fmt"
	"sort"
	"strings"
)

// Registry is the immutable, concurrency-safe index over a set of packs.
type Registry struct {
	packs    map[string]*Pack // by ID
	prefixes map[string]*Pack // by prefix
	ordered  []*Pack          // by ID, for stable iteration
	terms    map[IRI]Term     // every term of every pack

	unitPack   *UnitPack
	byUCUM     map[string]Unit
	byCommon   map[string]Unit
	unitsOrder []Unit
}

// NewRegistry indexes packs and an optional unit table. It refuses a set that
// is not internally consistent -- duplicate ids or prefixes, overlapping
// namespaces, a term filed under the wrong key or outside its pack's
// namespace, an unknown kind -- because a generated table that is wrong must
// stop the process, not validate bindings against a shape nobody reviewed.
func NewRegistry(packs []*Pack, units *UnitPack) (*Registry, error) {
	r := &Registry{
		packs:    map[string]*Pack{},
		prefixes: map[string]*Pack{},
		terms:    map[IRI]Term{},
	}
	for _, p := range packs {
		if p == nil {
			return nil, fmt.Errorf("semantic: nil pack")
		}
		if !validPackID(p.ID) {
			return nil, fmt.Errorf("semantic: pack id %q is not [a-z0-9-]+", p.ID)
		}
		if !validPrefix(p.Prefix) {
			return nil, fmt.Errorf("semantic: pack %q prefix %q is not a valid prefix", p.ID, p.Prefix)
		}
		if err := checkNamespace(p.Namespace); err != nil {
			return nil, fmt.Errorf("semantic: pack %q namespace: %w", p.ID, err)
		}
		if _, dup := r.packs[p.ID]; dup {
			return nil, fmt.Errorf("semantic: duplicate pack id %q", p.ID)
		}
		if other, dup := r.prefixes[p.Prefix]; dup {
			return nil, fmt.Errorf("semantic: packs %q and %q share prefix %q", other.ID, p.ID, p.Prefix)
		}
		for _, o := range packs {
			if o != p && o != nil && namespacesOverlap(o.Namespace, p.Namespace) {
				return nil, fmt.Errorf("semantic: packs %q and %q have overlapping namespaces", o.ID, p.ID)
			}
		}
		r.packs[p.ID] = p
		r.prefixes[p.Prefix] = p
		r.ordered = append(r.ordered, p)
		for key, t := range p.Terms {
			switch {
			case key != t.IRI:
				return nil, fmt.Errorf("semantic: pack %q term %q is filed under %q", p.ID, t.IRI, key)
			case !strings.HasPrefix(string(key), p.Namespace) || len(key) == len(p.Namespace):
				return nil, fmt.Errorf("semantic: pack %q term %q is outside namespace %q", p.ID, key, p.Namespace)
			case !t.Kind.Valid():
				return nil, fmt.Errorf("semantic: pack %q term %q has invalid kind %q", p.ID, key, t.Kind)
			}
			if _, dup := r.terms[key]; dup {
				return nil, fmt.Errorf("semantic: term %q is defined twice", key)
			}
			r.terms[key] = t
		}
	}
	sort.Slice(r.ordered, func(i, j int) bool { return r.ordered[i].ID < r.ordered[j].ID })

	if units != nil {
		r.unitPack = units
		r.byUCUM = make(map[string]Unit, len(units.Units))
		r.byCommon = map[string]Unit{}
		for _, u := range units.Units {
			if u.UCUM == "" {
				return nil, fmt.Errorf("semantic: unit table has a row with no UCUM code (%q)", u.IRI)
			}
			if _, dup := r.byUCUM[u.UCUM]; dup {
				return nil, fmt.Errorf("semantic: unit table lists UCUM code %q twice", u.UCUM)
			}
			r.byUCUM[u.UCUM] = u
			r.unitsOrder = append(r.unitsOrder, u)
			if u.CommonCode == "" {
				continue
			}
			// Several UCUM spellings can share one common code; the reverse
			// lookup answers with the shortest, then the first in sort order,
			// which is the same on every build.
			if cur, ok := r.byCommon[u.CommonCode]; !ok || len(u.UCUM) < len(cur.UCUM) ||
				(len(u.UCUM) == len(cur.UCUM) && u.UCUM < cur.UCUM) {
				r.byCommon[u.CommonCode] = u
			}
		}
		sort.Slice(r.unitsOrder, func(i, j int) bool { return r.unitsOrder[i].UCUM < r.unitsOrder[j].UCUM })
	}
	return r, nil
}

// Packs lists the registry's packs ordered by id.
func (r *Registry) Packs() []*Pack { return append([]*Pack(nil), r.ordered...) }

// Pack returns a pack by id.
func (r *Registry) Pack(id string) (*Pack, bool) {
	p, ok := r.packs[id]
	return p, ok
}

// Term returns the term with this expanded IRI from any pack, enabled or not.
func (r *Registry) Term(iri IRI) (Term, bool) {
	t, ok := r.terms[iri]
	return t, ok
}

// IsA reports whether iri is ancestor or reaches it through Parents. It is
// reflexive for a known term, transitive, and safe on cyclic data. A term the
// registry does not know -- in particular any custom-prefix term -- is never
// anything: false, not true.
func (r *Registry) IsA(iri, ancestor IRI) bool {
	if _, ok := r.terms[iri]; !ok {
		return false
	}
	if iri == ancestor {
		return true
	}
	seen := map[IRI]struct{}{iri: {}}
	queue := []IRI{iri}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, parent := range r.terms[cur].Parents {
			if parent == ancestor {
				return true
			}
			if _, done := seen[parent]; done {
				continue
			}
			seen[parent] = struct{}{}
			if _, known := r.terms[parent]; known {
				queue = append(queue, parent)
			}
		}
	}
	return false
}

// UnitByUCUM looks a unit up by its UCUM code.
func (r *Registry) UnitByUCUM(code string) (Unit, bool) {
	u, ok := r.byUCUM[code]
	return u, ok
}

// UnitByCommonCode looks a unit up by its UN/CEFACT common code.
func (r *Registry) UnitByCommonCode(code string) (Unit, bool) {
	u, ok := r.byCommon[code]
	return u, ok
}

// Units lists the unit table ordered by UCUM code.
func (r *Registry) Units() []Unit { return append([]Unit(nil), r.unitsOrder...) }

// UnitPack returns the unit table's provenance (its rows are served by Units).
func (r *Registry) UnitPack() (UnitPack, bool) {
	if r.unitPack == nil {
		return UnitPack{}, false
	}
	p := *r.unitPack
	p.Units = nil
	return p, true
}

// Resolution is what Resolve learned about an accepted term.
type Resolution struct {
	IRI IRI
	// Opaque is true for a custom-prefix term: accepted, never reasoned over.
	Opaque bool
	// Pack is the owning pack's id; empty when Opaque.
	Pack string
	// Term is the pack's entry; zero when Opaque.
	Term Term
}

// Expand turns a CURIE (or an in-namespace full IRI) into its IRI. It checks
// the prefix and the tenant context, not the term itself; Validate does that.
func (r *Registry) Expand(curie string, ctx TenantContext) (IRI, error) {
	iri, _, _, err := r.locate(curie, ctx)
	return iri, err
}

// Validate expands curie and checks it against wantKind, returning the
// EXPANDED IRI for storage.
func (r *Registry) Validate(curie string, wantKind Kind, ctx TenantContext) (IRI, error) {
	res, err := r.Resolve(curie, wantKind, ctx)
	if err != nil {
		return "", err
	}
	return res.IRI, nil
}

// Resolve is Validate with the evidence: whether the term was opaque, which
// pack owns it, and its entry.
func (r *Registry) Resolve(curie string, wantKind Kind, ctx TenantContext) (Resolution, error) {
	if !wantKind.Valid() {
		return Resolution{}, refuse(ErrInvalidKind, curie, "%q is not CLASS, PREDICATE or OBSERVABLE_PROPERTY", string(wantKind))
	}
	iri, pack, custom, err := r.locate(curie, ctx)
	if err != nil {
		return Resolution{}, err
	}
	if custom {
		return Resolution{IRI: iri, Opaque: true}, nil
	}
	t, ok := pack.Terms[iri]
	if !ok {
		return Resolution{}, refuse(ErrUnknownTerm, curie, "%s is not a term of pack %q (%s)", iri, pack.ID, pack.Version)
	}
	if t.Kind != wantKind {
		return Resolution{}, refuse(ErrWrongKind, curie, "%s is a %s, not a %s", iri, t.Kind, wantKind)
	}
	if t.Deprecated {
		if t.ReplacedBy != "" {
			return Resolution{}, refuse(ErrDeprecatedTerm, curie, "%s is deprecated and may not be used in a new binding; its replacement is %s", iri, t.ReplacedBy)
		}
		return Resolution{}, refuse(ErrDeprecatedTerm, curie, "%s is deprecated and may not be used in a new binding", iri)
	}
	return Resolution{IRI: iri, Pack: pack.ID, Term: t}, nil
}

// locate does everything Expand and Validate share. Exactly one of pack and
// custom describes the owner on success.
func (r *Registry) locate(curie string, ctx TenantContext) (IRI, *Pack, bool, error) {
	if curie == "" {
		return "", nil, false, refuse(ErrMalformed, curie, "empty")
	}
	if len(curie) > MaxCURIEBytes {
		return "", nil, false, refuse(ErrMalformed, curie[:32]+"...", "%d bytes exceeds the %d byte limit", len(curie), MaxCURIEBytes)
	}
	if bad := badChar(curie); bad != 0 {
		return "", nil, false, refuse(ErrMalformed, curie, "contains the character %q", bad)
	}
	enabled, customs, err := r.resolveContext(ctx)
	if err != nil {
		return "", nil, false, err
	}

	if isFullIRI(curie) {
		for _, p := range enabled {
			if strings.HasPrefix(curie, p.Namespace) {
				if len(curie) == len(p.Namespace) {
					return "", nil, false, refuse(ErrMalformed, curie, "a namespace with no term")
				}
				return IRI(curie), p, false, nil
			}
		}
		for _, c := range customs {
			if strings.HasPrefix(curie, c.namespace) {
				if len(curie) == len(c.namespace) {
					return "", nil, false, refuse(ErrMalformed, curie, "a namespace with no term")
				}
				return IRI(curie), nil, true, nil
			}
		}
		return "", nil, false, refuse(ErrUnknownPrefix, curie, "no enabled vocabulary or custom namespace contains this IRI")
	}

	colon := strings.IndexByte(curie, ':')
	if colon < 0 {
		return "", nil, false, refuse(ErrMalformed, curie, "a bare word: there is no default prefix, write prefix:term")
	}
	prefix, local := curie[:colon], curie[colon+1:]
	if !validPrefix(prefix) {
		return "", nil, false, refuse(ErrMalformed, curie, "%q is not a valid prefix", prefix)
	}
	if local == "" || strings.HasPrefix(local, "//") {
		return "", nil, false, refuse(ErrMalformed, curie, "the part after the colon is empty or starts with //")
	}
	for _, p := range enabled {
		if p.Prefix == prefix {
			return IRI(p.Namespace + local), p, false, nil
		}
	}
	for _, c := range customs {
		if c.prefix == prefix {
			return IRI(c.namespace + local), nil, true, nil
		}
	}
	if p, ok := r.prefixes[prefix]; ok {
		return "", nil, false, refuse(ErrUnknownPrefix, curie, "pack %q is not enabled for this tenant", p.ID)
	}
	return "", nil, false, refuse(ErrUnknownPrefix, curie, "prefix %q is neither an enabled pack nor a custom prefix", prefix)
}

func isFullIRI(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "urn:")
}

// badChar returns the first character no CURIE or IRI may contain, or 0.
func badChar(s string) rune {
	for _, c := range s {
		if c <= ' ' || c == 0x7f || strings.ContainsRune("<>\"{}|\\^`", c) {
			return c
		}
	}
	return 0
}

func validPackID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// validPrefix accepts an XML-NCName-like prefix: a letter, then letters,
// digits, '.', '_' or '-'.
func validPrefix(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i, c := range s {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if i == 0 && !letter {
			return false
		}
		if !letter && !(c >= '0' && c <= '9') && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// checkNamespace requires an https:// (with a host) or urn: namespace.
func checkNamespace(ns string) error {
	if len(ns) > MaxCURIEBytes {
		return fmt.Errorf("%d bytes exceeds the %d byte limit", len(ns), MaxCURIEBytes)
	}
	if bad := badChar(ns); bad != 0 {
		return fmt.Errorf("contains the character %q", bad)
	}
	switch {
	case strings.HasPrefix(ns, "https://"):
		rest := ns[len("https://"):]
		host := rest
		if i := strings.IndexAny(rest, "/?#"); i >= 0 {
			host = rest[:i]
		}
		if host == "" {
			return fmt.Errorf("an https namespace needs a host")
		}
		return nil
	case strings.HasPrefix(ns, "urn:"):
		if len(ns) <= len("urn:") {
			return fmt.Errorf("a urn namespace needs content")
		}
		return nil
	}
	return fmt.Errorf("must start with https:// or urn:")
}

func namespacesOverlap(a, b string) bool {
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

type customPrefix struct{ prefix, namespace string }

// resolveContext validates a tenant context against this registry and returns
// the enabled packs (ordered by id) and custom prefixes (ordered by prefix).
func (r *Registry) resolveContext(ctx TenantContext) ([]*Pack, []customPrefix, error) {
	var enabled []*Pack
	seen := map[string]bool{}
	for _, id := range ctx.EnabledPacks {
		if seen[id] {
			continue
		}
		seen[id] = true
		p, ok := r.packs[id]
		if !ok {
			return nil, nil, refuse(ErrUnknownPack, id, "this build ships no such pack")
		}
		enabled = append(enabled, p)
	}
	sort.Slice(enabled, func(i, j int) bool { return enabled[i].ID < enabled[j].ID })

	if len(ctx.CustomPrefixes) > MaxCustomPrefixes {
		return nil, nil, refuse(ErrLimit, fmt.Sprintf("%d custom prefixes", len(ctx.CustomPrefixes)), "at most %d are allowed", MaxCustomPrefixes)
	}
	customs := make([]customPrefix, 0, len(ctx.CustomPrefixes))
	for prefix, ns := range ctx.CustomPrefixes {
		customs = append(customs, customPrefix{prefix, ns})
	}
	sort.Slice(customs, func(i, j int) bool { return customs[i].prefix < customs[j].prefix })

	for i, c := range customs {
		if !validPrefix(c.prefix) {
			return nil, nil, refuse(ErrMalformed, c.prefix, "not a valid custom prefix")
		}
		switch strings.ToLower(c.prefix) {
		case "http", "https", "urn":
			return nil, nil, refuse(ErrMalformed, c.prefix, "a URI scheme cannot be used as a prefix")
		}
		if p, ok := r.prefixes[c.prefix]; ok {
			return nil, nil, refuse(ErrPrefixShadow, c.prefix, "shadows the prefix of pack %q", p.ID)
		}
		if err := checkNamespace(c.namespace); err != nil {
			return nil, nil, refuse(ErrMalformed, c.prefix+" -> "+truncate(c.namespace), "%v", err)
		}
		for _, p := range r.ordered {
			if namespacesOverlap(c.namespace, p.Namespace) {
				return nil, nil, refuse(ErrPrefixShadow, c.prefix, "namespace %s overlaps the namespace of pack %q", c.namespace, p.ID)
			}
		}
		for _, o := range customs[:i] {
			if namespacesOverlap(c.namespace, o.namespace) {
				return nil, nil, refuse(ErrPrefixShadow, c.prefix, "namespace %s overlaps custom prefix %q", c.namespace, o.prefix)
			}
		}
	}
	return enabled, customs, nil
}

// ValidateContext checks a tenant context on its own, for a caller that is
// about to store it. It returns the same errors Validate would.
func (r *Registry) ValidateContext(ctx TenantContext) error {
	_, _, err := r.resolveContext(ctx)
	return err
}

func truncate(s string) string {
	if len(s) > 48 {
		return s[:48] + "..."
	}
	return s
}
