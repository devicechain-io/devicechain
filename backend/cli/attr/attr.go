// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package attr implements `dcctl attr` — a scriptable door onto an entity's current-state
// attributes, which are the rows that ARE a classification facet's values.
//
// It is a client over the device-management GraphQL API as it stands (setEntityAttribute,
// entityAttributes, deleteEntityAttribute, facetKeys) and adds no server behaviour. What
// it does add is the one thing a script cannot cheaply get right on its own: the scope
// and value type a facet value must be written with.
//
// 🔴 THE DEFAULT SCOPE IS SHARED BECAUSE THAT IS THE ONLY SCOPE A FACET SELECTOR READS.
// Writing the right key at CLIENT or SERVER scope is valid, raises no error, reads back,
// and matches nothing in Browse or in a rule-scoped group. The same holds for the value
// type: "3" written as STRING against a LONG facet saves and never matches. So for a
// SHARED write the type is taken from the facet's declaration when --type is absent, and a
// --type that contradicts the declaration is refused here rather than saved.
package attr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Client is the GraphQL seam this package drives. *userclient.TenantSession satisfies it,
// which is what carries the caller's own tenant access token: the server authorizes every
// call as the person who ran the command.
type Client interface {
	Query(ctx context.Context, baseURL, query string, variables map[string]any, out any) error
}

// Kinds are the entity families that carry attributes (and so facet values) and that
// this command addresses by token.
var Kinds = []string{"device", "asset", "area", "customer"}

// Scopes and ValueTypes mirror the server's vocabularies; the server validates them too.
var (
	Scopes     = []string{"CLIENT", "SERVER", "SHARED"}
	ValueTypes = []string{"STRING", "LONG", "DOUBLE", "BOOLEAN", "JSON"}
)

// FacetScope is the one scope a facet's values are read from.
const FacetScope = "SHARED"

// pageSize is the per-round-trip page for reads; an entity's attributes are few.
const pageSize = 200

const (
	setMutation = `mutation($request:EntityAttributeSetRequest!){` +
		`setEntityAttribute(request:$request){entityType scope attrKey valueType value lastUpdated}}`
	unsetMutation = `mutation($entityType:String!,$entity:String!,$scope:String!,$attrKey:String!){` +
		`deleteEntityAttribute(entityType:$entityType,entity:$entity,scope:$scope,attrKey:$attrKey)}`
	getQuery = `query($criteria:EntityAttributeSearchCriteria!){` +
		`entityAttributes(criteria:$criteria){results{scope attrKey valueType value lastUpdated}` +
		`pagination{totalRecords}}}`
	facetKeysQuery = `query($criteria:FacetKeySearchCriteria!){` +
		`facetKeys(criteria:$criteria){results{key valueType}pagination{totalRecords}}}`
)

// Attribute is one attribute row as the API reports it.
type Attribute struct {
	Scope       string  `json:"scope"`
	Key         string  `json:"attrKey"`
	ValueType   string  `json:"valueType"`
	Value       *string `json:"value"`
	LastUpdated *string `json:"lastUpdated"`
}

// Target names the entity an operation acts on.
type Target struct {
	// Endpoint is the device-management GraphQL URL.
	Endpoint string
	Kind     string
	Token    string
}

