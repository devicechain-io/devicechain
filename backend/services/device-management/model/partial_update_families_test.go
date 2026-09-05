// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"gorm.io/datatypes"
)

// THE REGISTRY OF CONVERTED FAMILIES.
//
// Every entity whose update mutation carries the platform-wide partial-update
// semantic is declared here, once, and the harness in
// partial_update_harness_test.go drives all of its properties against every one.
// Converting the next family is adding a row here.
//
// 🔴 The `seeded` values in each field list are what the family's seed() must
// actually write. They are not documentation:
// TestPartialUpdate_SeedPopulatesEveryFieldDistinctly reads the row back and fails
// if the two disagree, because a fixture that does not hold what the table claims
// makes "the update preserved it" unobservable.
//
// EVERY family in this service is now converted, deviceProfile included — it was the
// last, and it was last because its payload token was a RENAME channel rather than a
// duplicate identity. That capability moved to renameDeviceProfile rather than being
// dropped, which is what made the conversion legitimate.
//
// The completeness of this list is not left to this comment to enforce.
// TestEveryUpdateTakesADedicatedUpdateRequest reflects over *Api's Update* methods and
// requires each to take a dedicated *UpdateRequest that IS registered here — so a family
// added tomorrow on the full-replace shape, or one quietly deleted from this list, fails
// there whether or not anyone remembers to write it down.
func partialUpdateFamilies() []putest.Family[*Api] {
	return []putest.Family[*Api]{
		deviceTypeFamily(),
		assetTypeFamily(),
		customerTypeFamily(),
		areaTypeFamily(),
		assetFamily(),
		customerFamily(),
		areaFamily(),
		deviceFamily(),
		metricDefinitionFamily(),
		commandDefinitionFamily(),
		detectionRuleFamily(),
		geoFenceFamily(),
		entityGroupFamily(),
		deviceCredentialFamily(),
		provisioningProfileFamily(),
		entityRelationshipTypeFamily(),
		deviceProfileFamily(),
	}
}

// ─── shared fixture values ─────────────────────────────────────────────────
//
// brandedSeed and memberSeed are the single source for their fixtures: the create
// requests and the field tables both read them, so the "seeded" column cannot drift
// away from what the seed actually wrote.

var brandedSeed = struct {
	Name, Description, ImageUrl, Icon, Bg, Fg, Border, Metadata string
}{
	Name:        "Original name",
	Description: "Original description",
	ImageUrl:    "https://example.invalid/original.png",
	Icon:        "gauge",
	Bg:          "#111111",
	Fg:          "#222222",
	Border:      "#333333",
	Metadata:    `{"fleet":"north"}`,
}

var memberSeed = struct{ Name, Description, Metadata string }{
	Name:        "Original name",
	Description: "Original description",
	Metadata:    `{"fleet":"north"}`,
}

// The two type tokens every typed-member fixture creates: the one the member is
// seeded under, and the one the re-point property moves it to.
const (
	seededTypeToken = "type-a"
	otherTypeToken  = "type-b"
)

// ─── the branded registry types ────────────────────────────────────────────
//
// AssetType, CustomerType and AreaType are the same entity with three names —
// token, name, description, five branding fields, metadata. DeviceType is that
// shape plus a profile reference and two identity facets, so it is declared
// separately rather than through a parameter nobody else would use.

// brandedFields declares the eight shared fields, given accessors that reach them
// on one family's update request. The accessors are what make this type-safe: a
// field renamed on the request is a compile error here, not a silently skipped test.
func brandedFields[R any](
	name, description, imageURL, icon, bg, fg, border, metadata func(*R) *dcgraphql.OptionalString,
) []putest.Field {
	return []putest.Field{
		putest.OptionalStringField("name", brandedSeed.Name, "Renamed", name),
		putest.OptionalStringField("description", brandedSeed.Description, "Rewritten", description),
		putest.OptionalStringField("imageUrl", brandedSeed.ImageUrl, "https://example.invalid/new.png", imageURL),
		putest.OptionalStringField("icon", brandedSeed.Icon, "wrench", icon),
		putest.OptionalStringField("backgroundColor", brandedSeed.Bg, "#aaaaaa", bg),
		putest.OptionalStringField("foregroundColor", brandedSeed.Fg, "#bbbbbb", fg),
		putest.OptionalStringField("borderColor", brandedSeed.Border, "#cccccc", border),
		putest.OptionalStringField("metadata", brandedSeed.Metadata, `{"fleet":"south"}`, metadata),
	}
}

// readBranded is the read half, shared the same way: the eight columns every
// branded registry type stores, read off the embedded rdb structs.
func readBranded(named *namedBrandedRow) map[string]string {
	return map[string]string{
		"name":            nullStr(named.Name),
		"description":     nullStr(named.Description),
		"imageUrl":        nullStr(named.ImageUrl),
		"icon":            nullStr(named.Icon),
		"backgroundColor": nullStr(named.BackgroundColor),
		"foregroundColor": nullStr(named.ForegroundColor),
		"borderColor":     nullStr(named.BorderColor),
		"metadata":        jsonStr(named.Metadata),
	}
}

// seededPropertySchema is the draft property contract the asset-type family seeds,
// and the asset family publishes on both of its types. One STRING property with no
// constraints, so it can be filled, replaced and cleared without any of the harness's
// states colliding with a required-ness or bounds refusal — those are covered by
// their own tests, not by this one.
const seededPropertySchema = `[{"name":"vendor","dataType":"STRING"}]`

