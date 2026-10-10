// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// batchRow is the shape the hot batch inserts carry: the tenant is PROMOTED from an
// embedded TenantScoped, as on event-management's events and payload rows, not declared on
// the row itself.
type batchRow struct {
	TenantScoped
	EventId     []byte
	DeviceToken string
	Name        string
	Value       float64
}

func batchRows(n int) []*batchRow {
	rows := make([]*batchRow, n)
	for i := range rows {
		rows[i] = &batchRow{TenantScoped: TenantScoped{TenantId: "acme"}, DeviceToken: fmt.Sprintf("d-%d", i)}
	}
	return rows
}

// destTenants runs twice on every create — the tenant-scope callback's mismatch check and
// the erasure fence's statementTenants — over every row of the statement, so on a
// 64-row batch insert it runs 128 times per statement. Looking the field up by NAME on
// each row walks the struct's embedded fields and allocates every time; what the walk
// costs must not grow with the rows beyond the result slice itself.
func TestDestTenantsDoesNotAllocatePerRow(t *testing.T) {
	rows := batchRows(64)
	var got []string
	allocs := testing.AllocsPerRun(100, func() { got = destTenants(rows, "TenantId") })
	if len(got) != 64 || got[0] != "acme" || got[63] != "acme" {
		t.Fatalf("destTenants over 64 rows = %d values (%v...), want 64 x acme", len(got), got[:min(len(got), 2)])
	}
	// The result slice grows by doubling: at most ~8 allocations for 64 values. A lookup
	// that allocates per row costs 64 or more on top.
	if allocs > 16 {
		t.Fatalf("destTenants over 64 rows made %.0f allocations; want at most 16 (none per row)", allocs)
	}
}

// A field the struct does not have, a field reached through a non-nil embedded pointer, and
// a shadowed field read exactly as a by-name lookup reads them.
func TestDestTenantsCachedLookupKeepsByNameSemantics(t *testing.T) {
	type viaPointer struct {
		*TenantScoped
		Name string
	}
	type shadowed struct {
		TenantScoped
		TenantId int // shadows the promoted string field: not a tenant string
	}
	for name, tc := range map[string]struct {
		dest  any
		field string
		want  []string
	}{
		"promoted":               {[]batchRow{{TenantScoped: TenantScoped{TenantId: "a"}}}, "TenantId", []string{"a"}},
		"absent field":           {[]batchRow{{}}, "Tenant", nil},
		"via non-nil pointer":    {[]viaPointer{{TenantScoped: &TenantScoped{TenantId: "b"}}}, "TenantId", []string{"b"}},
		"shadowed by non-string": {[]shadowed{{TenantId: 7}}, "TenantId", nil},
		"mixed types in a slice": {[]any{batchRow{TenantScoped: TenantScoped{TenantId: "c"}}, projection{Tenant: "d"}}, "TenantId", []string{"c"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := destTenants(tc.dest, tc.field)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("destTenants = %v, want %v", got, tc.want)
			}
		})
	}
}

func BenchmarkDestTenants(b *testing.B) {
	for _, n := range []int{1, 16, 64} {
		rows := batchRows(n)
		b.Run(fmt.Sprintf("rows=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = destTenants(rows, "TenantId")
			}
		})
	}
}

// Two local types can share a printed name ("rdb.row") and still lay their fields out
// differently, so the cache must be keyed on the type itself, not on its name: a key
// built from t.String() would hand the second type the first one's index.
func TestDestTenantsCacheKeysOnTypeIdentityNotItsName(t *testing.T) {
	first := func() any {
		type row struct {
			TenantId string
			Name     string
		}
		return []row{{TenantId: "first", Name: "n"}}
	}()
	second := func() any {
		type row struct {
			Name     string
			Count    int
			TenantId string
		}
		return []row{{Name: "n", Count: 1, TenantId: "second"}}
	}()
	if a, b := reflect.TypeOf(first).Elem(), reflect.TypeOf(second).Elem(); a.String() != b.String() || a == b {
		t.Fatalf("fixture: want two distinct types with one printed name, got %s and %s", a, b)
	}
	for _, tc := range []struct {
		dest any
		want string
	}{{first, "first"}, {second, "second"}, {first, "first"}} {
		if got := destTenants(tc.dest, "TenantId"); len(got) != 1 || got[0] != tc.want {
			t.Fatalf("destTenants = %v, want [%s]", got, tc.want)
		}
	}
}

// The cache is shared by every statement of every goroutine. Run under -race, this fails
// on unsynchronised access to it.
func TestDestTenantsIsSafeForConcurrentUse(t *testing.T) {
	type a struct {
		TenantScoped
		X int
	}
	type b struct {
		Y        string
		TenantId string
	}
	type c struct{ Tenant string }
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				field := []string{"TenantId", "Tenant", fmt.Sprintf("Missing%d", i%7)}[(g+i)%3]
				for _, dest := range []any{
					[]a{{TenantScoped: TenantScoped{TenantId: "t"}}},
					[]b{{TenantId: "t"}},
					[]c{{Tenant: "t"}},
				} {
					got := destTenants(dest, field)
					want := reflect.ValueOf(dest).Index(0).FieldByName(field)
					if want.IsValid() != (len(got) == 1) {
						t.Errorf("destTenants(%T, %s) = %v, by-name valid=%v", dest, field, got, want.IsValid())
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()
}
