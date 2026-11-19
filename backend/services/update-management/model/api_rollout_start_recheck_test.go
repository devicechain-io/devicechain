// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRolloutFreezeRechecksMembershipAfterIdentityLookup(t *testing.T) {
	for _, n := range []int{1, 3} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			e := newRolloutEnv(t, n)
			e.devices.incarnations = map[string]uint64{"acme/dev-0": 17}
			e.create(t, "r1", 10, nil)
			calls, lookedUp := 0, false
			e.groups.afterResolve = func() {
				calls++
				if calls == 1 {
					// R1 saw incarnation 17. Before L, replace it outside the group.
					e.devices.incarnations["acme/dev-0"] = 23
					if n == 1 {
						e.groups.set("acme", "fleet")
					} else {
						e.groups.set("acme", "fleet", "dev-2", "new-member", "dev-1")
					}
				} else {
					require.True(t, lookedUp, "R2 must follow L")
				}
			}
			e.api.Incarnations = incarnationLookupFunc(func(ctx context.Context, tenant string, tokens []string) (map[string]uint64, error) {
				ids, err := e.devices.DeviceIncarnations(ctx, tenant, tokens)
				require.Equal(t, uint64(23), ids["dev-0"], "L saw the replacement")
				lookedUp = true
				return ids, err
			})
			v, err := e.api.StartRollout(tenantCtx("acme"), "r1")
			require.Zero(t, e.assignmentCount(t, "dev-0"), "the non-member replacement must never be assigned")
			if n == 1 {
				requireRefusal(t, err, CodeInvalidRollout)
				require.Equal(t, RolloutDraft, e.rollout(t, "r1").Rollout.State)
				require.Empty(t, e.targets(t, "r1", nil))
			} else {
				require.NoError(t, err)
				require.Equal(t, 2, v.Rollout.TargetCount)
				targets := e.targets(t, "r1", nil)
				require.Len(t, targets, 2)
				require.Equal(t, "dev-1", targets[0].Target.DeviceToken, "preserve R1 order")
				require.Equal(t, "dev-2", targets[1].Target.DeviceToken)
				for _, target := range targets {
					require.Equal(t, int64(1), target.Target.DeviceIncarnation.Int64)
					require.Equal(t, TargetDispatched, target.Target.Status)
				}
			}
			require.Equal(t, 2, calls)
		})
	}
}

func TestRolloutFreezeRefusesUncertainSecondResolution(t *testing.T) {
	for _, name := range []string{"lookup failure", "rejected", "changed version", "lost version", "static became dynamic"} {
		t.Run(name, func(t *testing.T) {
			e := newRolloutEnv(t, 1)
			v1, v2 := int32(1), int32(2)
			if name != "static became dynamic" {
				e.groups.version = &v1
			}
			e.create(t, "r1", 1, nil)
			e.groups.afterResolve = func() {
				switch name {
				case "lookup failure":
					e.groups.err = errors.New("R2 unavailable")
				case "rejected":
					e.groups.rejected = map[string]string{"acme/fleet": "GROUP_NOT_FOUND"}
				case "lost version":
					e.groups.version = nil
				default:
					e.groups.version = &v2
				}
			}
			_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
			code := CodeRolloutGroupUnavailable
			if name == "rejected" {
				code = CodeRolloutGroupUnusable
			}
			requireRefusal(t, err, code)
			require.Equal(t, RolloutDraft, e.rollout(t, "r1").Rollout.State)
			require.Empty(t, e.targets(t, "r1", nil))
			require.Zero(t, e.assignmentCount(t, "dev-0"))
		})
	}
}
