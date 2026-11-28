// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intentseam_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/internal/intentseam"
)

// The SQL rules of the seam, checked on the statements it actually builds rather than
// remembered: every reference to a device-management table names its own tenant predicate
// (bound to the caller's tenant) and its own liveness predicate in its own clause, every join is
// a LEFT JOIN whose predicates sit in its ON clause, and the selector fragment sits inside the
// scoped outer devices row.

// seamTables are the device-management tables the seam reads. Each soft-deletes.
var seamTables = []string{
	"devices", "device_types", "device_profiles", "device_profile_versions",
	"entity_attributes", "entity_groups", "entity_group_versions",
}

var tableRef = regexp.MustCompile(`\b(FROM|JOIN)\s+(` + strings.Join(seamTables, "|") + `)\b(?:\s+([a-z][a-z0-9_]*))?`)

// clauseEnd ends the clause a reference owns: the next FROM, JOIN, SELECT or CTE boundary.
var clauseEnd = regexp.MustCompile(`\bFROM\b|\bLEFT JOIN\b|\bJOIN\b|\bSELECT\b|\n\)`)

var sqlKeywords = map[string]bool{"WHERE": true, "ON": true, "LEFT": true, "JOIN": true, "ORDER": true, "LIMIT": true, "AND": true}

// checkSeamSQL returns every violation of the rules in q, whose positional "?" args are args.
func checkSeamSQL(q string, args []any, tenant string) []string {
	var bad []string
	if strings.Count(q, "?") != len(args) {
		bad = append(bad, fmt.Sprintf("%d placeholders for %d args", strings.Count(q, "?"), len(args)))
		return bad
	}
	for _, m := range regexp.MustCompile(`\b(INNER |RIGHT |FULL |CROSS |)JOIN\b`).FindAllStringSubmatchIndex(q, -1) {
		if !strings.HasSuffix(strings.TrimRight(q[:m[0]], " "), "LEFT") {
			bad = append(bad, fmt.Sprintf("a join that is not a LEFT JOIN at %q", excerpt(q, m[0])))
		}
	}
	refs := tableRef.FindAllStringSubmatchIndex(q, -1)
	if len(refs) == 0 {
		bad = append(bad, "the statement names no seam table at all")
	}
	for _, m := range refs {
		kind, table := q[m[2]:m[3]], q[m[4]:m[5]]
		alias := table
		if m[6] >= 0 && !sqlKeywords[strings.ToUpper(q[m[6]:m[7]])] {
			alias = q[m[6]:m[7]]
		}
		start := m[1]
		end := len(q)
		if loc := clauseEnd.FindStringIndex(q[start:]); loc != nil {
			end = start + loc[0]
		}
		clause := q[start:end]
		if kind == "JOIN" && strings.Contains(clause, "WHERE") {
			bad = append(bad, fmt.Sprintf("%s %s: a WHERE inside its join clause can drop a member", kind, table))
		}
		tp := alias + ".tenant_id = ?"
		i := strings.Index(clause, tp)
		if i < 0 {
			bad = append(bad, fmt.Sprintf("%s %s %s: no %s in its own clause", kind, table, alias, tp))
		} else {
			pos := start + i + len(tp) - 1
			if arg := args[strings.Count(q[:pos], "?")]; arg != tenant {
				bad = append(bad, fmt.Sprintf("%s %s: its tenant placeholder is bound to %v, not the caller's tenant", kind, table, arg))
			}
		}
		if !strings.Contains(clause, alias+".deleted_at IS NULL") {
			bad = append(bad, fmt.Sprintf("%s %s %s: no %s.deleted_at IS NULL in its own clause", kind, table, alias, alias))
		}
	}
	return bad
}

func excerpt(q string, at int) string {
	return q[max(0, at-30):min(len(q), at+40)]
}

