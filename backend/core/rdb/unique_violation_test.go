// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"errors"
	"testing"
)

// The two messages this function exists to recognise, quoted from the drivers rather than
// paraphrased. They are the whole contract: if either driver changes its wording, this
// test is what says so, and the alternative — asserting against a message this file made
// up — would keep passing while production stopped being translated.
const (
	postgresViolation = `ERROR: duplicate key value violates unique constraint ` +
		`"uix_connectors_tenant_token" (SQLSTATE 23505)`
	sqliteViolation = `constraint failed: UNIQUE constraint failed: ` +
		`connectors.tenant_id, connectors.token (2067)`
)

func TestIsUniqueViolationRecognisesBothDrivers(t *testing.T) {
	cases := map[string]struct {
		err     error
		index   string
		columns []string
		want    bool
	}{
		"postgres names the index": {
			errors.New(postgresViolation), "uix_connectors_tenant_token",
			[]string{"connectors.token"}, true,
		},
		"sqlite names the columns": {
			errors.New(sqliteViolation), "uix_connectors_tenant_token",
			[]string{"connectors.token"}, true,
		},
		// The index name is what Postgres prints, so a caller who knows only the columns
		// still has to work against SQLite — and vice versa. Each spelling is recognised
		// on its own evidence rather than needing both to be present.
		"sqlite matches with no index name supplied": {
			errors.New(sqliteViolation), "", []string{"connectors.token"}, true,
		},
		"postgres matches with no columns supplied": {
			errors.New(postgresViolation), "uix_connectors_tenant_token", nil, true,
		},

		// ─── the counterweights ────────────────────────────────────────────
		//
		// 🔴 A MATCHER THAT SAID YES TO EVERYTHING WOULD PASS EVERY CASE ABOVE. These are
		// what make the four of them mean something.
		"nil is not a violation": {nil, "uix_connectors_tenant_token", []string{"connectors.token"}, false},
		"an unrelated failure is not a collision": {
			errors.New("connection refused"), "uix_connectors_tenant_token",
			[]string{"connectors.token"}, false,
		},
		// 🔴 THE ONE THAT MATTERS MOST. A uniqueness error on a DIFFERENT index of the
		// same service must not be translated into this one's sentence: telling a caller
		// their token is taken when a version number collided is a worse answer than the
		// raw driver text, because it is confidently wrong rather than merely unhelpful.
		"another index on another table": {
			errors.New(`UNIQUE constraint failed: connector_versions.connector_id, connector_versions.version`),
			"uix_connectors_tenant_token", []string{"connectors.token"}, false,
		},
		"another index on the SAME table": {
			errors.New(`duplicate key value violates unique constraint "uix_connectors_tenant_external_id"`),
			"uix_connectors_tenant_token", []string{"connectors.token"}, false,
		},
		// Supplying neither piece of evidence must not make the function answer yes to a
		// bare uniqueness marker — that would translate every collision in the process.
		"no index and no columns":           {errors.New(sqliteViolation), "", nil, false},
		"an empty column is not a wildcard": {errors.New(sqliteViolation), "", []string{""}, false},
		// Every named column must be present, so a caller naming a pair does not match on
		// half of it.
		"one of two columns missing": {
			errors.New(sqliteViolation), "",
			[]string{"connectors.token", "connectors.external_id"}, false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := IsUniqueViolation(tc.err, tc.index, tc.columns...); got != tc.want {
				t.Fatalf("IsUniqueViolation(%v, %q, %v) = %v, want %v",
					tc.err, tc.index, tc.columns, got, tc.want)
			}
		})
	}
}

// 🔴 THE MARKER IS NOT SUFFICIENT ON ITS OWN, stated as its own property because it is the
// mistake the signature is shaped to prevent. "UNIQUE constraint failed" appears in every
// SQLite uniqueness error in the process; a matcher keyed on it alone would answer yes for
// every table, and the case above proves only that ONE other table is excluded.
func TestTheUniquenessMarkerAloneIsNotAMatch(t *testing.T) {
	for _, msg := range []string{
		"UNIQUE constraint failed: iam_roles.token",
		"UNIQUE constraint failed: secrets.tenant_id, secrets.scope, secrets.name",
		"UNIQUE constraint failed",
	} {
		if IsUniqueViolation(errors.New(msg), "uix_connectors_tenant_token", "connectors.token") {
			t.Fatalf("%q was read as a connector-token collision", msg)
		}
	}
}
