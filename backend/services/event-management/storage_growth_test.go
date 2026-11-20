// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

// The deletion watermark and device token holder tables are reported through the shared
// storage-growth collector, so the chart's EntityDeletionWatermarksLarge alert and the
// persistence dashboard can see them. Both grow for the life of the tenant and nothing prunes
// either. The table names are written out rather than derived from the list: comparing a list
// with itself passes for any list, including an empty one.
func TestUnprunedTablesAreReportedForGrowth(t *testing.T) {
	var tables []string
	for _, m := range appendOnlyModels {
		parsed, err := schema.Parse(m, &sync.Map{}, schema.NamingStrategy{})
		require.NoError(t, err)
		tables = append(tables, parsed.Table)
	}
	require.Equal(t, []string{"entity_deletion_watermarks", "device_token_holders"}, tables)
}

// The list only matters if the service hands it to the registration. Nothing else exercises
// main.go's wiring, so read the spec literal: a nil or a different list fails here.
func TestEventManagementSpecRegistersTheAppendOnlyList(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	require.NoError(t, err)
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "RdbSpec" {
			return true
		}
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok || kv.Key.(*ast.Ident).Name != "AppendOnly" {
				continue
			}
			if id, ok := kv.Value.(*ast.Ident); ok {
				found = append(found, id.Name)
			} else {
				found = append(found, "<not appendOnlyModels>")
			}
		}
		return true
	})
	require.Equal(t, []string{"appendOnlyModels"}, found)
}