func assetTypeFamily() putest.Family[*Api] {
	return putest.Family[*Api]{
		Name:    "assetType",
		Token:   "at-1",
		Migrate: []any{&AssetType{}, &Asset{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			if _, err := api.CreateAssetType(ctx, &AssetTypeCreateRequest{
				Token: "at-1", Name: strp(brandedSeed.Name), Description: strp(brandedSeed.Description),
				ImageUrl: strp(brandedSeed.ImageUrl), Icon: strp(brandedSeed.Icon),
				BackgroundColor: strp(brandedSeed.Bg), ForegroundColor: strp(brandedSeed.Fg),
				BorderColor: strp(brandedSeed.Border), Metadata: strp(brandedSeed.Metadata),
				PropertySchema: strp(seededPropertySchema),
			}); err != nil {
				t.Fatalf("seed asset type: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.AssetTypesByToken(ctx, []string{"at-1"})
			e := requireOne(t, "asset type", rows, err)
			got := readBranded(&namedBrandedRow{e.NamedEntity, e.BrandedEntity, e.Metadata})
			got["propertySchema"] = jsonStr(e.PropertySchema)
			return got
		},
		NewRequest: func() any { return new(AssetTypeUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateAssetType(ctx, token, req.(*AssetTypeUpdateRequest))
			return err
		},
		Fields: append(brandedFields(
			func(r *AssetTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Name },
			func(r *AssetTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Description },
			func(r *AssetTypeUpdateRequest) *dcgraphql.OptionalString { return &r.ImageUrl },
			func(r *AssetTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Icon },
			func(r *AssetTypeUpdateRequest) *dcgraphql.OptionalString { return &r.BackgroundColor },
			func(r *AssetTypeUpdateRequest) *dcgraphql.OptionalString { return &r.ForegroundColor },
			func(r *AssetTypeUpdateRequest) *dcgraphql.OptionalString { return &r.BorderColor },
			func(r *AssetTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata },
		),
			// The draft contract behaves like every other optional document column:
			// omitted keeps it, null withdraws it, a value replaces it wholesale. It is
			// listed here rather than folded into brandedFields because only asset types
			// have one.
			putest.OptionalStringField("propertySchema", seededPropertySchema,
				`[{"name":"vendor","dataType":"STRING"},{"name":"stages","dataType":"INT"}]`,
				func(r *AssetTypeUpdateRequest) *dcgraphql.OptionalString { return &r.PropertySchema }),
		),
	}
}

func customerTypeFamily() putest.Family[*Api] {
	return putest.Family[*Api]{
		Name:    "customerType",
		Token:   "ct-1",
		Migrate: []any{&CustomerType{}, &Customer{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			if _, err := api.CreateCustomerType(ctx, &CustomerTypeCreateRequest{
				Token: "ct-1", Name: strp(brandedSeed.Name), Description: strp(brandedSeed.Description),
				ImageUrl: strp(brandedSeed.ImageUrl), Icon: strp(brandedSeed.Icon),
				BackgroundColor: strp(brandedSeed.Bg), ForegroundColor: strp(brandedSeed.Fg),
				BorderColor: strp(brandedSeed.Border), Metadata: strp(brandedSeed.Metadata),
			}); err != nil {
				t.Fatalf("seed customer type: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.CustomerTypesByToken(ctx, []string{"ct-1"})
			e := requireOne(t, "customer type", rows, err)
			return readBranded(&namedBrandedRow{e.NamedEntity, e.BrandedEntity, e.Metadata})
		},
		NewRequest: func() any { return new(CustomerTypeUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateCustomerType(ctx, token, req.(*CustomerTypeUpdateRequest))
			return err
		},
		Fields: brandedFields(
			func(r *CustomerTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Name },
			func(r *CustomerTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Description },
			func(r *CustomerTypeUpdateRequest) *dcgraphql.OptionalString { return &r.ImageUrl },
			func(r *CustomerTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Icon },
			func(r *CustomerTypeUpdateRequest) *dcgraphql.OptionalString { return &r.BackgroundColor },
			func(r *CustomerTypeUpdateRequest) *dcgraphql.OptionalString { return &r.ForegroundColor },
			func(r *CustomerTypeUpdateRequest) *dcgraphql.OptionalString { return &r.BorderColor },
			func(r *CustomerTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata },
		),
	}
}

func areaTypeFamily() putest.Family[*Api] {
	return putest.Family[*Api]{
		Name:    "areaType",
		Token:   "art-1",
		Migrate: []any{&AreaType{}, &Area{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			if _, err := api.CreateAreaType(ctx, &AreaTypeCreateRequest{
				Token: "art-1", Name: strp(brandedSeed.Name), Description: strp(brandedSeed.Description),
				ImageUrl: strp(brandedSeed.ImageUrl), Icon: strp(brandedSeed.Icon),
				BackgroundColor: strp(brandedSeed.Bg), ForegroundColor: strp(brandedSeed.Fg),
				BorderColor: strp(brandedSeed.Border), Metadata: strp(brandedSeed.Metadata),
			}); err != nil {
				t.Fatalf("seed area type: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.AreaTypesByToken(ctx, []string{"art-1"})
			e := requireOne(t, "area type", rows, err)
			return readBranded(&namedBrandedRow{e.NamedEntity, e.BrandedEntity, e.Metadata})
		},
		NewRequest: func() any { return new(AreaTypeUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateAreaType(ctx, token, req.(*AreaTypeUpdateRequest))
			return err
		},
		Fields: brandedFields(
			func(r *AreaTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Name },
			func(r *AreaTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Description },
			func(r *AreaTypeUpdateRequest) *dcgraphql.OptionalString { return &r.ImageUrl },
			func(r *AreaTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Icon },
			func(r *AreaTypeUpdateRequest) *dcgraphql.OptionalString { return &r.BackgroundColor },
			func(r *AreaTypeUpdateRequest) *dcgraphql.OptionalString { return &r.ForegroundColor },
			func(r *AreaTypeUpdateRequest) *dcgraphql.OptionalString { return &r.BorderColor },
			func(r *AreaTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata },
		),
	}
}

// ─── the typed members: Asset, Customer, Area ──────────────────────────────
//
// Name, description, metadata and a REQUIRED reference to their type. The
// reference is where they differ from the branded types in more than naming: its
// column is NOT NULL, so a null on it is refused rather than honoured, and the
// harness asserts that refusal in place of the clearing property.

func memberFields[R any](refName string,
	name, description, metadata, typeToken func(*R) *dcgraphql.OptionalString,
) []putest.Field {
	return []putest.Field{
		putest.OptionalStringField("name", memberSeed.Name, "Renamed", name),
		putest.OptionalStringField("description", memberSeed.Description, "Rewritten", description),
		putest.OptionalStringField("metadata", memberSeed.Metadata, `{"fleet":"south"}`, metadata),
		putest.RequiredRefField(refName, seededTypeToken, otherTypeToken, typeToken),
	}
}

func readMember(named *namedRow, refName, refToken string) map[string]string {
	return map[string]string{
		"name":        nullStr(named.Name),
		"description": nullStr(named.Description),
		"metadata":    jsonStr(named.Metadata),
		refName:       refToken,
	}
}

func assetFamily() putest.Family[*Api] {
	return putest.Family[*Api]{
		Name:    "asset",
		Token:   "a-1",
		Migrate: []any{&AssetType{}, &Asset{}, &AssetTypeVersion{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			// BOTH types declare AND PUBLISH the same contract. Publishing is what makes
			// properties settable at all, and giving both types the same one is what lets
			// the retype field move the asset between them without its already-stored
			// properties becoming undeclared in the destination — that refusal is real and
			// has its own test; here it would only be a fixture fighting the harness.
			for _, tok := range []string{seededTypeToken, otherTypeToken} {
				if _, err := api.CreateAssetType(ctx, &AssetTypeCreateRequest{
					Token: tok, PropertySchema: strp(seededPropertySchema),
				}); err != nil {
					t.Fatalf("seed asset type %q: %v", tok, err)
				}
				if _, err := api.PublishAssetType(ctx, tok, nil, nil, "harness"); err != nil {
					t.Fatalf("publish asset type %q: %v", tok, err)
				}
			}
			if _, err := api.CreateAsset(ctx, &AssetCreateRequest{
				Token: "a-1", AssetTypeToken: seededTypeToken,
				Name: strp(memberSeed.Name), Description: strp(memberSeed.Description),
				Metadata: strp(memberSeed.Metadata), Properties: strp(seededProperties),
			}); err != nil {
				t.Fatalf("seed asset: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.AssetsByToken(ctx, []string{"a-1"})
			e := requireOne(t, "asset", rows, err)
			got := readMember(&namedRow{e.NamedEntity, e.Metadata}, "assetTypeToken",
				refTokenOf(t, e.AssetType == nil, func() string { return e.AssetType.Token }))
			got["properties"] = jsonStr(e.Properties)
			return got
		},
		NewRequest: func() any { return new(AssetUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateAsset(ctx, token, req.(*AssetUpdateRequest))
			return err
		},
		Fields: append(memberFields("assetTypeToken",
			func(r *AssetUpdateRequest) *dcgraphql.OptionalString { return &r.Name },
			func(r *AssetUpdateRequest) *dcgraphql.OptionalString { return &r.Description },
			func(r *AssetUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata },
			func(r *AssetUpdateRequest) *dcgraphql.OptionalString { return &r.AssetTypeToken },
		),
			// The property document is nullable — clearing it is how an asset stops
			// carrying properties — so it takes the honoured-null path, not the refused
			// one its assetTypeToken neighbour takes.
			putest.OptionalStringField("properties", seededProperties, `{"vendor":"Northwind"}`,
				func(r *AssetUpdateRequest) *dcgraphql.OptionalString { return &r.Properties }),
		),
	}
}

// seededProperties fills seededPropertySchema's one declared property.
const seededProperties = `{"vendor":"Acme"}`

func customerFamily() putest.Family[*Api] {
	return putest.Family[*Api]{
		Name:    "customer",
		Token:   "c-1",
		Migrate: []any{&CustomerType{}, &Customer{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			for _, tok := range []string{seededTypeToken, otherTypeToken} {
				if _, err := api.CreateCustomerType(ctx, &CustomerTypeCreateRequest{Token: tok}); err != nil {
					t.Fatalf("seed customer type %q: %v", tok, err)
				}
			}
			if _, err := api.CreateCustomer(ctx, &CustomerCreateRequest{
				Token: "c-1", CustomerTypeToken: seededTypeToken,
				Name: strp(memberSeed.Name), Description: strp(memberSeed.Description),
				Metadata: strp(memberSeed.Metadata),
			}); err != nil {
				t.Fatalf("seed customer: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.CustomersByToken(ctx, []string{"c-1"})
			e := requireOne(t, "customer", rows, err)
			return readMember(&namedRow{e.NamedEntity, e.Metadata}, "customerTypeToken",
				refTokenOf(t, e.CustomerType == nil, func() string { return e.CustomerType.Token }))
		},
		NewRequest: func() any { return new(CustomerUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateCustomer(ctx, token, req.(*CustomerUpdateRequest))
			return err
		},
		Fields: memberFields("customerTypeToken",
			func(r *CustomerUpdateRequest) *dcgraphql.OptionalString { return &r.Name },
			func(r *CustomerUpdateRequest) *dcgraphql.OptionalString { return &r.Description },
			func(r *CustomerUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata },
			func(r *CustomerUpdateRequest) *dcgraphql.OptionalString { return &r.CustomerTypeToken },
		),
	}
}

func areaFamily() putest.Family[*Api] {
	return putest.Family[*Api]{
		Name:    "area",
		Token:   "ar-1",
		Migrate: []any{&AreaType{}, &Area{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			for _, tok := range []string{seededTypeToken, otherTypeToken} {
				if _, err := api.CreateAreaType(ctx, &AreaTypeCreateRequest{Token: tok}); err != nil {
					t.Fatalf("seed area type %q: %v", tok, err)
				}
			}
			if _, err := api.CreateArea(ctx, &AreaCreateRequest{
				Token: "ar-1", AreaTypeToken: seededTypeToken,
				Name: strp(memberSeed.Name), Description: strp(memberSeed.Description),
				Metadata: strp(memberSeed.Metadata),
			}); err != nil {
				t.Fatalf("seed area: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.AreasByToken(ctx, []string{"ar-1"})
			e := requireOne(t, "area", rows, err)
			return readMember(&namedRow{e.NamedEntity, e.Metadata}, "areaTypeToken",
				refTokenOf(t, e.AreaType == nil, func() string { return e.AreaType.Token }))
		},
		NewRequest: func() any { return new(AreaUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateArea(ctx, token, req.(*AreaUpdateRequest))
			return err
		},
		Fields: memberFields("areaTypeToken",
			func(r *AreaUpdateRequest) *dcgraphql.OptionalString { return &r.Name },
			func(r *AreaUpdateRequest) *dcgraphql.OptionalString { return &r.Description },
			func(r *AreaUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata },
			func(r *AreaUpdateRequest) *dcgraphql.OptionalString { return &r.AreaTypeToken },
		),
	}
}

// ─── Device ────────────────────────────────────────────────────────────────
//
// The member shape plus externalId. Its type reference is required for the same
// reason the others' are, and carries more weight: a re-type may move the device
// onto a different profile, which is what the post-commit roster fan-out watches.

func deviceFamily() putest.Family[*Api] {
	const externalIdSeed = "ext-original"
	return putest.Family[*Api]{
		Name:  "device",
		Token: "d-1",
		Migrate: []any{&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{},
			&MetricDefinition{}, &CommandDefinition{}, &DetectionRule{}, &DetectionRuleScopeRef{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			for _, tok := range []string{seededTypeToken, otherTypeToken} {
				if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: tok}); err != nil {
					t.Fatalf("seed device type %q: %v", tok, err)
				}
			}
			if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{
				Token: "d-1", DeviceTypeToken: seededTypeToken, ExternalId: strp(externalIdSeed),
				Name: strp(memberSeed.Name), Description: strp(memberSeed.Description),
				Metadata: strp(memberSeed.Metadata),
			}); err != nil {
				t.Fatalf("seed device: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.DevicesByToken(ctx, []string{"d-1"})
			e := requireOne(t, "device", rows, err)
			got := readMember(&namedRow{e.NamedEntity, e.Metadata}, "deviceTypeToken",
				refTokenOf(t, e.DeviceType == nil, func() string { return e.DeviceType.Token }))
			got["externalId"] = nullStr(e.ExternalId)
			return got
		},
		NewRequest: func() any { return new(DeviceUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateDevice(ctx, token, req.(*DeviceUpdateRequest))
			return err
		},
		Fields: append(
			memberFields("deviceTypeToken",
				func(r *DeviceUpdateRequest) *dcgraphql.OptionalString { return &r.Name },
				func(r *DeviceUpdateRequest) *dcgraphql.OptionalString { return &r.Description },
				func(r *DeviceUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata },
				func(r *DeviceUpdateRequest) *dcgraphql.OptionalString { return &r.DeviceTypeToken },
			),
			putest.OptionalStringField("externalId", externalIdSeed, "ext-new",
				func(r *DeviceUpdateRequest) *dcgraphql.OptionalString { return &r.ExternalId }),
		),
	}
}

// ─── DeviceType ────────────────────────────────────────────────────────────
//
// The first family converted, and the one the harness was extracted from. It is
// the branded shape plus two identity facets and a NULLABLE profile reference —
// nullable, unlike every other reference here, because "no profile adopted" is a
// real state for a device type. That is why profileToken clears where
// assetTypeToken refuses.
//
// The behaviours unique to it — an empty profileToken detaching, an unknown one
// refusing the whole update, a re-point moving it — stay in
// api_device_type_partial_update_test.go, which is where a reader looks for them.

func deviceTypeFamily() putest.Family[*Api] {
	const (
		manufacturerSeed = "Acme"
		modelSeed        = "A-1000"
		profileSeed      = "profile-a"
	)
	return putest.Family[*Api]{
		Name:  "deviceType",
		Token: "dt-1",
		Migrate: []any{&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{},
			&MetricDefinition{}, &CommandDefinition{}, &DetectionRule{}, &DetectionRuleScopeRef{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			for _, tok := range []string{profileSeed, "profile-b"} {
				if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: tok}); err != nil {
					t.Fatalf("seed profile %q: %v", tok, err)
				}
			}
			if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{
				Token: "dt-1", Name: strp(brandedSeed.Name), Description: strp(brandedSeed.Description),
				ImageUrl: strp(brandedSeed.ImageUrl), Icon: strp(brandedSeed.Icon),
				BackgroundColor: strp(brandedSeed.Bg), ForegroundColor: strp(brandedSeed.Fg),
				BorderColor: strp(brandedSeed.Border), Metadata: strp(brandedSeed.Metadata),
				ProfileToken: strp(profileSeed),
				Manufacturer: strp(manufacturerSeed), Model: strp(modelSeed),
			}); err != nil {
				t.Fatalf("seed device type: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.DeviceTypesByToken(ctx, []string{"dt-1"})
			e := requireOne(t, "device type", rows, err)
			got := readBranded(&namedBrandedRow{e.NamedEntity, e.BrandedEntity, e.Metadata})
			got["manufacturer"] = nullStr(e.Manufacturer)
			got["model"] = nullStr(e.ModelName)
			got["profileToken"] = nullMarker
			if e.ProfileId != nil {
				profiles, perr := api.DeviceProfilesById(ctx, []uint{*e.ProfileId})
				if perr != nil || len(profiles) != 1 {
					t.Fatalf("resolve adopted profile %d: %v (%d matches)", *e.ProfileId, perr, len(profiles))
				}
				got["profileToken"] = profiles[0].Token
			}
			return got
		},
		NewRequest: func() any { return new(DeviceTypeUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateDeviceType(ctx, token, req.(*DeviceTypeUpdateRequest))
			return err
		},
		Fields: append(
			brandedFields(
				func(r *DeviceTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Name },
				func(r *DeviceTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Description },
				func(r *DeviceTypeUpdateRequest) *dcgraphql.OptionalString { return &r.ImageUrl },
				func(r *DeviceTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Icon },
				func(r *DeviceTypeUpdateRequest) *dcgraphql.OptionalString { return &r.BackgroundColor },
				func(r *DeviceTypeUpdateRequest) *dcgraphql.OptionalString { return &r.ForegroundColor },
				func(r *DeviceTypeUpdateRequest) *dcgraphql.OptionalString { return &r.BorderColor },
				func(r *DeviceTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata },
			),
			putest.OptionalStringField("manufacturer", manufacturerSeed, "Globex",
				func(r *DeviceTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Manufacturer }),
			putest.OptionalStringField("model", modelSeed, "B-2000",
				func(r *DeviceTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Model }),
			// NULLABLE, unlike every other reference in this file: detaching a profile is
			// a state a device type can legitimately be in.
			putest.OptionalStringField("profileToken", profileSeed, "profile-b",
				func(r *DeviceTypeUpdateRequest) *dcgraphql.OptionalString { return &r.ProfileToken }),
		),
	}
}

// ─── read plumbing ─────────────────────────────────────────────────────────

// namedBrandedRow and namedRow carry the embedded rdb structs a read needs, so
// readBranded/readMember can be written once instead of per family. They exist
// because Go has no way to say "any struct embedding rdb.NamedEntity".
type namedBrandedRow struct {
	rdb.NamedEntity
	rdb.BrandedEntity
	Metadata *datatypes.JSON
}

type namedRow struct {
	rdb.NamedEntity
	Metadata *datatypes.JSON
}

// refTokenOf reads a required reference's token, failing rather than returning a
// plausible blank when the preload came back nil. A nil preload means a dangling FK
// — a real defect — and reporting it as "" would make the harness's own assertions
// compare two empty strings and pass.
func refTokenOf(t *testing.T, isNil bool, read func() string) string {
	t.Helper()
	if isNil {
		t.Fatal("the entity's type preload is nil, so its reference is dangling")
	}
	return read()
}

// ─── the profile-scoped definitions ────────────────────────────────────────
//
// MetricDefinition, CommandDefinition and DetectionRule all hang off a DeviceProfile
// through a NOT NULL FK, so all three declare deviceProfileToken as a required
// REFERENCE: a null is refused, and an unknown token refuses the whole update.

// twoProfiles seeds the profile a definition is created under and the one the re-parent
// property moves it to. TWO is load-bearing — with one profile, "the reference moved"
// and "the reference was left alone" are the same observation.
func twoProfiles(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	for _, tok := range []string{seededProfileToken, otherProfileToken} {
		seedDeviceProfile(t, api, ctx, tok)
	}
}

const (
	seededProfileToken = "prof-a"
	otherProfileToken  = "prof-b"
)

func metricDefinitionFamily() putest.Family[*Api] {
	const (
		metricKeySeed   = "temperature"
		dataTypeSeed    = string(MetricDouble)
		unitSeed        = "Cel"
		enumSeed        = `["low","high"]`
		descriptorSeed  = "wot:TemperatureProperty"
		minSeed         = 1.5
		maxSeed         = 90.25
		nameSeed        = "Original name"
		descriptionSeed = "Original description"
		metadataSeed    = `{"fleet":"north"}`
	)
	return putest.Family[*Api]{
		Name:    "metricDefinition",
		Token:   "md-1",
		Migrate: deviceProfileTables,
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			twoProfiles(t, api, ctx)
			minValue, maxValue := minSeed, maxSeed
			if _, err := api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{
				Token: "md-1", DeviceProfileToken: seededProfileToken, MetricKey: metricKeySeed,
				Name: strp(nameSeed), Description: strp(descriptionSeed), DataType: dataTypeSeed,
				Unit: strp(unitSeed), MinValue: &minValue, MaxValue: &maxValue,
				Enum: strp(enumSeed), Descriptor: strp(descriptorSeed), Metadata: strp(metadataSeed),
			}); err != nil {
				t.Fatalf("seed metric definition: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.MetricDefinitionsByToken(ctx, []string{"md-1"})
			e := requireOne(t, "metric definition", rows, err)
			return map[string]string{
				"deviceProfileToken": refTokenOf(t, e.DeviceProfile == nil, func() string { return e.DeviceProfile.Token }),
				"metricKey":          e.MetricKey,
				"name":               nullStr(e.Name),
				"description":        nullStr(e.Description),
				"dataType":           e.DataType,
				"unit":               nullStr(e.Unit),
				"minValue":           nullFloatStr(e.MinValue),
				"maxValue":           nullFloatStr(e.MaxValue),
				"enum":               jsonStr(e.Enum),
				"descriptor":         nullStr(e.Descriptor),
				"metadata":           jsonStr(e.Metadata),
			}
		},
		NewRequest: func() any { return new(MetricDefinitionUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateMetricDefinition(ctx, token, req.(*MetricDefinitionUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			putest.RequiredRefField("deviceProfileToken", seededProfileToken, otherProfileToken,
				func(r *MetricDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.DeviceProfileToken }),
			putest.RequiredStringField("metricKey", metricKeySeed, "humidity",
				func(r *MetricDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.MetricKey }),
			putest.OptionalStringField("name", nameSeed, "Renamed",
				func(r *MetricDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", descriptionSeed, "Rewritten",
				func(r *MetricDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			// The replacement is another STORABLE type. INT is; STRING is not, and using it
			// would make this property fail for a reason that belongs to the vocabulary
			// check rather than to the fold under test.
			putest.RequiredStringField("dataType", dataTypeSeed, string(MetricInt),
				func(r *MetricDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.DataType }),
			putest.OptionalStringField("unit", unitSeed, "kW",
				func(r *MetricDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.Unit }),
			putest.OptionalFloat64Field("minValue", minSeed, -40,
				func(r *MetricDefinitionUpdateRequest) *dcgraphql.OptionalFloat64 { return &r.MinValue }),
			putest.OptionalFloat64Field("maxValue", maxSeed, 125,
				func(r *MetricDefinitionUpdateRequest) *dcgraphql.OptionalFloat64 { return &r.MaxValue }),
			putest.OptionalStringField("enum", enumSeed, `["cold","hot"]`,
				func(r *MetricDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.Enum }),
			putest.OptionalStringField("descriptor", descriptorSeed, "wot:HumidityProperty",
				func(r *MetricDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.Descriptor }),
			putest.OptionalStringField("metadata", metadataSeed, `{"fleet":"south"}`,
				func(r *MetricDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata }),
		},
	}
}

func commandDefinitionFamily() putest.Family[*Api] {
	const (
		commandKeySeed  = "drive"
		schemaSeed      = `[{"name":"speed","dataType":"DOUBLE"}]`
		nameSeed        = "Original name"
		descriptionSeed = "Original description"
		metadataSeed    = `{"fleet":"north"}`
	)
	return putest.Family[*Api]{
		Name:    "commandDefinition",
		Token:   "cd-1",
		Migrate: deviceProfileTables,
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			twoProfiles(t, api, ctx)
			if _, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{
				Token: "cd-1", DeviceProfileToken: seededProfileToken, CommandKey: commandKeySeed,
				Name: strp(nameSeed), Description: strp(descriptionSeed),
				ParameterSchema: strp(schemaSeed), Metadata: strp(metadataSeed),
			}); err != nil {
				t.Fatalf("seed command definition: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.CommandDefinitionsByToken(ctx, []string{"cd-1"})
			e := requireOne(t, "command definition", rows, err)
			return map[string]string{
				"deviceProfileToken": refTokenOf(t, e.DeviceProfile == nil, func() string { return e.DeviceProfile.Token }),
				"commandKey":         e.CommandKey,
				"name":               nullStr(e.Name),
				"description":        nullStr(e.Description),
				"parameterSchema":    jsonStr(e.ParameterSchema),
				"metadata":           jsonStr(e.Metadata),
			}
		},
		NewRequest: func() any { return new(CommandDefinitionUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateCommandDefinition(ctx, token, req.(*CommandDefinitionUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			putest.RequiredRefField("deviceProfileToken", seededProfileToken, otherProfileToken,
				func(r *CommandDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.DeviceProfileToken }),
			putest.RequiredStringField("commandKey", commandKeySeed, "steer",
				func(r *CommandDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.CommandKey }),
			putest.OptionalStringField("name", nameSeed, "Renamed",
				func(r *CommandDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", descriptionSeed, "Rewritten",
				func(r *CommandDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			putest.OptionalStringField("parameterSchema", schemaSeed, `[{"name":"angle","dataType":"INT"}]`,
				func(r *CommandDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.ParameterSchema }),
			putest.OptionalStringField("metadata", metadataSeed, `{"fleet":"south"}`,
				func(r *CommandDefinitionUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata }),
		},
	}
}

// ─── DetectionRule ─────────────────────────────────────────────────────────
//
// The only family here whose fields are not independent: entityGroupToken and
// entityGroupVersion are one SCOPE validated as a pair, so they are declared with
// putest.PairedWith and the harness moves them together. See putest.Field.Partner.
//
// The fixture publishes two dynamic entity groups because the pair's replacement value
// has to be a scope that EXISTS: moving the token to grp-b and the version to 2 in one
// request means grp-b@2 must resolve, which is why grp-b is published twice.

func detectionRuleFamily() putest.Family[*Api] {
	const (
		definitionSeed     = `{"type":"threshold","metric":"temp","op":">","value":30}`
		definitionReplace  = `{"type":"threshold","metric":"temp","op":">","value":45}`
		authoringGraphSeed = `{"nodes":[],"edges":[]}`
		nameSeed           = "Original name"
		descriptionSeed    = "Original description"
		metadataSeed       = `{"fleet":"north"}`
		scopeGroupSeed     = "grp-a"
		scopeGroupReplace  = "grp-b"
	)
	scopeToken, scopeVersion := putest.PairedWith(
		putest.OptionalStringField("entityGroupToken", scopeGroupSeed, scopeGroupReplace,
			func(r *DetectionRuleUpdateRequest) *dcgraphql.OptionalString { return &r.EntityGroupToken }),
		putest.OptionalInt32Field("entityGroupVersion", 1, 2,
			func(r *DetectionRuleUpdateRequest) *dcgraphql.OptionalInt32 { return &r.EntityGroupVersion }),
	)
	return putest.Family[*Api]{
		Name:  "detectionRule",
		Token: "dr-1",
		Migrate: append(append([]any{}, deviceProfileTables...),
			&EntityGroup{}, &EntityGroupVersion{}, &EntityGroupMembership{}, &EntityGroupFacetRef{},
			&EntityAttribute{}),
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			twoProfiles(t, api, ctx)
			publishDynamicGroupVersions(t, api, ctx, scopeGroupSeed, 1)
			publishDynamicGroupVersions(t, api, ctx, scopeGroupReplace, 2)
			if _, err := api.CreateDetectionRule(ctx, &DetectionRuleCreateRequest{
				Token: "dr-1", DeviceProfileToken: seededProfileToken,
				Name: strp(nameSeed), Description: strp(descriptionSeed),
				Definition: definitionSeed, Enabled: true, Metadata: strp(metadataSeed),
				AuthoringGraph:   strp(authoringGraphSeed),
				EntityGroupToken: strp(scopeGroupSeed), EntityGroupVersion: i32p(1),
			}); err != nil {
				t.Fatalf("seed detection rule: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.DetectionRulesByToken(ctx, []string{"dr-1"})
			e := requireOne(t, "detection rule", rows, err)
			got := map[string]string{
				"deviceProfileToken": refTokenOf(t, e.DeviceProfile == nil, func() string { return e.DeviceProfile.Token }),
				"name":               nullStr(e.Name),
				"description":        nullStr(e.Description),
				"definition":         string(e.Definition),
				"enabled":            boolStr(e.Enabled),
				"metadata":           jsonStr(e.Metadata),
				"authoringGraph":     nullMarker,
				"entityGroupToken":   nullMarker,
				"entityGroupVersion": nullMarker,
			}
			if len(e.AuthoringGraph) > 0 {
				got["authoringGraph"] = string(e.AuthoringGraph)
			}
			if e.EntityGroupToken != nil {
				got["entityGroupToken"] = *e.EntityGroupToken
			}
			if e.EntityGroupVersion != nil {
				got["entityGroupVersion"] = intStr(*e.EntityGroupVersion)
			}
			return got
		},
		NewRequest: func() any { return new(DetectionRuleUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateDetectionRule(ctx, token, req.(*DetectionRuleUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			putest.RequiredRefField("deviceProfileToken", seededProfileToken, otherProfileToken,
				func(r *DetectionRuleUpdateRequest) *dcgraphql.OptionalString { return &r.DeviceProfileToken }),
			putest.OptionalStringField("name", nameSeed, "Renamed",
				func(r *DetectionRuleUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", descriptionSeed, "Rewritten",
				func(r *DetectionRuleUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			putest.RequiredStringField("definition", definitionSeed, definitionReplace,
				func(r *DetectionRuleUpdateRequest) *dcgraphql.OptionalString { return &r.Definition }),
			putest.RequiredBoolField("enabled", true,
				func(r *DetectionRuleUpdateRequest) *dcgraphql.OptionalBool { return &r.Enabled }),
			putest.OptionalStringField("metadata", metadataSeed, `{"fleet":"south"}`,
				func(r *DetectionRuleUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata }),
			putest.OptionalStringField("authoringGraph", authoringGraphSeed, `{"nodes":[{"id":"n1"}],"edges":[]}`,
				func(r *DetectionRuleUpdateRequest) *dcgraphql.OptionalString { return &r.AuthoringGraph }),
			scopeToken,
			scopeVersion,
		},
	}
}

// publishDynamicGroupVersions creates a dynamic entity group and publishes it `versions`
// times, so a scope can pin any version from 1 to that number. Each publish needs a
// DIFFERENT draft selector, because a publish that would freeze the selector already
// frozen mints nothing.
func publishDynamicGroupVersions(t *testing.T, api *Api, ctx context.Context, token string, versions int) {
	t.Helper()
	dynamic := string(MembershipDynamic)
	selector := `attr["climate"] == "v1"`
	if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
		Token: token, MemberType: "device", MembershipMode: &dynamic, Selector: &selector,
	}); err != nil {
		t.Fatalf("seed dynamic group %q: %v", token, err)
	}
	for v := 1; v <= versions; v++ {
		if v > 1 {
			next := `attr["climate"] == "v` + intStr(int32(v)) + `"`
			if _, err := api.UpdateEntityGroup(ctx, token, &EntityGroupUpdateRequest{
				Selector: dcgraphql.OptionalStringOf(next),
			}); err != nil {
				t.Fatalf("edit %q draft for v%d: %v", token, v, err)
			}
		}
		published, err := api.PublishEntityGroup(ctx, token, nil, nil, "harness")
		if err != nil {
			t.Fatalf("publish %q v%d: %v", token, v, err)
		}
		if published.Version != int32(v) {
			t.Fatalf("publishing %q produced version %d, want %d — the scope fixture pins "+
				"version numbers, so a mint that skipped would make the family's declared "+
				"values name a version that does not exist", token, published.Version, v)
		}
	}
}

// ─── GeoFence ──────────────────────────────────────────────────────────────

func geoFenceFamily() putest.Family[*Api] {
	const (
		nameSeed        = "Original name"
		descriptionSeed = "Original description"
		metadataSeed    = `{"fleet":"north"}`
	)
	// The STORED geometry is the canonical form, not the bytes the request carried, so the
	// declared readings are canonicalized through the same function the write path uses.
	// That does make canonicalization itself unobservable here — deliberately: this harness
	// is about which fields an update touches, and the canonical form has its own tests in
	// geofence_canonical_test.go. The anti-vacuity control still bites, since the two
	// canonical documents below differ.
	seededGeometry := canonicalGeometryFor(polygonGeometry(-84.3881, 33.7490, -84.3875, 33.7492,
		-84.3872, 33.7486, -84.3879, 33.7483, -84.3881, 33.7490))
	replaceGeometry := canonicalGeometryFor(polygonGeometry(-84.40, 33.75, -84.39, 33.76,
		-84.38, 33.74, -84.40, 33.75))
	return putest.Family[*Api]{
		Name:    "geoFence",
		Token:   "gf-1",
		Migrate: []any{&GeoFence{}, &GeoFenceSetVersion{}, &GeoFenceGeometryBlob{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			if _, err := api.CreateGeoFence(ctx, &GeoFenceCreateRequest{
				Token: "gf-1", Geometry: seededGeometry,
				Name: strp(nameSeed), Description: strp(descriptionSeed), Metadata: strp(metadataSeed),
			}); err != nil {
				t.Fatalf("seed geofence: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.GeoFencesByToken(ctx, []string{"gf-1"})
			e := requireOne(t, "geofence", rows, err)
			return map[string]string{
				"name":        nullStr(e.Name),
				"description": nullStr(e.Description),
				"geometry":    string(e.Geometry),
				"metadata":    jsonStr(e.Metadata),
			}
		},
		NewRequest: func() any { return new(GeoFenceUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateGeoFence(ctx, token, req.(*GeoFenceUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", nameSeed, "Renamed",
				func(r *GeoFenceUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", descriptionSeed, "Rewritten",
				func(r *GeoFenceUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			putest.RequiredStringField("geometry", seededGeometry, replaceGeometry,
				func(r *GeoFenceUpdateRequest) *dcgraphql.OptionalString { return &r.Geometry }),
			putest.OptionalStringField("metadata", metadataSeed, `{"fleet":"south"}`,
				func(r *GeoFenceUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata }),
		},
	}
}

// canonicalGeometryFor renders a geometry document the way the store will hold it.
func canonicalGeometryFor(raw string) string {
	canonical, _, err := validateGeoFenceGeometry(raw)
	if err != nil {
		panic("the geofence family declared an invalid geometry: " + err.Error())
	}
	return canonical
}

// ─── EntityGroup ───────────────────────────────────────────────────────────
//
// The fixture is a DYNAMIC group, because that is the mode in which every field of the
// input is exercisable: a static group has no selector, so seeding one would leave the
// only field with three interesting states out of the harness entirely.

func entityGroupFamily() putest.Family[*Api] {
	const selectorSeed = `attr["climate"] == "arid"`
	return putest.Family[*Api]{
		Name:    "entityGroup",
		Token:   "eg-1",
		Migrate: []any{&EntityGroup{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			dynamic := string(MembershipDynamic)
			selector := selectorSeed
			if _, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
				Token: "eg-1", MemberType: "device", MembershipMode: &dynamic, Selector: &selector,
				Name: strp(brandedSeed.Name), Description: strp(brandedSeed.Description),
				ImageUrl: strp(brandedSeed.ImageUrl), Icon: strp(brandedSeed.Icon),
				BackgroundColor: strp(brandedSeed.Bg), ForegroundColor: strp(brandedSeed.Fg),
				BorderColor: strp(brandedSeed.Border), Metadata: strp(brandedSeed.Metadata),
			}); err != nil {
				t.Fatalf("seed entity group: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.EntityGroupsByToken(ctx, []string{"eg-1"})
			e := requireOne(t, "entity group", rows, err)
			got := readBranded(&namedBrandedRow{e.NamedEntity, e.BrandedEntity, e.Metadata})
			got["selector"] = nullStr(e.Selector)
			return got
		},
		NewRequest: func() any { return new(EntityGroupUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateEntityGroup(ctx, token, req.(*EntityGroupUpdateRequest))
			return err
		},
		Fields: append(
			brandedFields(
				func(r *EntityGroupUpdateRequest) *dcgraphql.OptionalString { return &r.Name },
				func(r *EntityGroupUpdateRequest) *dcgraphql.OptionalString { return &r.Description },
				func(r *EntityGroupUpdateRequest) *dcgraphql.OptionalString { return &r.ImageUrl },
				func(r *EntityGroupUpdateRequest) *dcgraphql.OptionalString { return &r.Icon },
				func(r *EntityGroupUpdateRequest) *dcgraphql.OptionalString { return &r.BackgroundColor },
				func(r *EntityGroupUpdateRequest) *dcgraphql.OptionalString { return &r.ForegroundColor },
				func(r *EntityGroupUpdateRequest) *dcgraphql.OptionalString { return &r.BorderColor },
				func(r *EntityGroupUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata },
			),
			putest.RequiredStringField("selector", selectorSeed, `attr["climate"] == "humid"`,
				func(r *EntityGroupUpdateRequest) *dcgraphql.OptionalString { return &r.Selector }),
		),
	}
}

// ─── DeviceCredential ──────────────────────────────────────────────────────

func deviceCredentialFamily() putest.Family[*Api] {
	const (
		credentialIdSeed    = "a-minted-bearer"
		credentialValueSeed = "s3cret-material"
		expiresAtSeed       = "2031-03-04T05:06:07Z"
		metadataSeed        = `{"fleet":"north"}`
	)
	return putest.Family[*Api]{
		Name:    "deviceCredential",
		Token:   "dcred-1",
		Migrate: append(append([]any{}, deviceProfileTables...), &DeviceCredential{}),
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
				t.Fatalf("seed device type: %v", err)
			}
			for _, tok := range []string{"dev-a", "dev-b"} {
				if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{
					Token: tok, DeviceTypeToken: "dt",
				}); err != nil {
					t.Fatalf("seed device %q: %v", tok, err)
				}
			}
			if _, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
				Token: "dcred-1", DeviceToken: "dev-a",
				CredentialType: string(CredentialAccessToken), CredentialId: credentialIdSeed,
				CredentialValue: strp(credentialValueSeed), Enabled: true,
				ExpiresAt: strp(expiresAtSeed), Metadata: strp(metadataSeed),
			}); err != nil {
				t.Fatalf("seed device credential: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.DeviceCredentialsByToken(ctx, []string{"dcred-1"})
			e := requireOne(t, "device credential", rows, err)
			return map[string]string{
				"deviceToken":     refTokenOf(t, e.Device == nil, func() string { return e.Device.Token }),
				"credentialType":  e.CredentialType,
				"credentialId":    e.CredentialId,
				"credentialValue": nullStr(e.CredentialValue),
				"enabled":         boolStr(e.Enabled),
				"expiresAt":       nullTimeStr(e.ExpiresAt),
				"metadata":        jsonStr(e.Metadata),
			}
		},
		NewRequest: func() any { return new(DeviceCredentialUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateDeviceCredential(ctx, token, req.(*DeviceCredentialUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			putest.RequiredRefField("deviceToken", "dev-a", "dev-b",
				func(r *DeviceCredentialUpdateRequest) *dcgraphql.OptionalString { return &r.DeviceToken }),
			putest.RequiredStringField("credentialType", string(CredentialAccessToken), string(CredentialMqttBasic),
				func(r *DeviceCredentialUpdateRequest) *dcgraphql.OptionalString { return &r.CredentialType }),
			putest.RequiredStringField("credentialId", credentialIdSeed, "a-rotated-bearer",
				func(r *DeviceCredentialUpdateRequest) *dcgraphql.OptionalString { return &r.CredentialId }),
			putest.OptionalStringField("credentialValue", credentialValueSeed, "rotated-material",
				func(r *DeviceCredentialUpdateRequest) *dcgraphql.OptionalString { return &r.CredentialValue }),
			putest.RequiredBoolField("enabled", true,
				func(r *DeviceCredentialUpdateRequest) *dcgraphql.OptionalBool { return &r.Enabled }),
			putest.OptionalStringField("expiresAt", expiresAtSeed, "2032-01-02T03:04:05Z",
				func(r *DeviceCredentialUpdateRequest) *dcgraphql.OptionalString { return &r.ExpiresAt }),
			putest.OptionalStringField("metadata", metadataSeed, `{"fleet":"south"}`,
				func(r *DeviceCredentialUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata }),
		},
	}
}

// nullTimeStr renders a nullable timestamp the way the API's own String field carries it,
// so a family's declared value and the stored one are comparable.
//
// 🔴 .UTC() is not decoration. SQLite hands the time back in the local zone, so a
// declared "2031-03-04T05:06:07Z" would read as "2031-03-04T00:06:07-05:00" — the same
// instant, a different string, and the anti-vacuity control failing for a reason that has
// nothing to do with partial updates.
func nullTimeStr(v sql.NullTime) string {
	if !v.Valid {
		return nullMarker
	}
	return v.Time.UTC().Format(time.RFC3339)
}

// nullFloatStr renders a nullable Float column the same way optionalFloat64Field renders
// the value a caller sends, so the two are comparable.
func nullFloatStr(v sql.NullFloat64) string {
	if !v.Valid {
		return nullMarker
	}
	return floatStr(v.Float64)
}

// ─── ProvisioningProfile ───────────────────────────────────────────────────

func provisioningProfileFamily() putest.Family[*Api] {
	const (
		nameSeed        = "Original name"
		descriptionSeed = "Original description"
		provisionKey    = "fleet-key"
		provisionSecret = "fleet-s3cret"
		expiresAtSeed   = "2031-03-04T05:06:07Z"
		metadataSeed    = `{"fleet":"north"}`
	)
	return putest.Family[*Api]{
		Name:    "provisioningProfile",
		Token:   "pp-1",
		Migrate: append(append([]any{}, deviceProfileTables...), &ProvisioningProfile{}),
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			for _, tok := range []string{"dt-a", "dt-b"} {
				if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: tok}); err != nil {
					t.Fatalf("seed device type %q: %v", tok, err)
				}
			}
			if _, err := api.CreateProvisioningProfile(ctx, &ProvisioningProfileCreateRequest{
				Token: "pp-1", Name: strp(nameSeed), Description: strp(descriptionSeed),
				ProvisionKey: provisionKey, ProvisionSecret: provisionSecret,
				Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt-a",
				Enabled: true, ExpiresAt: strp(expiresAtSeed), Metadata: strp(metadataSeed),
			}); err != nil {
				t.Fatalf("seed provisioning profile: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.ProvisioningProfilesByToken(ctx, []string{"pp-1"})
			e := requireOne(t, "provisioning profile", rows, err)
			return map[string]string{
				"name":            nullStr(e.Name),
				"description":     nullStr(e.Description),
				"provisionKey":    e.ProvisionKey,
				"provisionSecret": e.ProvisionSecret,
				"strategy":        e.Strategy,
				"deviceTypeToken": refTokenOf(t, e.DeviceType == nil, func() string { return e.DeviceType.Token }),
				"enabled":         boolStr(e.Enabled),
				"expiresAt":       nullTimeStr(e.ExpiresAt),
				"metadata":        jsonStr(e.Metadata),
			}
		},
		NewRequest: func() any { return new(ProvisioningProfileUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateProvisioningProfile(ctx, token, req.(*ProvisioningProfileUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", nameSeed, "Renamed",
				func(r *ProvisioningProfileUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", descriptionSeed, "Rewritten",
				func(r *ProvisioningProfileUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			putest.RequiredStringField("provisionKey", provisionKey, "rotated-key",
				func(r *ProvisioningProfileUpdateRequest) *dcgraphql.OptionalString { return &r.ProvisionKey }),
			putest.RequiredStringField("provisionSecret", provisionSecret, "rotated-s3cret",
				func(r *ProvisioningProfileUpdateRequest) *dcgraphql.OptionalString { return &r.ProvisionSecret }),
			putest.RequiredStringField("strategy", string(ProvisionAllowNew), string(ProvisionCheckPreProvisioned),
				func(r *ProvisioningProfileUpdateRequest) *dcgraphql.OptionalString { return &r.Strategy }),
			putest.RequiredRefField("deviceTypeToken", "dt-a", "dt-b",
				func(r *ProvisioningProfileUpdateRequest) *dcgraphql.OptionalString { return &r.DeviceTypeToken }),
			putest.RequiredBoolField("enabled", true,
				func(r *ProvisioningProfileUpdateRequest) *dcgraphql.OptionalBool { return &r.Enabled }),
			putest.OptionalStringField("expiresAt", expiresAtSeed, "2032-01-02T03:04:05Z",
				func(r *ProvisioningProfileUpdateRequest) *dcgraphql.OptionalString { return &r.ExpiresAt }),
			putest.OptionalStringField("metadata", metadataSeed, `{"fleet":"south"}`,
				func(r *ProvisioningProfileUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata }),
		},
	}
}

// ─── EntityRelationshipType ────────────────────────────────────────────────

func entityRelationshipTypeFamily() putest.Family[*Api] {
	return putest.Family[*Api]{
		Name:    "entityRelationshipType",
		Token:   "ert-1",
		Migrate: []any{&EntityRelationshipType{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			if _, err := api.CreateEntityRelationshipType(ctx, &EntityRelationshipTypeCreateRequest{
				Token: "ert-1", Name: strp(memberSeed.Name), Description: strp(memberSeed.Description),
				Metadata: strp(memberSeed.Metadata), Tracked: true,
			}); err != nil {
				t.Fatalf("seed entity relationship type: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.EntityRelationshipTypesByToken(ctx, []string{"ert-1"})
			e := requireOne(t, "entity relationship type", rows, err)
			return map[string]string{
				"name":        nullStr(e.Name),
				"description": nullStr(e.Description),
				"metadata":    jsonStr(e.Metadata),
				"tracked":     boolStr(e.Tracked),
			}
		},
		NewRequest: func() any { return new(EntityRelationshipTypeUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateEntityRelationshipType(ctx, token, req.(*EntityRelationshipTypeUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", memberSeed.Name, "Renamed",
				func(r *EntityRelationshipTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", memberSeed.Description, "Rewritten",
				func(r *EntityRelationshipTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			putest.OptionalStringField("metadata", memberSeed.Metadata, `{"fleet":"south"}`,
				func(r *EntityRelationshipTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata }),
			putest.RequiredBoolField("tracked", true,
				func(r *EntityRelationshipTypeUpdateRequest) *dcgraphql.OptionalBool { return &r.Tracked }),
		},
	}
}

// ─── the device profile, and its one non-scalar field ──────────────────────
//
// deviceProfile was the LAST family in this service on the full-replace shape, and it
// was last because its payload token was a RENAME CHANNEL rather than a duplicate
// identity — dropping the field would have deleted a capability rather than converted
// one. The rename moved to renameDeviceProfile (pinned in api_profiles_rename_test.go)
// and the update input then converted like every other.

// deviceProfileSeed is the single source for what the profile fixture writes; the seed
// and the field table both read it, so the "seeded" column cannot drift away from the
// row.
var deviceProfileSeed = struct{ Name, Description, Category, Metadata string }{
	Name:        "Original name",
	Description: "Original description",
	Category:    "tracker",
	Metadata:    `{"fleet":"north"}`,
}

// The two renderings of the position declaration the family drives. They are the
// literal JSON the column holds, because that is what the harness compares — the
// encoder marshals LocationDeclaration with omitempty, so a field left nil is absent
// from the document rather than present as null.
const (
	seededLocation   = `{"expectedAccuracyMeters":7.25,"expectedUpdateIntervalSeconds":60}`
	replacedLocation = `{"expectedAccuracyMeters":1.5}`
)

// locationField declares the profile's position declaration for the harness.
//
// 🔴 IT IS HAND-BUILT RATHER THAN COMING FROM ONE OF core's FIELD CONSTRUCTORS, and the
// reason is the type: this is the platform's only INPUT-OBJECT field carrying the three
// states, so there is no shared constructor to reach for. What it must still satisfy is
// the contract every other field does — Seeded, Replace and Cleared pairwise distinct,
// Set and SetNull both present — and the harness's anti-vacuity control checks that
// rather than trusting it.
//
// The value is carried as the rendered JSON in both directions: Set parses it back into
// a declaration, so the "sent with a value" state and the reading the harness compares
// against cannot be two different spellings of one document.
func locationField() putest.Field {
	return putest.Field{
		Name: "location", Seeded: seededLocation, Replace: replacedLocation,
		Cleared: nullMarker,
		Kind:    putest.Clearable,
		Set: func(req any, v string) {
			var decl LocationDeclaration
			if err := json.Unmarshal([]byte(v), &decl); err != nil {
				panic("the location field was declared with a value that is not a rendered " +
					"declaration: " + v)
			}
			req.(*DeviceProfileUpdateRequest).Location = OptionalLocationDeclarationOf(decl)
		},
		SetNull: func(req any) {
			req.(*DeviceProfileUpdateRequest).Location = ClearedLocationDeclaration()
		},
	}
}

func deviceProfileFamily() putest.Family[*Api] {
	return putest.Family[*Api]{
		Name:    "deviceProfile",
		Token:   "dp-1",
		Migrate: deviceProfileTables,
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			var decl LocationDeclaration
			if err := json.Unmarshal([]byte(seededLocation), &decl); err != nil {
				t.Fatalf("the seeded location is not a rendered declaration: %v", err)
			}
			if _, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{
				Token: "dp-1", Name: strp(deviceProfileSeed.Name),
				Description: strp(deviceProfileSeed.Description),
				Category:    strp(deviceProfileSeed.Category),
				Metadata:    strp(deviceProfileSeed.Metadata),
				Location:    &decl,
			}); err != nil {
				t.Fatalf("seed device profile: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.DeviceProfilesByToken(ctx, []string{"dp-1"})
			e := requireOne(t, "device profile", rows, err)
			return map[string]string{
				"name":        nullStr(e.Name),
				"description": nullStr(e.Description),
				"category":    nullStr(e.Category),
				"metadata":    jsonStr(e.Metadata),
				"location":    jsonStr(e.LocationDeclaration),
			}
		},
		NewRequest: func() any { return new(DeviceProfileUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateDeviceProfile(ctx, token, req.(*DeviceProfileUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", deviceProfileSeed.Name, "Renamed",
				func(r *DeviceProfileUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", deviceProfileSeed.Description, "Rewritten",
				func(r *DeviceProfileUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			putest.OptionalStringField("category", deviceProfileSeed.Category, "gateway",
				func(r *DeviceProfileUpdateRequest) *dcgraphql.OptionalString { return &r.Category }),
			putest.OptionalStringField("metadata", deviceProfileSeed.Metadata, `{"fleet":"south"}`,
				func(r *DeviceProfileUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata }),
			locationField(),
		},
	}
}