func (t Target) validate() error {
	if strings.TrimSpace(t.Endpoint) == "" {
		return errors.New("no device-management endpoint was resolved")
	}
	if !contains(Kinds, t.Kind) {
		return fmt.Errorf("unknown entity kind %q: expected one of %s", t.Kind, strings.Join(Kinds, ", "))
	}
	if strings.TrimSpace(t.Token) == "" {
		return errors.New("the entity token is empty")
	}
	return nil
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// normScope upper-cases a scope and applies the default; ok is false for an unknown one.
func normScope(s, def string) (string, bool) {
	s = strings.ToUpper(s)
	if s == "" {
		s = def
	}
	return s, s == "" || contains(Scopes, s)
}

// SetOptions is one `attr set`.
type SetOptions struct {
	Target
	Key   string
	Value string
	// Scope defaults to FacetScope when empty.
	Scope string
	// ValueType is resolved from the facet's declaration when empty (SHARED scope only).
	ValueType string
}

// Set upserts one attribute and returns the row the server stored.
func Set(ctx context.Context, c Client, o SetOptions) (Attribute, error) {
	if err := o.validate(); err != nil {
		return Attribute{}, err
	}
	if strings.TrimSpace(o.Key) == "" {
		return Attribute{}, errors.New("the attribute key is empty")
	}
	scope, ok := normScope(o.Scope, FacetScope)
	if !ok {
		return Attribute{}, fmt.Errorf("unknown scope %q: expected one of %s", o.Scope, strings.Join(Scopes, ", "))
	}
	vt := strings.ToUpper(o.ValueType)
	if vt != "" && !contains(ValueTypes, vt) {
		return Attribute{}, fmt.Errorf("unknown value type %q: expected one of %s", o.ValueType, strings.Join(ValueTypes, ", "))
	}

	if scope == FacetScope {
		declared, found, err := declaredType(ctx, c, o.Target, o.Key)
		if err != nil {
			return Attribute{}, err
		}
		switch {
		case found && vt == "":
			vt = declared
		case found && vt != declared:
			return Attribute{}, fmt.Errorf("%s facet %q is declared as %s, and writing it as %s would save "+
				"but never match; omit --type to use the declared type", o.Kind, o.Key, declared, vt)
		}
	}
	if vt == "" {
		return Attribute{}, fmt.Errorf("%s key %q has no declared facet type to take, so --type is required (one of %s)",
			o.Kind, o.Key, strings.Join(ValueTypes, ", "))
	}

	var out struct {
		Set Attribute `json:"setEntityAttribute"`
	}
	vars := map[string]any{"request": map[string]any{
		"entityType": o.Kind, "entity": o.Token, "scope": scope,
		"attrKey": o.Key, "valueType": vt, "value": o.Value,
	}}
	if err := c.Query(ctx, o.Endpoint, setMutation, vars, &out); err != nil {
		return Attribute{}, fmt.Errorf("setting %s %q on %s %q: %w", scope, o.Key, o.Kind, o.Token, err)
	}
	return out.Set, nil
}

// declaredType looks the key up in the tenant's declared facet keys for the entity's
// family. found is false when the key is not declared as a facet.
func declaredType(ctx context.Context, c Client, t Target, key string) (string, bool, error) {
	seen := 0
	for page := 1; ; page++ {
		var out struct {
			Facets struct {
				Results []struct {
					Key       string `json:"key"`
					ValueType string `json:"valueType"`
				} `json:"results"`
				Pagination struct {
					TotalRecords *int `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"facetKeys"`
		}
		vars := map[string]any{"criteria": map[string]any{
			"pageNumber": page, "pageSize": pageSize, "memberType": t.Kind}}
		if err := c.Query(ctx, t.Endpoint, facetKeysQuery, vars, &out); err != nil {
			return "", false, fmt.Errorf("looking up the declared %s facets: %w", t.Kind, err)
		}
		for _, f := range out.Facets.Results {
			if f.Key == key {
				return f.ValueType, true, nil
			}
		}
		seen += len(out.Facets.Results)
		total := out.Facets.Pagination.TotalRecords
		if len(out.Facets.Results) == 0 || total == nil || seen >= *total {
			return "", false, nil
		}
	}
}

// GetOptions is one `attr get`.
type GetOptions struct {
	Target
	// Key narrows to one attribute key; empty lists every attribute.
	Key string
	// Scope narrows to one scope; empty lists every scope.
	Scope string
}

// Get reads an entity's attributes. A search on an entity that does not exist yields no
// rows rather than an error, so an empty answer is returned as such and the caller says
// so; it cannot tell a missing entity from one with no attributes.
func Get(ctx context.Context, c Client, o GetOptions) ([]Attribute, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	scope, ok := normScope(o.Scope, "")
	if !ok {
		return nil, fmt.Errorf("unknown scope %q: expected one of %s", o.Scope, strings.Join(Scopes, ", "))
	}
	var all []Attribute
	for page := 1; ; page++ {
		criteria := map[string]any{
			"pageNumber": page, "pageSize": pageSize,
			"entityType": o.Kind, "entity": o.Token,
		}
		if scope != "" {
			criteria["scope"] = scope
		}
		if o.Key != "" {
			criteria["attrKeys"] = []string{o.Key}
		}
		var out struct {
			Attrs struct {
				Results    []Attribute `json:"results"`
				Pagination struct {
					TotalRecords *int `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"entityAttributes"`
		}
		if err := c.Query(ctx, o.Endpoint, getQuery, map[string]any{"criteria": criteria}, &out); err != nil {
			return nil, fmt.Errorf("reading attributes of %s %q: %w", o.Kind, o.Token, err)
		}
		all = append(all, out.Attrs.Results...)
		total := out.Attrs.Pagination.TotalRecords
		if len(out.Attrs.Results) == 0 || total == nil || len(all) >= *total {
			return all, nil
		}
	}
}

// UnsetOptions is one `attr unset`.
type UnsetOptions struct {
	Target
	Key string
	// Scope defaults to FacetScope when empty.
	Scope string
}

// Unset removes one attribute by its natural key and reports whether a row was removed.
func Unset(ctx context.Context, c Client, o UnsetOptions) (bool, error) {
	if err := o.validate(); err != nil {
		return false, err
	}
	if strings.TrimSpace(o.Key) == "" {
		return false, errors.New("the attribute key is empty")
	}
	scope, ok := normScope(o.Scope, FacetScope)
	if !ok {
		return false, fmt.Errorf("unknown scope %q: expected one of %s", o.Scope, strings.Join(Scopes, ", "))
	}
	var out struct {
		Deleted bool `json:"deleteEntityAttribute"`
	}
	vars := map[string]any{"entityType": o.Kind, "entity": o.Token, "scope": scope, "attrKey": o.Key}
	if err := c.Query(ctx, o.Endpoint, unsetMutation, vars, &out); err != nil {
		return false, fmt.Errorf("removing %s %q from %s %q: %w", scope, o.Key, o.Kind, o.Token, err)
	}
	return out.Deleted, nil
}

// Print writes attributes as a table, or as a JSON array when asJSON is set.
func Print(w io.Writer, attrs []Attribute, asJSON bool) error {
	if asJSON {
		if attrs == nil {
			attrs = []Attribute{}
		}
		b, err := json.MarshalIndent(attrs, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCOPE\tKEY\tTYPE\tVALUE\tUPDATED")
	for _, a := range attrs {
		v, u := "", ""
		if a.Value != nil {
			v = *a.Value
		}
		if a.LastUpdated != nil {
			u = *a.LastUpdated
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.Scope, a.Key, a.ValueType, v, u)
	}
	return tw.Flush()
}
