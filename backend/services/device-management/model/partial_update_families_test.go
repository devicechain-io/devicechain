// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
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
// Families still on the FULL-REPLACE shape, and therefore deliberately absent:
// deviceProfile, metricDefinition, commandDefinition, detectionRule, geoFence,
// entityGroup, deviceCredential, provisioningProfile, entityRelationshipType.
func partialUpdateFamilies() []partialUpdateFamily {
	return []partialUpdateFamily{
		deviceTypeFamily(),
		assetTypeFamily(),
		customerTypeFamily(),
		areaTypeFamily(),
		assetFamily(),
		customerFamily(),
		areaFamily(),
		deviceFamily(),
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
) []partialField {
	return []partialField{
		optionalStringField("name", brandedSeed.Name, "Renamed", name),
		optionalStringField("description", brandedSeed.Description, "Rewritten", description),
		optionalStringField("imageUrl", brandedSeed.ImageUrl, "https://example.invalid/new.png", imageURL),
		optionalStringField("icon", brandedSeed.Icon, "wrench", icon),
		optionalStringField("backgroundColor", brandedSeed.Bg, "#aaaaaa", bg),
		optionalStringField("foregroundColor", brandedSeed.Fg, "#bbbbbb", fg),
		optionalStringField("borderColor", brandedSeed.Border, "#cccccc", border),
		optionalStringField("metadata", brandedSeed.Metadata, `{"fleet":"south"}`, metadata),
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

func assetTypeFamily() partialUpdateFamily {
	return partialUpdateFamily{
		name:    "assetType",
		token:   "at-1",
		migrate: []any{&AssetType{}, &Asset{}},
		seed: func(t *testing.T, api *Api, ctx context.Context) {
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
		read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.AssetTypesByToken(ctx, []string{"at-1"})
			e := requireOne(t, "asset type", rows, err)
			got := readBranded(&namedBrandedRow{e.NamedEntity, e.BrandedEntity, e.Metadata})
			got["propertySchema"] = jsonStr(e.PropertySchema)
			return got
		},
		newRequest: func() any { return new(AssetTypeUpdateRequest) },
		update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateAssetType(ctx, token, req.(*AssetTypeUpdateRequest))
			return err
		},
		fields: append(brandedFields(
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
			optionalStringField("propertySchema", seededPropertySchema,
				`[{"name":"vendor","dataType":"STRING"},{"name":"stages","dataType":"INT"}]`,
				func(r *AssetTypeUpdateRequest) *dcgraphql.OptionalString { return &r.PropertySchema }),
		),
	}
}

func customerTypeFamily() partialUpdateFamily {
	return partialUpdateFamily{
		name:    "customerType",
		token:   "ct-1",
		migrate: []any{&CustomerType{}, &Customer{}},
		seed: func(t *testing.T, api *Api, ctx context.Context) {
			if _, err := api.CreateCustomerType(ctx, &CustomerTypeCreateRequest{
				Token: "ct-1", Name: strp(brandedSeed.Name), Description: strp(brandedSeed.Description),
				ImageUrl: strp(brandedSeed.ImageUrl), Icon: strp(brandedSeed.Icon),
				BackgroundColor: strp(brandedSeed.Bg), ForegroundColor: strp(brandedSeed.Fg),
				BorderColor: strp(brandedSeed.Border), Metadata: strp(brandedSeed.Metadata),
			}); err != nil {
				t.Fatalf("seed customer type: %v", err)
			}
		},
		read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.CustomerTypesByToken(ctx, []string{"ct-1"})
			e := requireOne(t, "customer type", rows, err)
			return readBranded(&namedBrandedRow{e.NamedEntity, e.BrandedEntity, e.Metadata})
		},
		newRequest: func() any { return new(CustomerTypeUpdateRequest) },
		update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateCustomerType(ctx, token, req.(*CustomerTypeUpdateRequest))
			return err
		},
		fields: brandedFields(
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

func areaTypeFamily() partialUpdateFamily {
	return partialUpdateFamily{
		name:    "areaType",
		token:   "art-1",
		migrate: []any{&AreaType{}, &Area{}},
		seed: func(t *testing.T, api *Api, ctx context.Context) {
			if _, err := api.CreateAreaType(ctx, &AreaTypeCreateRequest{
				Token: "art-1", Name: strp(brandedSeed.Name), Description: strp(brandedSeed.Description),
				ImageUrl: strp(brandedSeed.ImageUrl), Icon: strp(brandedSeed.Icon),
				BackgroundColor: strp(brandedSeed.Bg), ForegroundColor: strp(brandedSeed.Fg),
				BorderColor: strp(brandedSeed.Border), Metadata: strp(brandedSeed.Metadata),
			}); err != nil {
				t.Fatalf("seed area type: %v", err)
			}
		},
		read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.AreaTypesByToken(ctx, []string{"art-1"})
			e := requireOne(t, "area type", rows, err)
			return readBranded(&namedBrandedRow{e.NamedEntity, e.BrandedEntity, e.Metadata})
		},
		newRequest: func() any { return new(AreaTypeUpdateRequest) },
		update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateAreaType(ctx, token, req.(*AreaTypeUpdateRequest))
			return err
		},
		fields: brandedFields(
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
) []partialField {
	return []partialField{
		optionalStringField("name", memberSeed.Name, "Renamed", name),
		optionalStringField("description", memberSeed.Description, "Rewritten", description),
		optionalStringField("metadata", memberSeed.Metadata, `{"fleet":"south"}`, metadata),
		requiredRefField(refName, seededTypeToken, otherTypeToken, typeToken),
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

func assetFamily() partialUpdateFamily {
	return partialUpdateFamily{
		name:    "asset",
		token:   "a-1",
		migrate: []any{&AssetType{}, &Asset{}, &AssetTypeVersion{}},
		seed: func(t *testing.T, api *Api, ctx context.Context) {
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
		read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.AssetsByToken(ctx, []string{"a-1"})
			e := requireOne(t, "asset", rows, err)
			got := readMember(&namedRow{e.NamedEntity, e.Metadata}, "assetTypeToken",
				refTokenOf(t, e.AssetType == nil, func() string { return e.AssetType.Token }))
			got["properties"] = jsonStr(e.Properties)
			return got
		},
		newRequest: func() any { return new(AssetUpdateRequest) },
		update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateAsset(ctx, token, req.(*AssetUpdateRequest))
			return err
		},
		fields: append(memberFields("assetTypeToken",
			func(r *AssetUpdateRequest) *dcgraphql.OptionalString { return &r.Name },
			func(r *AssetUpdateRequest) *dcgraphql.OptionalString { return &r.Description },
			func(r *AssetUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata },
			func(r *AssetUpdateRequest) *dcgraphql.OptionalString { return &r.AssetTypeToken },
		),
			// The property document is nullable — clearing it is how an asset stops
			// carrying properties — so it takes the honoured-null path, not the refused
			// one its assetTypeToken neighbour takes.
			optionalStringField("properties", seededProperties, `{"vendor":"Northwind"}`,
				func(r *AssetUpdateRequest) *dcgraphql.OptionalString { return &r.Properties }),
		),
	}
}

// seededProperties fills seededPropertySchema's one declared property.
const seededProperties = `{"vendor":"Acme"}`

func customerFamily() partialUpdateFamily {
	return partialUpdateFamily{
		name:    "customer",
		token:   "c-1",
		migrate: []any{&CustomerType{}, &Customer{}},
		seed: func(t *testing.T, api *Api, ctx context.Context) {
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
		read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.CustomersByToken(ctx, []string{"c-1"})
			e := requireOne(t, "customer", rows, err)
			return readMember(&namedRow{e.NamedEntity, e.Metadata}, "customerTypeToken",
				refTokenOf(t, e.CustomerType == nil, func() string { return e.CustomerType.Token }))
		},
		newRequest: func() any { return new(CustomerUpdateRequest) },
		update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateCustomer(ctx, token, req.(*CustomerUpdateRequest))
			return err
		},
		fields: memberFields("customerTypeToken",
			func(r *CustomerUpdateRequest) *dcgraphql.OptionalString { return &r.Name },
			func(r *CustomerUpdateRequest) *dcgraphql.OptionalString { return &r.Description },
			func(r *CustomerUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata },
			func(r *CustomerUpdateRequest) *dcgraphql.OptionalString { return &r.CustomerTypeToken },
		),
	}
}

func areaFamily() partialUpdateFamily {
	return partialUpdateFamily{
		name:    "area",
		token:   "ar-1",
		migrate: []any{&AreaType{}, &Area{}},
		seed: func(t *testing.T, api *Api, ctx context.Context) {
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
		read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.AreasByToken(ctx, []string{"ar-1"})
			e := requireOne(t, "area", rows, err)
			return readMember(&namedRow{e.NamedEntity, e.Metadata}, "areaTypeToken",
				refTokenOf(t, e.AreaType == nil, func() string { return e.AreaType.Token }))
		},
		newRequest: func() any { return new(AreaUpdateRequest) },
		update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateArea(ctx, token, req.(*AreaUpdateRequest))
			return err
		},
		fields: memberFields("areaTypeToken",
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

func deviceFamily() partialUpdateFamily {
	const externalIdSeed = "ext-original"
	return partialUpdateFamily{
		name:  "device",
		token: "d-1",
		migrate: []any{&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{},
			&MetricDefinition{}, &CommandDefinition{}, &DetectionRule{}, &DetectionRuleScopeRef{}},
		seed: func(t *testing.T, api *Api, ctx context.Context) {
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
		read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.DevicesByToken(ctx, []string{"d-1"})
			e := requireOne(t, "device", rows, err)
			got := readMember(&namedRow{e.NamedEntity, e.Metadata}, "deviceTypeToken",
				refTokenOf(t, e.DeviceType == nil, func() string { return e.DeviceType.Token }))
			got["externalId"] = nullStr(e.ExternalId)
			return got
		},
		newRequest: func() any { return new(DeviceUpdateRequest) },
		update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateDevice(ctx, token, req.(*DeviceUpdateRequest))
			return err
		},
		fields: append(
			memberFields("deviceTypeToken",
				func(r *DeviceUpdateRequest) *dcgraphql.OptionalString { return &r.Name },
				func(r *DeviceUpdateRequest) *dcgraphql.OptionalString { return &r.Description },
				func(r *DeviceUpdateRequest) *dcgraphql.OptionalString { return &r.Metadata },
				func(r *DeviceUpdateRequest) *dcgraphql.OptionalString { return &r.DeviceTypeToken },
			),
			optionalStringField("externalId", externalIdSeed, "ext-new",
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

func deviceTypeFamily() partialUpdateFamily {
	const (
		manufacturerSeed = "Acme"
		modelSeed        = "A-1000"
		profileSeed      = "profile-a"
	)
	return partialUpdateFamily{
		name:  "deviceType",
		token: "dt-1",
		migrate: []any{&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{},
			&MetricDefinition{}, &CommandDefinition{}, &DetectionRule{}, &DetectionRuleScopeRef{}},
		seed: func(t *testing.T, api *Api, ctx context.Context) {
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
		read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
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
		newRequest: func() any { return new(DeviceTypeUpdateRequest) },
		update: func(api *Api, ctx context.Context, token string, req any) error {
			_, err := api.UpdateDeviceType(ctx, token, req.(*DeviceTypeUpdateRequest))
			return err
		},
		fields: append(
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
			optionalStringField("manufacturer", manufacturerSeed, "Globex",
				func(r *DeviceTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Manufacturer }),
			optionalStringField("model", modelSeed, "B-2000",
				func(r *DeviceTypeUpdateRequest) *dcgraphql.OptionalString { return &r.Model }),
			// NULLABLE, unlike every other reference in this file: detaching a profile is
			// a state a device type can legitimately be in.
			optionalStringField("profileToken", profileSeed, "profile-b",
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
