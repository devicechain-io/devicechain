// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAttributeWritersGoThroughPrimitive(t *testing.T) {
	start := time.Now()
	sources, err := attributeWriterSources("..")
	if err != nil {
		t.Fatal(err)
	}
	violations, err := scanAttributeWriterPackages(sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Error(violation)
	}
	t.Logf("scanned %d production files in %s", len(sources), time.Since(start))
	if time.Since(start) >= 10*time.Second {
		t.Fatal("attribute writer guard exceeded 10 seconds")
	}
}

const attributeScratchPrelude = `package model
import (
 "fmt"
 "gorm.io/gorm"
 "gorm.io/gorm/clause"
)
type EntityAttribute struct{}
type AttributeGeneration struct{}
type Other struct{}
type API struct{}
func (api *API) attrsFor() []EntityAttribute { return nil }
func (api *API) headsFor() ([]AttributeGeneration, error) { return nil, nil }
`

func TestAttributeWriterPlantedViolations(t *testing.T) {
	cases := map[string]string{
		"raw scan insert":        `func scratch(tx *gorm.DB) { tx.Raw("INSERT INTO entity_attributes DEFAULT VALUES RETURNING *").Scan(new(Other)) }`,
		"raw rows update":        `func scratch(tx *gorm.DB) { tx.Raw("UPDATE entity_attribute_generations SET generation=2 RETURNING *").Rows() }`,
		"raw row delete":         `func scratch(tx *gorm.DB) { tx.Raw("DELETE FROM entity_attributes RETURNING *").Row() }`,
		"batch create":           `func scratch(tx *gorm.DB) { tx.CreateInBatches([]EntityAttribute{{}}, 10) }`,
		"first or create":        `func scratch(tx *gorm.DB) { tx.FirstOrCreate(&AttributeGeneration{}) }`,
		"first or init save":     `func scratch(tx *gorm.DB) { var row EntityAttribute; tx.FirstOrInit(&row); tx.Save(&row) }`,
		"assign first or create": `func scratch(tx *gorm.DB) { tx.Assign(EntityAttribute{}).FirstOrCreate(&EntityAttribute{}) }`,
		"attrs first or create":  `func scratch(tx *gorm.DB) { tx.Attrs(EntityAttribute{}).FirstOrCreate(&EntityAttribute{}) }`,
		"association append":     `func scratch(tx *gorm.DB) { tx.Model(&Other{}).Association("Attributes").Append(&EntityAttribute{}) }`,
		"association replace":    `func scratch(tx *gorm.DB) { tx.Model(&Other{}).Association("Attributes").Replace(&EntityAttribute{}) }`,
		"association delete":     `func scratch(tx *gorm.DB) { tx.Model(&Other{}).Association("Attributes").Delete(&EntityAttribute{}) }`,
		"association clear":      `type Parent struct { Attributes []EntityAttribute }; func scratch(tx *gorm.DB) { tx.Model(&Parent{}).Association("Attributes").Clear() }`,
		"formatted exec":         `func scratch(tx *gorm.DB) { tx.Exec(fmt.Sprintf("UPDATE %s SET generation=2", table)) }`,
		"formatted raw":          `func scratch(tx *gorm.DB) { tx.Raw(fmt.Sprintf("DELETE FROM %s RETURNING *", table)).Rows() }`,
		"concatenated exec":      `func scratch(tx *gorm.DB) { tx.Exec("DELETE FROM " + table) }`,
		"concatenated raw":       `func scratch(tx *gorm.DB) { sql := "UPDATE " + table + " SET generation=2 RETURNING *"; tx.Raw(sql).Row() }`,
		"split table":            `func scratch(tx *gorm.DB) { tx.Exec("DELETE FROM " + "entity_" + "attributes") }`,
		"clause table":           `func scratch(tx *gorm.DB) { q := tx.Clauses(clause.From{Tables: []clause.Table{{Name: "entity_attributes"}}}); q.Updates(map[string]any{"value": "bad"}) }`,
		"attribute table":        `func scratch(tx *gorm.DB) { tx.Table("entity_attributes").Updates(map[string]any{"value": "bad"}) }`,
		"generation table":       `func scratch(tx *gorm.DB) { q := tx.Table("entity_attribute_generations"); q.Delete(map[string]any{}) }`,
		"inferred result":        `func scratch(tx *gorm.DB, api *API) { rows := api.attrsFor(); tx.Delete(&rows) }`,
		"inferred tuple":         `func scratch(tx *gorm.DB, api *API) { rows, err := api.headsFor(); _ = err; tx.Delete(&rows) }`,
		"erased helper":          `func erase(tx *gorm.DB, value any) { tx.Delete(value) }; func scratch(tx *gorm.DB, api *API) { rows := api.attrsFor(); erase(tx, &rows) }`,
		"erased generation":      `func erase(tx *gorm.DB, value any) { tx.Save(value) }; func scratch(tx *gorm.DB) { erase(tx, &AttributeGeneration{}) }`,
		"erased two hops":        `func erase(tx *gorm.DB, value any) { tx.Delete(value) }; func middle(tx *gorm.DB, input any) { erase(tx, input) }; func scratch(tx *gorm.DB, api *API) { middle(tx, api.attrsFor()) }`,
		"generic helper":         `func erase[T any](tx *gorm.DB, value T) { tx.CreateInBatches(value, 10) }; func scratch(tx *gorm.DB, api *API) { erase(tx, api.attrsFor()) }`,
		"typed variable":         `func scratch(tx *gorm.DB) { var rows []EntityAttribute; tx.Delete(&rows) }`,
		"model alias":            `func scratch(tx *gorm.DB) { q := tx.Model(&EntityAttribute{}); q.Update("value", "bad") }`,
		"direct attribute":       `func scratch(tx *gorm.DB) { tx.Create(&EntityAttribute{}) }`,
		"direct generation":      `func scratch(tx *gorm.DB) { tx.Create(&AttributeGeneration{}) }`,
		"generation SQL":         `func scratch(tx *gorm.DB) { tx.Exec("INSERT INTO entity_attribute_generations DEFAULT VALUES") }`,
		"unscoped delete":        `func scratch(tx *gorm.DB) { tx.Unscoped().Delete(&EntityAttribute{}) }`,
		"package initializer":    `var bad = func(tx *gorm.DB) { tx.Delete(&EntityAttribute{}) }`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			sources := map[string][]byte{
				"model/api_delete.go": []byte(attributeScratchPrelude + source),
				"model/table.go":      []byte("package model\nconst table = \"entity_attribute_generations\""),
			}
			violations, err := scanAttributeWriterPackages(sources)
			if err != nil {
				t.Fatalf("invalid planted fixture: %v", err)
			}
			if len(violations) == 0 {
				t.Fatalf("planted violation escaped guard: %s", source)
			}
		})
	}
}

