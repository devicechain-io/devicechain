// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partialupdatetest

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// A TOY SERVICE, SO THE HARNESS HAS SOMETHING OF ITS OWN TO DRIVE.
//
// 🔴 IT IS NOT A CONVENIENCE FIXTURE. Two things about this package can only be measured
// here:
//
//   - the LIST field kind. No shipped *UpdateRequest carries a list yet — the slices that
//     add them come after this one — so without a family of its own, every line of
//     OptionalStringListField and the EmptyListIsTheSameAsANull property would ship
//     unexecuted, and the first service to declare a list field would be the one
//     discovering whether they work.
//   - whether the harness still FAILS. Every property here is a claim about a defect it
//     would catch, and a claim like that is worth nothing until the defect has been shown
//     to produce a red. See the negative controls at the bottom of this file, which run
//     deliberately broken versions of this same toy service in a subprocess.
//
// The toy is deliberately shaped like the real thing: a nullable column, a NOT NULL
// vocabulary column, a NOT NULL reference, and a list.

type demoOwner struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
}

type demoThing struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	Name    sql.NullString
	Kind    string
	Tags    string
	OwnerId uint
	Owner   *demoOwner
}

// demoUpdateRequest is a converted update input: no token, every field three-state.
type demoUpdateRequest struct {
	Name       dcgraphql.OptionalString
	Kind       dcgraphql.OptionalString
	Tags       dcgraphql.OptionalStringList
	OwnerToken dcgraphql.OptionalString
}

// demoOwnerFullReplaceRequest is the UNCONVERTED shape, kept so the guard's exemption
// path is exercised by something rather than described.
type demoOwnerFullReplaceRequest struct {
	Token string
	Name  *string
}

type demoApi struct {
	db *gorm.DB
	// fullReplace makes UpdateDemoThing ignore the Set flags and write from Value, which
	// is the exact defect the harness exists to catch. Only the negative control turns it
	// on.
	fullReplace bool
}

func (a *demoApi) UpdateDemoThing(ctx context.Context, token string,
	req *demoUpdateRequest) (*demoThing, error) {
	var found demoThing
	if err := a.db.WithContext(ctx).Preload("Owner").
		Where("token = ?", token).First(&found).Error; err != nil {
		return nil, err
	}

	// The reference is resolved BEFORE anything is folded, so an unknown token refuses
	// the whole update rather than half-applying it.
	if req.OwnerToken.Set {
		if req.OwnerToken.Value == nil || strings.TrimSpace(*req.OwnerToken.Value) == "" {
			return nil, errors.New("ownerToken cannot be cleared: every thing must reference one")
		}
		wanted := strings.TrimSpace(*req.OwnerToken.Value)
		current := ""
		if found.Owner != nil {
			current = found.Owner.Token
		}
		if wanted != current {
			var owner demoOwner
			if err := a.db.WithContext(ctx).Where("token = ?", wanted).First(&owner).Error; err != nil {
				return nil, err
			}
			found.OwnerId = owner.ID
			found.Owner = &owner
		}
	}

	if a.fullReplace {
		found.Name = nullStringFrom(req.Name.Value)
		if req.Kind.Value != nil {
			found.Kind = *req.Kind.Value
		} else {
			found.Kind = ""
		}
		found.Tags = RenderStringList(req.Tags.Value)
	} else {
		kind, err := req.Kind.ApplyToRequired("kind", found.Kind)
		if err != nil {
			return nil, err
		}
		found.Kind = kind
		found.Name = applyNullString(req.Name, found.Name)
		found.Tags = RenderStringList(req.Tags.ApplyTo(ParseStringList(found.Tags)))
	}

	if err := a.db.WithContext(ctx).Save(&found).Error; err != nil {
		return nil, err
	}
	return &found, nil
}

// UpdateDemoOwner is the unconverted update. It exists so the guard's exemption path has
// something real to describe.
func (a *demoApi) UpdateDemoOwner(ctx context.Context, token string,
	req *demoOwnerFullReplaceRequest) (*demoOwner, error) {
	var found demoOwner
	if err := a.db.WithContext(ctx).Where("token = ?", token).First(&found).Error; err != nil {
		return nil, err
	}
	return &found, nil
}

func (a *demoApi) CreateDemoOwner(ctx context.Context, token string) error {
	return a.db.WithContext(ctx).Create(&demoOwner{
		TokenReference: rdb.TokenReference{Token: token},
	}).Error
}

func applyNullString(o dcgraphql.OptionalString, current sql.NullString) sql.NullString {
	var p *string
	if current.Valid {
		v := current.String
		p = &v
	}
	return nullStringFrom(o.ApplyTo(p))
}

func nullStringFrom(v *string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *v, Valid: true}
}

// ─── the toy service's suite ───────────────────────────────────────────────

const (
	demoToken       = "thing-1"
	demoOwnerToken  = "owner-a"
	demoOwnerOther  = "owner-b"
	demoSeededName  = "Original name"
	demoSeededKind  = "gauge"
	demoStrictValid = "a-valid_token1"
)

var demoSeededTags = []string{"alpha", "beta"}

func demoTables() []any { return []any{&demoThing{}, &demoOwner{}} }

