// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// configTestApi stands up everything a SHARED device attribute write touches: the device
// graph, the attribute and group-membership tables, the profile/version tables the
// declaration resolves through, and the two configuration tables.
func configTestApi(t *testing.T) (*Api, context.Context) {
	t.Helper()
	// Shared-cache named in-memory DB: the attribute write opens a transaction while the
	// token resolve ran on the pool connection, so every connection must see one DB.
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceCredential{}, &DeviceReplacement{},
		&EntityAttribute{}, &EntityGroup{}, &EntityGroupVersion{}, &EntityGroupMembership{},
		&EntityGroupFacetRef{}, &EntityRelationship{}, &Alarm{},
		&DeviceProfile{}, &DeviceProfileVersion{}, &MetricDefinition{}, &CommandDefinition{},
		&DetectionRule{}, &DetectionRuleScopeRef{},
		&DeviceConfigurationRevision{}, &DeviceConfigurationState{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db}), core.WithTenant(context.Background(), "acme")
}

// seedConfiguredDevice creates profile "prof" publishing the given declaration, a type
// adopting it, and device "dev" of that type.
func seedConfiguredDevice(t *testing.T, api *Api, ctx context.Context, declared []ConfigurationKey) *Device {
	t.Helper()
	seedProfileWithRule(t, api, ctx, "prof", "r1", false)
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof", declared); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	profile := "prof"
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "sensor", ProfileToken: &profile}); err != nil {
		t.Fatalf("type: %v", err)
	}
	dev, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "dev", DeviceTypeToken: "sensor"})
	if err != nil {
		t.Fatalf("device: %v", err)
	}
	return dev
}

func setConfigAttr(t *testing.T, api *Api, ctx context.Context, scope AttributeScope, key string,
	vt AttributeValueType, value string) {
	t.Helper()
	if _, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: string(entity.TypeDevice), Entity: "dev", Scope: string(scope),
		AttrKey: key, ValueType: string(vt), Value: &value,
	}); err != nil {
		t.Fatalf("set %s %s: %v", scope, key, err)
	}
}

func readConfig(t *testing.T, api *Api, ctx context.Context) *DeviceConfiguration {
	t.Helper()
	c, err := api.DeviceConfigurationByToken(ctx, "dev")
	if err != nil {
		t.Fatalf("read configuration: %v", err)
	}
	return c
}

