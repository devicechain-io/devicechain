// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schemaplane_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
)

// These strings have phantom TypeScript types, but no codegen checks their GraphQL.
// Run uncached: go test -count=1 ./graphql/schemaplane (from backend/core).
// Both the frontend sources and served SDL live outside this module.
var frontendDocumentGroups = []struct {
	file  string
	area  string
	names string
}{
	{"packages/dashboards/src/queries.ts", "device-management", "CUSTOMER_DEVICES ANCHOR_DEVICES DEVICES_BY_TOKEN DEVICE_NAMES DECLARED_METRICS LIST_DEVICES LIST_CUSTOMERS LIST_AREAS LIST_ASSETS"},
	{"packages/dashboards/src/queries.ts", "event-management", "BUCKETED_MEASUREMENTS"},
	{"packages/dashboards/src/internal/alarm-doc.ts", "device-management", "ALARMS_QUERY ALARM_DEVICE_TYPES ALARM_TYPE_COUNTS ALARM_SEVERITY_COUNTS ALARM_STREAM ACKNOWLEDGE_ALARM CLEAR_ALARM"},
	{"packages/dashboards/src/internal/command-doc.ts", "command-delivery", "COMMANDS_QUERY CREATE_COMMAND"},
	{"packages/dashboards/src/internal/location-doc.ts", "device-state", "LATEST_LOCATIONS_QUERY"},
	{"packages/dashboards/src/internal/presence-doc.ts", "device-state", "DEVICE_PRESENCE_QUERY"},
	{"packages/dashboards/src/internal/measurement-doc.ts", "event-management", "MEASUREMENT_STREAM MEASUREMENT_STREAM_MULTI"},
	{"apps/dashboard/src/queries.ts", "user-management", "LOGIN VERIFY_MFA SELECT_TENANT REFRESH BEGIN_SSO COMPLETE_SSO LOGOUT TENANT_BASEMAP"},
	{"apps/dashboard/src/queries.ts", "dashboard-management", "PUBLISHED_DASHBOARD DASHBOARDS"},
}

var frontendCast = regexp.MustCompile(`\bas\s+(?:unknown\s+as\s+)?TypedDocument\s*<`)
var frontendLiteral = regexp.MustCompile("(?s)\\bconst\\s+([A-Za-z_$][A-Za-z0-9_$]*)\\s*=\\s*(`[^`]*`|'[^'\\r\\n]*'|\"[^\"\\r\\n]*\")\\s+as\\s+(?:unknown\\s+as\\s+)?TypedDocument\\s*<")
var frontendOperation = regexp.MustCompile(`^\s*(query|mutation|subscription)\b`)

// This deliberately supports only literal documents. A new expression, escape or
// interpolation must extend the reader explicitly, rather than disappear from coverage.
func frontendLiterals(source string) (map[string]string, error) {
	matches := frontendLiteral.FindAllStringSubmatch(source, -1)
	if casts := len(frontendCast.FindAllStringIndex(source, -1)); casts != len(matches) {
		return nil, fmt.Errorf("found %d TypedDocument casts but read %d literal documents; unsupported document expression", casts, len(matches))
	}
	out := map[string]string{}
	for _, m := range matches {
		name, doc := m[1], m[2][1:len(m[2])-1]
		if strings.Contains(doc, "\\") || strings.Contains(doc, "${") {
			return nil, fmt.Errorf("%s: escapes and interpolation are unsupported in document literals", name)
		}
		if _, exists := out[name]; exists {
			return nil, fmt.Errorf("duplicate document %s", name)
		}
		out[name] = doc
	}
	return out, nil
}

func frontendDocumentErrors(schema *graphql.Schema, doc string) []string {
	op := frontendOperation.FindStringSubmatch(doc)
	if op == nil {
		return []string{"expected an explicit query, mutation or subscription"}
	}
	// graphql-go skips validation when an operation's root is absent.
	if _, ok := schema.AST().RootOperationTypes[op[1]]; !ok {
		return []string{"served schema has no " + op[1] + " root"}
	}
	var errors []string
	for _, err := range schema.Validate(doc) {
		// These are documents, without runtime variable values. As in docsgql,
		// omit only the rule for missing values; variable declarations/usages,
		// input types, fields and arguments are still checked by the server.
		if err.Rule != "VariablesOfCorrectType" {
			errors = append(errors, err.Error())
		}
	}
	return errors
}

func TestFrontendDocumentsAgainstServedSDL(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "frontend")
	documents := map[string]string{}
	for _, tree := range []string{"packages/dashboards/src", "apps/dashboard/src"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || (!strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx")) || strings.Contains(entry.Name(), ".test.") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			docs, err := frontendLiterals(string(data))
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			for name, doc := range docs {
				documents[filepath.ToSlash(rel)+":"+name] = doc
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	schemas := map[string]*graphql.Schema{}
	covered := map[string]bool{}
	for _, group := range frontendDocumentGroups {
		schema := schemas[group.area]
		if schema == nil {
			sdl, served, err := schemaplane.SDLAt(filepath.Join(servicesTree, group.area, "graphql"), schemaplane.MountTenant)
			if err != nil || !served {
				t.Fatalf("%s tenant SDL: served=%v, error=%v", group.area, served, err)
			}
			schema, err = graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers(), graphql.MaxDepth(15), graphql.MaxQueryLength(100000))
			if err != nil {
				t.Fatalf("parse %s: %v", group.area, err)
			}
			schemas[group.area] = schema
		}
		for _, name := range strings.Fields(group.names) {
			key := group.file + ":" + name
			if covered[key] {
				t.Fatalf("duplicate validation table entry %s", key)
			}
			covered[key] = true
			doc, ok := documents[key]
			if !ok {
				t.Errorf("validation table entry %s has no source document", key)
				continue
			}
			t.Run(key, func(t *testing.T) {
				if errors := frontendDocumentErrors(schema, doc); len(errors) != 0 {
					t.Fatalf("%s against %s: %v", key, group.area, errors)
				}
				// Reach control: poison the actual document, keeping its operation
				// kind and variables. A validator that never visits it must fail.
				broken := strings.Replace(doc, "{", "{ frontendDocumentFieldThatDoesNotExist ", 1)
				errors := frontendDocumentErrors(schema, broken)
				if !strings.Contains(strings.Join(errors, "\n"), "frontendDocumentFieldThatDoesNotExist") {
					t.Fatalf("invalid-field control was not reached: %v", errors)
				}
			})
		}
	}
	for key := range documents {
		if !covered[key] {
			t.Errorf("unvalidated TypedDocument cast %s: add its served area to frontendDocumentGroups", key)
		}
	}
}

func TestFrontendDocumentReaderReach(t *testing.T) {
	for _, quote := range []string{"`", "'", "\""} {
		source := "export const NEW_DOCUMENT =\n" + quote + "query { token }" + quote + " as unknown as TypedDocument<Result, Vars>;"
		docs, err := frontendLiterals(source)
		if err != nil || len(docs) != 1 || docs["NEW_DOCUMENT"] != "query { token }" {
			t.Fatalf("quote %s: documents=%v, error=%v", quote, docs, err)
		}
	}
	for _, source := range []string{
		"const DOC = makeDocument() as unknown as TypedDocument<Result, Vars>;",
		"const DOC = `query { ${field} }` as unknown as TypedDocument<Result, Vars>;",
		"const DOC = 'query { \\u0074oken }' as unknown as TypedDocument<Result, Vars>;",
	} {
		if _, err := frontendLiterals(source); err == nil {
			t.Errorf("unsupported document was silently accepted: %s", source)
		}
	}
}
