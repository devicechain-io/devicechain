// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package profilecheck

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

type fakeQuerier struct {
	respond func(variables map[string]any) (string, error)
	tenant  string
	url     string
}

func (f *fakeQuerier) Query(_ context.Context, baseURL, tenant, _ string, variables map[string]any, out any) error {
	f.url, f.tenant = baseURL, tenant
	data, err := f.respond(variables)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), out)
}

func TestMissingReportsOnlyTheTokensDeviceManagementDidNotReturn(t *testing.T) {
	q := &fakeQuerier{respond: func(map[string]any) (string, error) {
		return `{"deviceProfilesByToken":[{"token":"gateway"}]}`, nil
	}}
	missing, err := New(q, "http://dm/graphql").Missing(context.Background(), "acme", []string{"gateway", "ghost"})
	require.NoError(t, err)
	require.Equal(t, []string{"ghost"}, missing)
	require.Equal(t, "acme", q.tenant, "the lookup is made in the caller's tenant")
	require.Equal(t, "http://dm/graphql", q.url)
}

func TestAnErrorIsNeverReportedAsNothingMissing(t *testing.T) {
	q := &fakeQuerier{respond: func(map[string]any) (string, error) { return "", errors.New("connection refused") }}
	missing, err := New(q, "http://dm/graphql").Missing(context.Background(), "acme", []string{"gateway"})
	require.Error(t, err)
	require.Nil(t, missing)
}

