// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package pgtest

import (
	"errors"
	"testing"
)

type recorder struct {
	fatal   bool
	message string
}

func (r *recorder) Helper() {}
func (r *recorder) Fatalf(format string, args ...any) {
	r.fatal = true
	r.message = format
}

func TestMissingDSNFailsNeverSkips(t *testing.T) {
	r := &recorder{}
	MustDSN(r, func(string) string { return "" })
	if !r.fatal {
		t.Fatal("a missing DC_FLEET_PG_DSN did not fail the test: the harness must never skip")
	}
	if _, err := ResolveDSN(func(string) string { return "" }); !errors.Is(err, ErrNoDSN) {
		t.Fatalf("want ErrNoDSN, got %v", err)
	}
}

func TestPresentDSNIsReturned(t *testing.T) {
	r := &recorder{}
	got := MustDSN(r, func(k string) string {
		if k != EnvDSN {
			t.Fatalf("read %q, want %q", k, EnvDSN)
		}
		return "postgres://x"
	})
	if r.fatal || got != "postgres://x" {
		t.Fatalf("a present DSN was refused or altered: %q fatal=%v", got, r.fatal)
	}
}