func demoSuite(fullReplace bool) Suite[*demoApi] {
	return Suite[*demoApi]{
		NewApi: func(t *testing.T, tables ...any) *demoApi {
			return &demoApi{db: NewSQLiteDB(t, tables...), fullReplace: fullReplace}
		},
		Context:  TenantContext("acme"),
		Families: demoFamilies(),
		CreateWithToken: func(t *testing.T, api *demoApi, ctx context.Context, token string) error {
			return api.CreateDemoOwner(ctx, token)
		},
		StrictnessTables: []any{&demoOwner{}},
		ValidToken:       demoStrictValid,
	}
}

func demoFamilies() []Family[*demoApi] {
	return []Family[*demoApi]{{
		Name:    "demoThing",
		Token:   demoToken,
		Migrate: demoTables(),
		Seed: func(t *testing.T, api *demoApi, ctx context.Context) {
			t.Helper()
			for _, tok := range []string{demoOwnerToken, demoOwnerOther} {
				if err := api.CreateDemoOwner(ctx, tok); err != nil {
					t.Fatalf("seed owner %q: %v", tok, err)
				}
			}
			var owner demoOwner
			if err := api.db.WithContext(ctx).Where("token = ?", demoOwnerToken).
				First(&owner).Error; err != nil {
				t.Fatalf("reload seeded owner: %v", err)
			}
			if err := api.db.WithContext(ctx).Create(&demoThing{
				TokenReference: rdb.TokenReference{Token: demoToken},
				Name:           sql.NullString{String: demoSeededName, Valid: true},
				Kind:           demoSeededKind,
				Tags:           RenderStringList(demoSeededTags),
				OwnerId:        owner.ID,
			}).Error; err != nil {
				t.Fatalf("seed thing: %v", err)
			}
		},
		Read: func(t *testing.T, api *demoApi, ctx context.Context) map[string]string {
			t.Helper()
			var rows []*demoThing
			err := api.db.WithContext(ctx).Preload("Owner").
				Where("token = ?", demoToken).Find(&rows).Error
			e := RequireOne(t, "demo thing", rows, err)
			ownerToken := NullMarker
			if e.Owner != nil {
				ownerToken = e.Owner.Token
			}
			return map[string]string{
				"name":       NullString(e.Name),
				"kind":       e.Kind,
				"tags":       e.Tags,
				"ownerToken": ownerToken,
			}
		},
		NewRequest: func() any { return new(demoUpdateRequest) },
		Update: func(api *demoApi, ctx context.Context, token string, req any) error {
			_, err := api.UpdateDemoThing(ctx, token, req.(*demoUpdateRequest))
			return err
		},
		Fields: []Field{
			OptionalStringField("name", demoSeededName, "Renamed",
				func(r *demoUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			RequiredStringField("kind", demoSeededKind, "wrench",
				func(r *demoUpdateRequest) *dcgraphql.OptionalString { return &r.Kind }),
			OptionalStringListField("tags", demoSeededTags, []string{"gamma"},
				func(r *demoUpdateRequest) *dcgraphql.OptionalStringList { return &r.Tags }),
			RequiredRefField("ownerToken", demoOwnerToken, demoOwnerOther,
				func(r *demoUpdateRequest) *dcgraphql.OptionalString { return &r.OwnerToken }),
		},
	}}
}

// THE POSITIVE CONTROL. Every property, over a family that includes a LIST field — which
// is the only place in the tree today where the list field kind is executed at all.
func TestHarnessDrivesEveryPropertyIncludingLists(t *testing.T) {
	Run(t, demoSuite(false))
}

// The guard, over the toy service's own two updates: one converted and registered, one
// unconverted and exempt.
func TestGuardOverTheToyService(t *testing.T) {
	AssertEveryUpdateTakesADedicatedRequest(t, UpdateSurface[*demoApi]{
		Families: demoFamilies(),
		Exempt: map[string]string{
			"UpdateDemoOwner": "demoOwnerFullReplaceRequest",
		},
		MinUpdateMethods: 2,
	})
}

// RenderStringList / ParseStringList have to round-trip, or the list field's Set closure
// and its Read reading are describing two different lists.
//
// The empty case is the one that matters: "" and "[]" and NullMarker are three readings a
// list column genuinely has, and a rendering that collapsed any two would make "the
// caller emptied it" unobservable.
func TestStringListRoundTripsAndKeepsTheEmptyCaseDistinct(t *testing.T) {
	for _, v := range [][]string{nil, {}, {"a"}, {"a", "b"}, {"a", "a"}, {"b", "a"}} {
		rendered := RenderStringList(v)
		back := ParseStringList(rendered)
		if RenderStringList(back) != rendered {
			t.Errorf("%q did not round-trip: got %q", rendered, RenderStringList(back))
		}
	}
	empty := RenderStringList(nil)
	if empty == "" || empty == NullMarker {
		t.Fatalf("an empty list renders as %q, which collides with a blank column or a NULL "+
			"one — three states a list column has and this harness must tell apart", empty)
	}
	if RenderStringList([]string{""}) == empty {
		t.Fatal("a one-entry list holding the empty string renders the same as an empty list")
	}
}

// A list field seeded EMPTY is refused at declaration, because the generic anti-vacuity
// control cannot see it: "[]" is neither blank nor NullMarker, so "preserved" and "never
// set" would be the same observation with every property green.
func TestAListFieldSeededEmptyIsRefused(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("declaring a list field with an empty seed was allowed, so a family could " +
				"assert nothing about it and stay green")
		}
	}()
	OptionalStringListField("tags", nil, []string{"a"},
		func(r *demoUpdateRequest) *dcgraphql.OptionalStringList { return &r.Tags })
}