// selectors the checks run over: plain, negated, numeric, a presence test, and a compound.
var seamSelectors = []string{
	`attr["zone"] == "quay"`,
	`!(attr["zone"] == "quay")`,
	`attr["power"] > 5`,
	`"retired" in attr`,
	`attr["zone"] == "quay" || (attr["power"] <= 2 && !("retired" in attr))`,
}

func TestSeamSQLNamesItsTenantOnEveryTable(t *testing.T) {
	const tenant = "tenant-a"
	statements := map[string]func(string) (string, []any, error){
		"digest": func(s string) (string, []any, error) { return intentseam.DigestSQL(tenant, "dim", s) },
		"page":   func(s string) (string, []any, error) { return intentseam.PageSQL(tenant, "dim", s, 0, 1000) },
		"intersect": func(s string) (string, []any, error) {
			return intentseam.IntersectSQL(tenant, s, `attr["zone"] == "east"`)
		},
	}
	for name, build := range statements {
		for _, sel := range seamSelectors {
			q, args, err := build(sel)
			if err != nil {
				t.Fatalf("%s %s: %v", name, sel, err)
			}
			for _, v := range checkSeamSQL(q, args, tenant) {
				t.Errorf("%s over %s: %s", name, sel, v)
			}
			// The selector fragment is AND-ed inside the scoped outer row, never the whole
			// predicate.
			if !strings.Contains(q, "WHERE devices.tenant_id = ? AND devices.deleted_at IS NULL AND (") {
				t.Errorf("%s over %s: the outer devices row is not scoped before the fragment", name, sel)
			}
		}
	}
	for name, q := range map[string]string{"group": intentseam.GroupSQL, "group version": intentseam.GroupVersionSQL} {
		args := []any{tenant, "x"}
		if name == "group version" {
			args = append(args, 1)
		}
		for _, v := range checkSeamSQL(q, args, tenant) {
			t.Errorf("%s lookup: %s", name, v)
		}
	}
}

// The checker's own negative controls: each planted defect must be found.
func TestTheSeamSQLCheckCanFail(t *testing.T) {
	const tenant = "tenant-a"
	q, args, err := intentseam.DigestSQL(tenant, "dim", `!(attr["zone"] == "quay")`)
	if err != nil {
		t.Fatal(err)
	}
	if v := checkSeamSQL(q, args, tenant); len(v) != 0 {
		t.Fatalf("the unplanted statement is not clean: %v", v)
	}
	dropArg := func(q string, args []any, pred string) (string, []any) {
		i := strings.Index(q, pred)
		n := strings.Count(q[:i+len(pred)], "?") - 1
		return strings.Replace(q, pred, "", 1), append(append([]any{}, args[:n]...), args[n+1:]...)
	}
	planted := map[string]func() (string, []any){
		"outer tenant predicate removed": func() (string, []any) {
			return dropArg(q, args, "devices.tenant_id = ? AND ")
		},
		"a joined table unscoped": func() (string, []any) {
			return dropArg(q, args, "dp.tenant_id = ? AND ")
		},
		"an inner join": func() (string, []any) {
			return strings.Replace(q, "LEFT JOIN device_profiles", "JOIN device_profiles", 1), args
		},
		"a liveness predicate removed": func() (string, []any) {
			return strings.Replace(q, "ea.deleted_at IS NULL AND ", "", 1), args
		},
		"a tenant placeholder bound to another value": func() (string, []any) {
			out := append([]any{}, args...)
			for i, a := range out {
				if a == tenant {
					out[i] = "tenant-b"
					break
				}
			}
			return q, out
		},
		"a selector attribute probe unscoped": func() (string, []any) {
			return dropArg(q, args, "ea.tenant_id = ? AND ea.entity_type")
		},
	}
	for name, plant := range planted {
		pq, pargs := plant()
		if pq == q && fmt.Sprint(pargs) == fmt.Sprint(args) {
			t.Fatalf("%s: the plant changed nothing", name)
		}
		if v := checkSeamSQL(pq, pargs, tenant); len(v) == 0 {
			t.Errorf("%s: the check did not notice", name)
		}
	}
}
