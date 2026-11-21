// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"strings"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/introspection"
)

// Legacy contracts cannot establish support for preview interfaces: those may
// change in a minor. Refuse their vocabulary even when the old snapshot contains
// it. This deliberately conservative check can also refuse an alias sharing a
// preview name; actual discovery has no such restriction.
func legacyPreviewCapability(contract *graphql.Schema, query string, variables map[string]any) string {
	names, inputNames, enumValues := map[string]bool{}, map[string]bool{}, map[string]bool{}
	includeDeprecated := &struct{ IncludeDeprecated bool }{true}
	for _, typ := range contract.Inspect().Types() {
		if typ.Name() == nil || strings.HasPrefix(*typ.Name(), "__") {
			continue
		}
		if previewDescription(typ.Description()) {
			names[*typ.Name()] = true
			// Generic member names (token, name, state) on an entirely preview
			// type do not make those names preview on unrelated stable types.
			continue
		}
		if fields := typ.Fields(includeDeprecated); fields != nil {
			for _, field := range *fields {
				if previewDescription(field.Description()) || previewType(field.Type()) {
					names[field.Name()] = true
					continue
				}
				for _, arg := range field.Args(includeDeprecated) {
					if previewDescription(arg.Description()) || previewType(arg.Type()) {
						inputNames[arg.Name()] = true
					}
				}
			}
		}
		if fields := typ.InputFields(includeDeprecated); fields != nil {
			for _, field := range *fields {
				if previewDescription(field.Description()) || previewType(field.Type()) {
					inputNames[field.Name()] = true
				}
			}
		}
		if values := typ.EnumValues(includeDeprecated); values != nil {
			for _, value := range *values {
				if previewDescription(value.Description()) {
					enumValues[value.Name()] = true
				}
			}
		}
	}
	identifiers := graphqlIdentifiers(query)
	for i, name := range identifiers {
		input := inputNames[name] && i+1 < len(identifiers) && identifiers[i+1] == ":"
		if names[name] || enumValues[name] || input {
			return name
		}
	}
	var checkValue func(any) string
	checkValue = func(value any) string {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				if inputNames[key] {
					return key
				}
				if name := checkValue(child); name != "" {
					return name
				}
			}
		case []any:
			for _, child := range v {
				if name := checkValue(child); name != "" {
					return name
				}
			}
		case string:
			if enumValues[v] {
				return v
			}
		}
		return ""
	}
	return checkValue(variables)
}

func previewDescription(description *string) bool {
	return description != nil && strings.Contains(*description, "(Preview:")
}

func previewType(typ *introspection.Type) bool {
	for typ != nil && (typ.Kind() == "NON_NULL" || typ.Kind() == "LIST") {
		typ = typ.OfType()
	}
	return typ != nil && previewDescription(typ.Description())
}

// graphqlIdentifiers extracts names and input separators; the real validator has
// already checked syntax. Comments and strings cannot select a feature.
func graphqlIdentifiers(query string) []string {
	var names []string
	for i := 0; i < len(query); {
		switch {
		case query[i] == '#':
			for i < len(query) && query[i] != '\n' && query[i] != '\r' {
				i++
			}
		case query[i] == '"':
			block := strings.HasPrefix(query[i:], "\"\"\"")
			i++
			if block {
				i += 2
			}
			for i < len(query) {
				if query[i] == '\\' {
					if block && strings.HasPrefix(query[i+1:], "\"\"\"") {
						i += 4
					} else if block {
						i++
					} else {
						i += 2
					}
				} else if block && strings.HasPrefix(query[i:], "\"\"\"") {
					i += 3
					break
				} else if !block && query[i] == '"' {
					i++
					break
				} else {
					i++
				}
			}
		case query[i] == ':':
			names = append(names, ":")
			i++
		case query[i] == '_' || query[i] >= 'A' && query[i] <= 'Z' || query[i] >= 'a' && query[i] <= 'z':
			start := i
			for i < len(query) && (query[i] == '_' || query[i] >= 'A' && query[i] <= 'Z' || query[i] >= 'a' && query[i] <= 'z' || query[i] >= '0' && query[i] <= '9') {
				i++
			}
			names = append(names, query[start:i])
		default:
			i++
		}
	}
	return names
}