func countRevisions(t *testing.T, api *Api, ctx context.Context) int64 {
	t.Helper()
	var n int64
	if err := api.RDB.DB(ctx).Model(&DeviceConfigurationRevision{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// The device-visible document is built from the DECLARED SHARED keys only. A SERVER value
// (even under a declared key's name), a CLIENT value, and an undeclared SHARED value —
// a classification facet — never reach it.
func TestConfigurationDocumentCarriesOnlyDeclaredSharedKeys(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{
		{Key: "interval", ValueType: "LONG"},
		{Key: "mode", ValueType: "STRING"},
	})
	setConfigAttr(t, api, ctx, AttributeScopeServer, "mode", AttributeValueString, "server-secret")
	setConfigAttr(t, api, ctx, AttributeScopeServer, "apiKey", AttributeValueString, "sk-live-123")
	setConfigAttr(t, api, ctx, AttributeScopeClient, "firmware", AttributeValueString, "1.2.3")
	setConfigAttr(t, api, ctx, AttributeScopeShared, "region", AttributeValueString, "eu-west")
	setConfigAttr(t, api, ctx, AttributeScopeShared, "alarmHigh", AttributeValueDouble, "80")
	setConfigAttr(t, api, ctx, AttributeScopeShared, "interval", AttributeValueLong, "30")

	c := readConfig(t, api, ctx)
	if c.Desired == nil {
		t.Fatal("no revision minted for a declared SHARED write")
	}
	if got, want := string(c.Desired.Document), `{"interval":30}`; got != want {
		t.Fatalf("document = %s, want %s", got, want)
	}
	if got, want := strings.Join(c.Undeclared, ","), "alarmHigh,region"; got != want {
		t.Fatalf("undeclared = %q, want %q", got, want)
	}
	for _, leaked := range []string{"server-secret", "sk-live-123", "1.2.3", "eu-west", "80"} {
		if strings.Contains(string(c.Desired.Document), leaked) {
			t.Fatalf("document leaked %q: %s", leaked, c.Desired.Document)
		}
	}
}

// A LONG written as "22.0" is 22 in the document, so a device that applied it and
// re-encodes what it holds produces the same bytes and therefore the same digest — and a
// report of that revision and digest is converged, not pending.
func TestLongWrittenAsTwentyTwoPointZeroConvergesOnDeviceReport(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{
		{Key: "sampleInterval", ValueType: "LONG"},
		{Key: "gain", ValueType: "DOUBLE"},
	})
	setConfigAttr(t, api, ctx, AttributeScopeShared, "sampleInterval", AttributeValueLong, "22.0")
	setConfigAttr(t, api, ctx, AttributeScopeShared, "gain", AttributeValueDouble, "1.50")

	desired := readConfig(t, api, ctx).Desired
	if got, want := string(desired.Document), `{"gain":1.5,"sampleInterval":22}`; got != want {
		t.Fatalf("document = %s, want %s", got, want)
	}

	// The device: decode what it received, apply, encode what it now holds.
	var applied map[string]any
	if err := json.Unmarshal(desired.Document, &applied); err != nil {
		t.Fatal(err)
	}
	held, err := json.Marshal(applied)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(held)
	deviceDigest := "sha256:" + hex.EncodeToString(sum[:])
	if deviceDigest != desired.Digest {
		t.Fatalf("device digest %s over %s != desired %s over %s", deviceDigest, held, desired.Digest, desired.Document)
	}

	if err := api.RDB.DB(ctx).Create(&DeviceConfigurationState{
		DeviceId:         dev.ID,
		ReportedRevision: sql.NullInt64{Int64: desired.Revision, Valid: true},
		ReportedDigest:   sql.NullString{String: deviceDigest, Valid: true},
		ReportedStatus:   sql.NullString{String: string(ConfigurationApplied), Valid: true},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if c := readConfig(t, api, ctx); c.Pending {
		t.Fatalf("device reported APPLIED for revision %d digest %s; still pending", desired.Revision, deviceDigest)
	}
}

// Applied means the SAME revision AND the SAME digest, and APPLIED status.
func TestPendingIsDecidedByRevisionAndDigestIdentity(t *testing.T) {
	desired := &DeviceConfigurationRevision{Revision: 3, Digest: "sha256:aa"}
	state := func(status string, rev int64, digest string) *DeviceConfigurationState {
		return &DeviceConfigurationState{
			ReportedStatus:   sql.NullString{String: status, Valid: true},
			ReportedRevision: sql.NullInt64{Int64: rev, Valid: true},
			ReportedDigest:   sql.NullString{String: digest, Valid: true},
		}
	}
	cases := []struct {
		name     string
		desired  *DeviceConfigurationRevision
		reported *DeviceConfigurationState
		want     bool
	}{
		{"nothing minted", nil, nil, false},
		{"never reported", desired, nil, true},
		{"applied, same revision and digest", desired, state("APPLIED", 3, "sha256:aa"), false},
		{"applied, same revision, other digest", desired, state("APPLIED", 3, "sha256:bb"), true},
		{"applied, other revision, same digest", desired, state("APPLIED", 2, "sha256:aa"), true},
		{"received only", desired, state("RECEIVED", 3, "sha256:aa"), true},
		{"rejected", desired, state("REJECTED", 3, "sha256:aa"), true},
		{"sync only, no report", desired, &DeviceConfigurationState{LastSyncAt: sql.NullTime{Valid: true}}, true},
	}
	for _, c := range cases {
		if got := IsPending(c.desired, c.reported); got != c.want {
			t.Errorf("%s: pending = %v, want %v", c.name, got, c.want)
		}
	}
}

// A revision is minted only when the document's digest changes: rewriting the same value
// or writing an undeclared key mints nothing; deleting a declared key mints the shrunken
// document. Revision numbers count up from 1.
func TestRevisionIsMintedOnlyWhenTheDocumentChanges(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})

	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "eco")
	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "eco")
	setConfigAttr(t, api, ctx, AttributeScopeShared, "facet", AttributeValueString, "x")
	if n := countRevisions(t, api, ctx); n != 1 {
		t.Fatalf("revisions after one distinct document = %d, want 1", n)
	}
	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "turbo")
	if _, err := api.DeleteEntityAttribute(ctx, string(entity.TypeDevice), "dev",
		string(AttributeScopeShared), "mode"); err != nil {
		t.Fatal(err)
	}
	page, err := api.DeviceConfigurationRevisions(ctx, "dev", rdb.Pagination{PageNumber: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range page.Results {
		got = append(got, string(rune('0'+r.Revision))+"="+string(r.Document))
	}
	want := []string{`3={}`, `2={"mode":"turbo"}`, `1={"mode":"eco"}`}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("revisions (newest first) = %v, want %v", got, want)
	}
	if c := readConfig(t, api, ctx); c.Stale {
		t.Fatal("latest revision matches the computed document but reads stale")
	}
}

