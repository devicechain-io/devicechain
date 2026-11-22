// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"strings"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/ast"
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
		if i > 0 && identifiers[i-1] == "$" {
			continue // A variable's name does not select a capability.
		}
		input := inputNames[name] && i+1 < len(identifiers) && identifiers[i+1] == ":"
		if names[name] || enumValues[name] || input {
			return name
		}
	}
	// Only declarations have the token sequence $ name : Type. The authoritative
	// GraphQL validator has already checked syntax and resolved input types. Read
	// those declarations, then walk JSON values using the schema's actual types;
	// scalar strings and unrelated input fields/enums cannot select preview names.
	for i := 0; i+3 < len(identifiers); i++ {
		if identifiers[i] != "$" || identifiers[i+2] != ":" {
			continue
		}
		typ, _ := legacyVariableType(contract.AST(), identifiers, i+3)
		if name := legacyPreviewInput(typ, variables[identifiers[i+1]]); name != "" {
			return name
		}
	}
	return ""
}

func legacyVariableType(schema *ast.Schema, tokens []string, i int) (ast.Type, int) {
	if i >= len(tokens) {
		return nil, i
	}
	var typ ast.Type
	if tokens[i] == "[" {
		inner, end := legacyVariableType(schema, tokens, i+1)
		if end >= len(tokens) || tokens[end] != "]" {
			return nil, end
		}
		typ, i = &ast.List{OfType: inner}, end+1
	} else {
		typ, i = schema.Types[tokens[i]], i+1
	}
	if i < len(tokens) && tokens[i] == "!" {
		typ, i = &ast.NonNull{OfType: typ}, i+1
	}
	return typ, i
}

func legacyPreviewInput(typ ast.Type, value any) string {
	if value == nil {
		return ""
	}
	if named, ok := typ.(ast.NamedType); ok {
		description := named.Description()
		if previewDescription(&description) {
			return named.TypeName()
		}
	}
	switch t := typ.(type) {
	case *ast.NonNull:
		return legacyPreviewInput(t.OfType, value)
	case *ast.List:
		if values, ok := value.([]any); ok {
			for _, child := range values {
				if name := legacyPreviewInput(t.OfType, child); name != "" {
					return name
				}
			}
		} else {
			// GraphQL coerces a single value into a one-element input list.
			return legacyPreviewInput(t.OfType, value)
		}
	case *ast.EnumTypeDefinition:
		text, _ := value.(string)
		for _, enum := range t.EnumValuesDefinition {
			if enum.EnumValue == text && previewDescription(&enum.Desc) {
				return text
			}
		}
	case *ast.InputObject:
		object, _ := value.(map[string]any)
		for _, field := range t.Values {
			if child, exists := object[field.Name.Name]; exists {
				if previewDescription(&field.Desc) {
					return field.Name.Name
				}
				if name := legacyPreviewInput(field.Type, child); name != "" {
					return name
				}
			}
		}
	}
	return ""
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

// graphqlIdentifiers extracts names, input separators and variable type syntax;
// the real validator has already checked syntax. Comments and strings cannot
// select a feature.
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
		case strings.ContainsRune(":$[]!", rune(query[i])):
			names = append(names, string(query[i]))
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
