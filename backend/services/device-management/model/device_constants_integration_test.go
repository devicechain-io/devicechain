// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Per-device constants on the real engine. devices.metadata is jsonb, so what the resolver and
// the constants door scan is NOT what the caller wrote: Postgres drops repeated keys (keeping the
// last), decodes escapes, re-spells numbers and re-lays whitespace and key order. The scanner is
// held to encoding/json on the bytes that come back, and those answers to encoding/json on the
// bytes that went in.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run ConstantsOnPostgres -v
package model

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeviceConstantsScannerAgreesOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	tenant := fmt.Sprintf("c6b%d", time.Now().UnixNano())
	ctx := core.WithTenant(context.Background(), tenant)
	seedType(t, api, ctx, "meter", "meter-profile")

	keys := []string{"cal", "é", "a\"b", "tab\tkey", "\u2028", "x"}
	big := `{"pad": "` + strings.Repeat("{[\\\"}],", 8000) + `", "cal": 7.25}`
	docs := map[string]string{
		"dup":         `{"cal": 1.5, "x": 0, "cal": 2.5}`,
		"escaped-key": `{"c\u0061l": 3, "\u00e9": 4, "a\"b": 5, "tab\tkey": 6, "\u2028": 7}`,
		"dup-escaped": `{"é": 1, "\u00e9": 2}`,
		"neg-zero":    `{"cal": -0}`,
		"neg-zero-e":  `{"cal": -0.0e+5}`,
		"exponents":   `{"cal": 1.0E2, "x": 25e-1}`,
		"huge":        `{"cal": 1e400, "x": -1e400}`,
		"tiny":        `{"cal": 1e-400, "x": 4.9e-324}`,
		"precise":     `{"cal": 12345678901234567890.123456789, "x": 0.1}`,
		"max":         `{"cal": 1.7976931348623157e308}`,
		"kinds":       `{"cal": "1.5", "x": null, "é": true, "a\"b": [1], "tab\tkey": {"cal": 1}}`,
		"nested":      `{"o": {"cal": 99, "s": "}\"{"}, "cal": 8, "l": [[[[{"cal": 1}]]]]}`,
		"spaces":      " \n {  \"cal\"\t:\r\n 9 \n}  ",
		"not-object":  `[{"cal": 1}]`,
		"scalar":      `5`,
		"string":      `"{\"cal\": 1}"`,
		"empty":       `{}`,
		"big":         big,
	}
	for name, md := range docs {
		md := md
		_, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: name, DeviceTypeToken: "meter", Metadata: &md})
		require.NoError(t, err, name)
	}

	tokens := make([]string, 0, len(docs))
	for name := range docs {
		tokens = append(tokens, name)
	}
	devices, err := api.DevicesByToken(ctx, tokens)
	require.NoError(t, err)
	require.Len(t, devices, len(docs))
	stored := map[string][]byte{}
	for _, d := range devices {
		require.NotNil(t, d.Metadata, d.Token)
		stored[d.Token] = []byte(*d.Metadata)
	}

	for name, written := range docs {
		pg := stored[name]
		got, gotObj := scannedConstants(pg, keys)
		wantPG, wantPGObj := constantsOracle(pg, keys)
		assert.Equal(t, wantPGObj, gotObj, "%s: object-ness of %s", name, pg)
		assert.Equal(t, wantPG, got, "%s: the scanner disagrees with encoding/json on the stored bytes %s", name, pg)
		wantIn, _ := constantsOracle([]byte(written), keys)
		if name == "neg-zero" || name == "neg-zero-e" {
			// numeric has no negative zero: -0 is stored as 0. Same value, other sign bit.
			assert.Equal(t, map[string]string{"cal": "n:0"}, got, "%s: stored %s", name, pg)
			continue
		}
		if name != "big" {
			t.Logf("%-12s written %-60s stored %s", name, written, pg)
		}
		assert.Equal(t, wantIn, got, "%s: what was written is what is read (stored %s)", name, pg)
	}

	// The door reads the same bytes through the same reader.
	byToken := map[string][]DeviceConstant{}
	var after uint64
	for {
		page, err := api.DeviceConstantsPage(ctx, "meter-profile", keys, after, 7)
		require.NoError(t, err)
		for _, e := range page.Entries {
			byToken[e.DeviceToken] = e.Constants
		}
		if page.NextCursor == nil {
			break
		}
		after = *page.NextCursor
	}
	for name := range docs {
		want := readConstants(stored[name], keys)
		if len(want) == 0 {
			assert.NotContains(t, byToken, name)
			continue
		}
		require.Contains(t, byToken, name)
		assert.Len(t, byToken[name], len(want), name)
	}
	f := func(v float64) *float64 { return &v }
	assert.Equal(t, []DeviceConstant{{Key: "cal", Number: f(2.5)}, {Key: "x", Number: f(0)}}, sortedConstants(byToken["dup"]))
	assert.Equal(t, []DeviceConstant{{Key: "cal", Number: f(7.25)}}, byToken["big"])
}

func sortedConstants(cs []DeviceConstant) []DeviceConstant {
	out := append([]DeviceConstant{}, cs...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j].Key < out[i].Key {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// A member limited to one customer reaches the door only through the store's scope rule, which
// lists that customer's devices and no other's: the metadata of another customer's device is
// never read for it. (The GraphQL door refuses a scoped caller outright, before this.)
func TestDeviceConstantsPageIsScopedOnPostgres(t *testing.T) {
	r := newScopeAlarmRig(t)
	ctx := r.operator
	seedType(t, r.api, ctx, "meter", "meter-profile")
	for _, c := range []string{"acme", "globex"} {
		md := fmt.Sprintf(`{"cal": %d}`, len(c))
		_, err := r.api.CreateDevice(ctx, &DeviceCreateRequest{Token: c + "-meter", DeviceTypeToken: "meter", Metadata: &md})
		require.NoError(t, err)
		r.assign(t, c+"-meter", c)
	}
	tokens := func(c context.Context) ([]string, error) {
		page, err := r.api.DeviceConstantsPage(c, "meter-profile", []string{"cal"}, 0, 100)
		if err != nil {
			return nil, err
		}
		out := []string{}
		for _, e := range page.Entries {
			out = append(out, e.DeviceToken)
		}
		return out, nil
	}
	all, err := tokens(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"acme-meter", "globex-meter"}, all)

	acme, err := tokens(r.member("customer:acme"))
	if err != nil {
		assert.ErrorIs(t, err, auth.ErrScopeUnsupported)
		t.Logf("scoped caller refused at the store: %v", err)
		return
	}
	assert.Equal(t, []string{"acme-meter"}, acme, "a scoped caller reads its own customer's devices only")
}