// A SHARED write that would push the document over the cap is refused with its code, and
// the attribute is not stored.
func TestOversizeConfigurationWriteIsRefused(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "blob", ValueType: "STRING"}})
	big := strings.Repeat("x", MaxConfigurationDocumentBytes)
	_, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: string(entity.TypeDevice), Entity: "dev", Scope: string(AttributeScopeShared),
		AttrKey: "blob", ValueType: string(AttributeValueString), Value: &big,
	})
	var refusal *ConfigurationRefusal
	if !errors.As(err, &refusal) || refusal.Extensions()["code"] != CodeConfigurationTooLarge {
		t.Fatalf("err = %v, want %s", err, CodeConfigurationTooLarge)
	}
	var n int64
	api.RDB.DB(ctx).Model(&EntityAttribute{}).Where("attr_key = ?", "blob").Count(&n)
	if n != 0 {
		t.Fatalf("refused write left %d attribute row(s)", n)
	}
	if n := countRevisions(t, api, ctx); n != 0 {
		t.Fatalf("refused write minted %d revision(s)", n)
	}
	// Exactly at the cap is accepted: {"blob":"…"} is 11 bytes of framing.
	fits := strings.Repeat("x", MaxConfigurationDocumentBytes-11)
	setConfigAttr(t, api, ctx, AttributeScopeShared, "blob", AttributeValueString, fits)
	if c := readConfig(t, api, ctx); c.Desired == nil || len(c.Desired.Document) != MaxConfigurationDocumentBytes {
		t.Fatal("a document exactly at the cap was not minted")
	}
}

// A declared key written with another value type is refused rather than silently left
// out of what the device receives.
func TestDeclaredKeyWrittenWithAnotherTypeIsRefused(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "interval", ValueType: "LONG"}})
	v := "thirty"
	_, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: string(entity.TypeDevice), Entity: "dev", Scope: string(AttributeScopeShared),
		AttrKey: "interval", ValueType: string(AttributeValueString), Value: &v,
	})
	var refusal *ConfigurationRefusal
	if !errors.As(err, &refusal) || refusal.Code != CodeConfigurationValueType {
		t.Fatalf("err = %v, want %s", err, CodeConfigurationValueType)
	}
}