func TestAttributeWriterLocalAliasAndInferredContainer(t *testing.T) {
	for _, source := range []string{
		`func scratch(tx *gorm.DB) { type row = EntityAttribute; rows := []row{}; tx.Delete(&rows) }`,
		`func scratch(tx *gorm.DB) { type rows []AttributeGeneration; values := make(rows, 0); tx.CreateInBatches(values, 10) }`,
		`func scratch(tx *gorm.DB) { api := &API{}; rows := api.attrsFor(); tx.Delete(&rows) }`,
		`func scratch(tx *gorm.DB) { type Parent struct { Attrs []EntityAttribute }; tx.Model(&Parent{}).Association("Attrs").Clear() }`,
	} {
		violations, err := scanAttributeWriterPackages(map[string][]byte{"model/scratch.go": []byte(attributeScratchPrelude + source)})
		if err != nil || len(violations) == 0 {
			t.Fatalf("local inferred type escaped: %s (%v)", source, err)
		}
	}
}

func TestAttributeWriterAllowedReadsAndUnrelatedWrites(t *testing.T) {
	for name, source := range map[string]string{
		"raw read":                `func scratch(tx *gorm.DB) { tx.Raw("SELECT * FROM entity_attributes FOR UPDATE").Rows() }`,
		"table read":              `func scratch(tx *gorm.DB) { var rows []EntityAttribute; tx.Table("entity_attributes").Find(&rows) }`,
		"first or init read":      `func scratch(tx *gorm.DB) { tx.FirstOrInit(&EntityAttribute{}) }`,
		"unrelated writes":        `func scratch(tx *gorm.DB) { tx.Create(&Other{}); tx.CreateInBatches([]Other{{}}, 10); tx.Exec("DELETE FROM other") }`,
		"shadowed names":          `const table="entity_attributes"; func scratch(tx *gorm.DB) { table := "other"; tx.Exec("DELETE FROM " + table) }`,
		"separate locals":         `func read(tx *gorm.DB) { rows := []EntityAttribute{}; tx.Find(&rows) }; func scratch(tx *gorm.DB) { rows := []Other{}; tx.Delete(&rows) }`,
		"unrelated erased helper": `func erase(tx *gorm.DB, value any) { tx.Delete(value) }; func scratch(tx *gorm.DB) { erase(tx, &Other{}) }`,
		"attribute filter":        `func scratch(tx *gorm.DB, rows []EntityAttribute) { tx.Model(&Other{}).Where("id IN ?", rows).Update("value", "ok") }`,
		"attribute subquery":      `func scratch(tx *gorm.DB) { tx.Model(&Other{}).Where("id IN (SELECT entity_id FROM entity_attributes)").Update("value", "ok") }`,
	} {
		t.Run(name, func(t *testing.T) {
			violations, err := scanAttributeWriterPackages(map[string][]byte{"model/scratch.go": []byte(attributeScratchPrelude + source)})
			if err != nil || len(violations) != 0 {
				t.Fatalf("legitimate source rejected: %v (%v)", violations, err)
			}
		})
	}
}

