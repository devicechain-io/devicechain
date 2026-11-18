// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	gql "github.com/graph-gophers/graphql-go"
)

// Validate the actual console documents and their paging contract. SDL validation
// alone accepts a fixed first page that silently hides later options. Run with
// -count=1: these TypeScript sources live outside the Go module's test cache.
func TestConsoleGroupPickersCanReadEveryPage(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, nil)
	for _, tc := range []struct{ file, operation string }{
		{"device-management.ts", "DeviceGroupTargets"},
		{"device-management.ts", "ScopeGroups"},
		{"browse.ts", "DynamicGroups"},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "frontend", "apps", "console", "src", "lib", "api", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			pattern := "(?s)graphql\\(`\\s*(query " + tc.operation + "\\b.*?)`\\)"
			matches := regexp.MustCompile(pattern).FindAllStringSubmatch(string(source), -1)
			if len(matches) != 1 {
				t.Fatalf("found %d documents for %s, want exactly one", len(matches), tc.operation)
			}
			document := matches[0][1]
			if errs := schema.ValidateWithVariables(document, map[string]any{
				"criteria": map[string]any{"pageNumber": 2, "pageSize": 200},
			}); len(errs) != 0 {
				t.Fatalf("picker does not validate against served SDL: %v", errs)
			}
			if !consolePickerHasPaging(document) {
				t.Fatal("picker must accept criteria and select totalRecords; a fixed first page hides options")
			}
		})
	}
}

func consolePickerHasPaging(document string) bool {
	compact := strings.Join(strings.Fields(document), " ")
	return strings.Contains(compact, "$criteria: EntityGroupSearchCriteria!") &&
		strings.Contains(compact, "entityGroups(criteria: $criteria)") &&
		strings.Contains(compact, "pagination { totalRecords }")
}

func TestConsolePickerPagingGuardRejectsTheFormerFirstPage(t *testing.T) {
	if consolePickerHasPaging(`query Picker { entityGroups(criteria: {pageNumber: 1, pageSize: 500}) { results { token } } }`) {
		t.Fatal("paging guard accepted the silent first-page truncation")
	}
}