// A profile publish does not re-snapshot the device; the read says stale until a revision
// for the new declaration is minted.
func TestPublishingANewDeclarationReadsStale(t *testing.T) {
	api, ctx := configTestApi(t)
	seedConfiguredDevice(t, api, ctx, nil)
	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "eco")
	if c := readConfig(t, api, ctx); c.Stale || c.Desired != nil {
		t.Fatalf("nothing declared: stale=%v desired=%v, want not stale and nothing minted", c.Stale, c.Desired)
	}
	if _, err := api.SetDeviceProfileConfigurationDeclaration(ctx, "prof",
		[]ConfigurationKey{{Key: "mode", ValueType: "STRING"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester"); err != nil {
		t.Fatal(err)
	}
	c := readConfig(t, api, ctx)
	if !c.Stale || c.Desired != nil || len(c.Undeclared) != 0 {
		t.Fatalf("after publishing a declaration: stale=%v desired=%v undeclared=%v; want stale, nothing minted, none undeclared",
			c.Stale, c.Desired, c.Undeclared)
	}
}

// Every configuration query is tenant-scoped: another tenant reading by the same device
// token, or by the device's internal id, sees nothing.
func TestConfigurationReadsAreTenantScoped(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "eco")
	if err := api.RDB.DB(ctx).Create(&DeviceConfigurationState{DeviceId: dev.ID,
		ReportedStatus: sql.NullString{String: "APPLIED", Valid: true}}).Error; err != nil {
		t.Fatal(err)
	}

	other := core.WithTenant(context.Background(), "globex")
	if _, err := api.DeviceConfigurationByToken(other, "dev"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("other tenant read by token: err = %v, want not found", err)
	}
	if _, err := api.DeviceConfigurationRevisions(other, "dev", rdb.Pagination{PageNumber: 1, PageSize: 10}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("other tenant revisions by token: err = %v, want not found", err)
	}
	db := api.RDB.DB(other)
	if latest, err := latestConfigurationRevision(db, dev.ID); err != nil || latest != nil {
		t.Fatalf("other tenant latest revision by device id = %+v, %v; want none", latest, err)
	}
	var states []DeviceConfigurationState
	if err := db.Where("device_id = ?", dev.ID).Find(&states).Error; err != nil || len(states) != 0 {
		t.Fatalf("other tenant state by device id = %d row(s), %v; want none", len(states), err)
	}
	if _, _, err := computeDeviceConfiguration(db, dev.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("other tenant computed document by device id: err = %v, want not found", err)
	}
}

// Deleting a device removes its revisions and reported state with it.
func TestDeleteDeviceRemovesItsConfiguration(t *testing.T) {
	api, ctx := configTestApi(t)
	dev := seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	setConfigAttr(t, api, ctx, AttributeScopeShared, "mode", AttributeValueString, "eco")
	if err := api.RDB.DB(ctx).Create(&DeviceConfigurationState{DeviceId: dev.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := api.DeleteDevice(ctx, "dev"); err != nil || !ok {
		t.Fatalf("delete device: %v, %v", ok, err)
	}
	var revs, states int64
	api.RDB.DB(ctx).Model(&DeviceConfigurationRevision{}).Where("device_id = ?", dev.ID).Count(&revs)
	api.RDB.DB(ctx).Model(&DeviceConfigurationState{}).Where("device_id = ?", dev.ID).Count(&states)
	if revs != 0 || states != 0 {
		t.Fatalf("after delete: %d revision(s), %d state row(s); want none", revs, states)
	}
}

// Values are typed by the declaration; JSON values are re-encoded canonically.
func TestConfigurationValuesAreCanonical(t *testing.T) {
	cases := []struct {
		vt, text, want string
	}{
		{"LONG", "9223372036854775807", "9223372036854775807"},
		{"DOUBLE", "22", "22"},
		{"DOUBLE", "0.1", "0.1"},
		{"DOUBLE", "1e21", "1e+21"},
		{"DOUBLE", "0.0000001", "1e-7"},
		{"DOUBLE", "-0", "0"},
		{"BOOLEAN", "true", "true"},
		{"STRING", "a<b&\"c\"", `"a<b&\"c\""`},
		{"JSON", `{ "b": [1.0, 2e0], "a": {"z": null, "y": "<"} }`, `{"a":{"y":"<","z":null},"b":[1,2]}`},
	}
	for _, c := range cases {
		got, err := canonicalConfigurationValue(c.vt, c.vt, c.text)
		if err != nil || string(got) != c.want {
			t.Errorf("%s %q = %s, %v; want %s", c.vt, c.text, got, err, c.want)
		}
	}
	for _, bad := range []struct{ declared, stored, text string }{
		{"LONG", "STRING", "3"},
		{"LONG", "LONG", "3.5"},
		{"JSON", "JSON", `{"a":1} trailing`},
		{"JSON", "JSON", `{`},
	} {
		if got, err := canonicalConfigurationValue(bad.declared, bad.stored, bad.text); err == nil {
			t.Errorf("%s/%s %q accepted as %s", bad.declared, bad.stored, bad.text, got)
		}
	}
}