func TestAttributeWriterScansEveryServicePackage(t *testing.T) {
	root := t.TempDir()
	for _, file := range []string{"main.go", "processor/writer.go", "graphql/writer.go", "attributeprotocol/writer.go", "model/writer.go", "schema/frozen.go", "processor/ignored_test.go"} {
		name := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		pkg := filepath.Base(filepath.Dir(name))
		if file == "main.go" {
			pkg = "main"
		}
		source := "package " + pkg + "\nimport \"gorm.io/gorm\"\nfunc scratch(tx *gorm.DB) { tx.Exec(\"DELETE FROM entity_attributes\") }"
		if err := os.WriteFile(name, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := attributeWriterSources(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 5 {
		t.Fatalf("scan scope: got %v, want all five production packages", sources)
	}
	violations, err := scanAttributeWriterPackages(sources)
	if err != nil {
		t.Fatal(err)
	}
	for name := range sources {
		found := false
		for _, violation := range violations {
			found = found || strings.HasPrefix(violation, name+":")
		}
		if !found {
			t.Errorf("planted writer outside model escaped: %s", name)
		}
	}
}

func TestAttributeWriterUnresolvablePackageFails(t *testing.T) {
	for name, source := range map[string]string{
		"recursive body alias":     `package model; func scratch() { type Loop = *Loop }`,
		"missing body type":        `package model; func scratch() { var rows []MissingType; _ = rows }`,
		"missing local import":     `package model; import "github.com/devicechain-io/dc-device-management/missing"; type Broken = missing.Row`,
		"missing local type":       "package model\ntype Broken struct { Rows []MissingType }",
		"conflicting declarations": "package model\ntype Broken int; type Broken string",
		"syntax error":             "package model\nfunc broken(",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := scanAttributeWriterPackages(map[string][]byte{"model/broken.go": []byte(source)}); err == nil {
				t.Fatal("unresolvable package silently passed")
			}
		})
	}
}

func TestAttributeWriterErasedHelperAcrossPackages(t *testing.T) {
	sources := map[string][]byte{
		"model/types.go":   []byte(`package model; type EntityAttribute struct{}; type AttributeGeneration struct{}`),
		"helper/delete.go": []byte(`package helper; import "gorm.io/gorm"; func Erase(tx *gorm.DB, value any) { tx.Delete(value) }`),
		"processor/writer.go": []byte(`package processor
import (
 "gorm.io/gorm"
 "github.com/devicechain-io/dc-device-management/model"
 "github.com/devicechain-io/dc-device-management/helper"
)
func scratch(tx *gorm.DB) { helper.Erase(tx, &model.EntityAttribute{}) }
`),
	}
	violations, err := scanAttributeWriterPackages(sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) == 0 || !strings.HasPrefix(violations[0], "helper/delete.go:") {
		t.Fatalf("erased cross-package writer escaped: %v", violations)
	}
}

func TestAttributeWriterSQLBuilderReturn(t *testing.T) {
	sources := map[string][]byte{
		"model/scratch.go": []byte(attributeScratchPrelude + `func statement() string { return fmt.Sprintf("DELETE FROM %s RETURNING *", table) }; func scratch(tx *gorm.DB) { tx.Raw(statement()).Rows() }`),
		"model/table.go":   []byte("package model\nconst table = \"entity_attributes\""),
	}
	violations, err := scanAttributeWriterPackages(sources)
	if err != nil || len(violations) == 0 {
		t.Fatalf("SQL builder return escaped: %v (%v)", violations, err)
	}
}

func TestAttributeWriterGenericReceiverTypeScope(t *testing.T) {
	source := attributeScratchPrelude + `type Box[V any] struct{}; func (b *Box[V]) Zero() V { var zero V; return zero }`
	violations, err := scanAttributeWriterPackages(map[string][]byte{"model/scratch.go": []byte(source)})
	if err != nil || len(violations) != 0 {
		t.Fatalf("generic receiver scope rejected: %v (%v)", violations, err)
	}
}

func TestAttributeWriterAuthorityRestrictions(t *testing.T) {
	for _, source := range []string{
		`func scratch(tx *gorm.DB) { deleteEntityAttributesOnTx(tx, 1) }`,
		`func scratch(tx *gorm.DB) { fn := upsertAttributeOnTx; fn(tx, &EntityAttribute{}) }`,
		`func scratch(tx *gorm.DB) { fn := bumpAttributeGenerationTx; fn(tx, 1) }`,
		`func scratch(tx *gorm.DB) { markAttributeOrphanSweep(tx) }`,
		`func scratch(tx *gorm.DB) { deleteAttributeOrphanBatch(tx, "entity_attributes") }`,
		`func scratch(tx *gorm.DB) { attrprotocol.Mark(tx) }`,
	} {
		violations, err := scanAttributeWriterPackages(map[string][]byte{"model/api_delete.go": []byte(attributeScratchPrelude + source)})
		if err != nil || len(violations) == 0 {
			t.Fatalf("restricted authority escaped: %s (%v)", source, err)
		}
	}
	// The exception is a full service-relative path, not a basename or function
	// name that a new package can copy to gain write authority.
	for _, name := range []string{"processor/api_attribute_owner.go", "api_attribute_owner.go"} {
		source := `package processor; import "gorm.io/gorm"; func applyAttributePatchTx(tx *gorm.DB) { tx.Exec("DELETE FROM entity_attributes") }`
		violations, err := scanAttributeWriterPackages(map[string][]byte{name: []byte(source)})
		if err != nil || len(violations) == 0 {
			t.Fatalf("copied allow-list name escaped: %s (%v)", name, err)
		}
	}
}