// An answer that carries no profiles at all (an empty list) means every requested token is missing.
func TestAnEmptyAnswerMeansEverythingIsMissing(t *testing.T) {
	q := &fakeQuerier{respond: func(map[string]any) (string, error) { return `{"deviceProfilesByToken":[]}`, nil }}
	missing, err := New(q, "u").Missing(context.Background(), "acme", []string{"a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, missing)
}

func TestDeviceOfReadsTheProfileThroughTheDeviceType(t *testing.T) {
	q := &fakeQuerier{respond: func(v map[string]any) (string, error) {
		require.Equal(t, []string{"dev-1"}, v["tokens"])
		return `{"devicesByToken":[{"token":"dev-1","enabled":true,"deviceType":{"profile":{"token":"gateway"}}}],"deviceScopeAnchors":[]}`, nil
	}}
	facts, found, err := New(q, "http://dm/graphql").DeviceOf(context.Background(), "acme", "dev-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "gateway", facts.Profile)
	require.False(t, facts.Disabled)
	require.Equal(t, "acme", q.tenant)
}

func TestDeviceOfDistinguishesNoDeviceNoProfileAndNoAnswer(t *testing.T) {
	// A device on a type with no profile exists, and has an empty profile.
	q := &fakeQuerier{respond: func(map[string]any) (string, error) {
		return `{"devicesByToken":[{"token":"dev-1","enabled":true,"deviceType":{"profile":null}}],"deviceScopeAnchors":[]}`, nil
	}}
	facts, found, err := New(q, "u").DeviceOf(context.Background(), "acme", "dev-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, facts.Profile)

	// A device the tenant does not have is absent, not an error.
	q = &fakeQuerier{respond: func(map[string]any) (string, error) { return `{"devicesByToken":[],"deviceScopeAnchors":[]}`, nil }}
	_, found, err = New(q, "u").DeviceOf(context.Background(), "acme", "ghost")
	require.NoError(t, err)
	require.False(t, found)

	// An answer naming some other device is not this device.
	q = &fakeQuerier{respond: func(map[string]any) (string, error) {
		return `{"devicesByToken":[{"token":"other","enabled":true,"deviceType":{"profile":{"token":"gateway"}}}],"deviceScopeAnchors":[]}`, nil
	}}
	_, found, err = New(q, "u").DeviceOf(context.Background(), "acme", "dev-1")
	require.NoError(t, err)
	require.False(t, found)

	// A failed lookup is an error, never "not found".
	q = &fakeQuerier{respond: func(map[string]any) (string, error) { return "", errors.New("connection refused") }}
	_, found, err = New(q, "u").DeviceOf(context.Background(), "acme", "dev-1")
	require.Error(t, err)
	require.False(t, found)
}

// 🔴 `enabled` IS READ, AND ITS ABSENCE IS AN ERROR. A disabled device must read as disabled, and an
// answer that cannot say must never read as enabled: the lookup is what keeps a quarantined device
// out of the update plane.
func TestDeviceOfReadsTheDisabledFlagAndRefusesAnAnswerWithoutIt(t *testing.T) {
	q := &fakeQuerier{respond: func(map[string]any) (string, error) {
		return `{"devicesByToken":[{"token":"dev-1","enabled":false,"deviceType":{"profile":{"token":"gateway"}}}],"deviceScopeAnchors":[]}`, nil
	}}
	facts, found, err := New(q, "u").DeviceOf(context.Background(), "acme", "dev-1")
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, facts.Disabled)
	require.Equal(t, "gateway", facts.Profile)

	q = &fakeQuerier{respond: func(map[string]any) (string, error) {
		return `{"devicesByToken":[{"token":"dev-1","deviceType":{"profile":{"token":"gateway"}}}],"deviceScopeAnchors":[]}`, nil
	}}
	_, _, err = New(q, "u").DeviceOf(context.Background(), "acme", "dev-1")
	require.Error(t, err)
}

// The device's scope anchors (ADR-081) are read in the same request and returned with the
// facts: the stamp an assignment is written with. Only this device's are taken; a device
// nothing owns has none; an answer without the list is an error, never "nothing owns it".
func TestDeviceOfReadsTheScopeAnchors(t *testing.T) {
	q := &fakeQuerier{respond: func(v map[string]any) (string, error) {
		return `{"devicesByToken":[{"token":"dev-1","enabled":true,"deviceType":{"profile":{"token":"gateway"}}}],
		  "deviceScopeAnchors":[{"deviceToken":"other","anchors":["customer:globex"]},
		                        {"deviceToken":"dev-1","anchors":["customer:acme","area:north"]}]}`, nil
	}}
	facts, found, err := New(q, "u").DeviceOf(context.Background(), "acme", "dev-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []string{"customer:acme", "area:north"}, facts.Anchors)

	q = &fakeQuerier{respond: func(map[string]any) (string, error) {
		return `{"devicesByToken":[{"token":"dev-1","enabled":true,"deviceType":{"profile":null}}],"deviceScopeAnchors":[]}`, nil
	}}
	facts, _, err = New(q, "u").DeviceOf(context.Background(), "acme", "dev-1")
	require.NoError(t, err)
	require.Equal(t, []string{}, facts.Anchors)

	q = &fakeQuerier{respond: func(map[string]any) (string, error) {
		return `{"devicesByToken":[{"token":"dev-1","enabled":true,"deviceType":{"profile":null}}]}`, nil
	}}
	_, _, err = New(q, "u").DeviceOf(context.Background(), "acme", "dev-1")
	require.Error(t, err)
}

func TestResolveGroupTargetsRelaysAPageAndItsCursor(t *testing.T) {
	q := &fakeQuerier{respond: func(v map[string]any) (string, error) {
		require.Equal(t, "fleet", v["groupToken"])
		require.Nil(t, v["afterId"], "the first page carries no cursor, as an explicit null")
		require.Nil(t, v["version"])
		return `{"resolveDeviceGroupTargets":{"rejected":false,"deviceTokens":["d1","d2"],"nextCursor":"c2","resolvedVersion":3}}`, nil
	}}
	page, err := New(q, "u").ResolveGroupTargets(core.WithTenant(context.Background(), "acme"), "fleet", nil, "", 1000)
	require.NoError(t, err)
	require.Equal(t, "acme", q.tenant)
	require.False(t, page.Rejected)
	require.Equal(t, []string{"d1", "d2"}, page.DeviceTokens)
	require.Equal(t, "c2", page.NextCursor)
	require.Equal(t, int32(3), *page.ResolvedVersion)
}

func TestResolveGroupTargetsKeepsARejectionApartFromAnError(t *testing.T) {
	q := &fakeQuerier{respond: func(map[string]any) (string, error) {
		return `{"resolveDeviceGroupTargets":{"rejected":true,"code":"NOT_A_DEVICE_GROUP","reason":"collects assets","deviceTokens":[]}}`, nil
	}}
	page, err := New(q, "u").ResolveGroupTargets(core.WithTenant(context.Background(), "acme"), "g", nil, "", 10)
	require.NoError(t, err)
	require.True(t, page.Rejected)
	require.Equal(t, "NOT_A_DEVICE_GROUP", page.Code)

	// An answer with no verdict is a broken response, never an empty group.
	q = &fakeQuerier{respond: func(map[string]any) (string, error) {
		return `{"resolveDeviceGroupTargets":{"deviceTokens":[]}}`, nil
	}}
	_, err = New(q, "u").ResolveGroupTargets(core.WithTenant(context.Background(), "acme"), "g", nil, "", 10)
	require.Error(t, err)

	_, err = New(q, "u").ResolveGroupTargets(context.Background(), "g", nil, "", 10)
	require.Error(t, err, "no tenant, no lookup")
}
