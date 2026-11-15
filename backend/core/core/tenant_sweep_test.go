// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestForEachTenant(t *testing.T) {
	broken := errors.New("tenant failed")
	stop := fmt.Errorf("broker: %w", ErrStopSweep)
	for _, tc := range []struct {
		name    string
		tenants []string
		fail    map[string]error
		cancel  bool
		want    TenantSweep
		calls   []string
		is      error
	}{
		{name: "order and isolation", tenants: []string{"B", "A", "C"}, fail: map[string]error{"A": broken}, want: TenantSweep{3, 1}, calls: []string{"B", "A", "C"}, is: ErrPassPartial},
		{name: "cancel between tenants", tenants: []string{"A", "B"}, cancel: true, want: TenantSweep{1, 0}, calls: []string{"A"}, is: context.Canceled},
		{name: "wrapped stop", tenants: []string{"A", "B"}, fail: map[string]error{"A": stop}, want: TenantSweep{1, 1}, calls: []string{"A"}, is: stop},
		{name: "empty token", tenants: []string{"", "B"}, want: TenantSweep{2, 1}, calls: []string{"B"}, is: ErrPassPartial},
		{name: "one of one", tenants: []string{"A"}, fail: map[string]error{"A": broken}, want: TenantSweep{1, 1}, calls: []string{"A"}, is: ErrPassPartial},
		{name: "all of two", tenants: []string{"A", "B"}, fail: map[string]error{"A": broken, "B": broken}, want: TenantSweep{2, 2}, calls: []string{"A", "B"}, is: broken},
		{name: "zero visited"},
		{name: "success", tenants: []string{"A"}, want: TenantSweep{1, 0}, calls: []string{"A"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(WithTenant(context.Background(), "parent"))
			defer cancel()
			var calls []string
			got, err := ForEachTenant(ctx, tc.tenants, func(tctx context.Context, tenant string) error {
				if value, ok := TenantFromContext(tctx); !ok || value != tenant {
					t.Fatalf("context tenant = %q, %v; want %q", value, ok, tenant)
				}
				calls = append(calls, tenant)
				if tc.cancel {
					cancel()
				}
				return tc.fail[tenant]
			})
			if got != tc.want || !reflect.DeepEqual(calls, tc.calls) || !errors.Is(err, tc.is) {
				t.Fatalf("got %+v, calls %v, error %v; want %+v, calls %v, error %v", got, calls, err, tc.want, tc.calls, tc.is)
			}
			if tc.name == "all of two" && errors.Is(err, ErrPassPartial) {
				t.Fatal("all of two must be a plain failure")
			}
		})
	}
}
