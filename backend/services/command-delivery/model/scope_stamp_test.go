// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

// anchorBook is a canned device-management: who owns which device.
type anchorBook struct {
	owners map[string][]string
	calls  int
	asked  []int
	fail   bool
}

func (b *anchorBook) DeviceScopeAnchors(_ context.Context, tokens []string) (map[string][]string, error) {
	b.calls++
	b.asked = append(b.asked, len(tokens))
	if b.fail {
		return nil, errors.New("device-management is unreachable")
	}
	out := map[string][]string{}
	for _, t := range tokens {
		if a, ok := b.owners[t]; ok {
			out[t] = a
		}
	}
	return out, nil
}

func stampOf(t *testing.T, api *Api, ctx context.Context, token string) rdb.Anchors {
	t.Helper()
	got, err := api.CommandsByToken(ctx, []string{token})
	require.NoError(t, err)
	require.Len(t, got, 1)
	return got[0].ScopeAnchors
}

// A single command is stamped with the anchors the enqueue gate reported for its device
// (ADR-081), whoever sends it — REACT's service token included, since that is how a
// customer comes to see the commands its rules sent. No gate means no stamp.
func TestCreateCommandStampsTheGatesAnchors(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")
	api := newTestApi(t)
	api.EnqueueValidator = &stubValidator{anchors: []string{"customer:acme", "area:north"}}
	_, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "c1", DeviceToken: "d1", Name: "reboot"})
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{"customer:acme", "area:north"}, stampOf(t, api, ctx, "c1"))

	api.EnqueueValidator = &stubValidator{anchors: []string{}}
	_, err = api.CreateCommand(ctx, &CommandCreateRequest{Token: "c2", DeviceToken: "d2", Name: "reboot"})
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{}, stampOf(t, api, ctx, "c2"), "an owner-less device is stamped empty")

	api.EnqueueValidator = nil
	_, err = api.CreateCommand(ctx, &CommandCreateRequest{Token: "c3", DeviceToken: "d3", Name: "reboot"})
	require.NoError(t, err)
	require.Nil(t, stampOf(t, api, ctx, "c3"), "with no gate there is nothing to stamp")
}

// A batch stamps each admitted device's own anchors, read in chunks the owner's lookup
// bound admits; a device the owner does not name is written unstamped.
func TestABatchStampsEachDevicesAnchors(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	api := newBatchTestApi(t)
	api.BatchValidator = &countingValidator{}
	devices := make([]string, 0, rdb.MaxLookupKeys+5)
	owners := map[string][]string{}
	for i := 0; i < rdb.MaxLookupKeys+5; i++ {
		d := fmt.Sprintf("pump-%05d", i)
		devices = append(devices, d)
		if i%2 == 0 {
			owners[d] = []string{"customer:acme"}
		} else if i != 1 {
			owners[d] = []string{"customer:globex"}
		}
	}
	book := &anchorBook{owners: owners}
	api.DeviceAnchors = book

	_, err := api.CreateCommandBatch(ctx, &CommandBatchCreateRequest{Token: "b1", Name: "reboot", DeviceTokens: &devices})
	require.NoError(t, err)
	require.Equal(t, []int{rdb.MaxLookupKeys, 5}, book.asked, "the lookup must be chunked at its bound")

	got := map[string]rdb.Anchors{}
	for _, c := range commandsOf(t, api, ctx, "b1") {
		got[c.DeviceToken] = c.ScopeAnchors
	}
	require.Len(t, got, len(devices))
	require.Equal(t, rdb.Anchors{"customer:acme"}, got["pump-00000"])
	require.Nil(t, got["pump-00001"], "a device the owner did not name is unstamped")
	require.Equal(t, rdb.Anchors{"customer:globex"}, got["pump-00003"])
	require.Equal(t, rdb.Anchors{"customer:acme"}, got[devices[rdb.MaxLookupKeys+4]])
}

// A batch whose stamps cannot be read writes nothing.
func TestABatchFailsClosedWhenItsStampsCannotBeRead(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	api := newBatchTestApi(t)
	api.BatchValidator = &countingValidator{}
	api.DeviceAnchors = &anchorBook{fail: true}
	devices := []string{"pump-1", "pump-2"}
	_, err := api.CreateCommandBatch(ctx, &CommandBatchCreateRequest{Token: "b1", Name: "reboot", DeviceTokens: &devices})
	require.Error(t, err)
	require.Empty(t, commandsOf(t, api, ctx, "b1"))
}
